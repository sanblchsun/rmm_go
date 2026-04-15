// builder/agent/main.go
package main

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"os/exec"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"unsafe"

	"github.com/go-vgo/robotgo"
	"github.com/gorilla/websocket"
	"github.com/pion/webrtc/v3"
	"github.com/pion/webrtc/v3/pkg/media"
)

// --- Конфигурация ---
const (
	serverURL                = "ws://192.168.88.127:8000/ws/agent/agent1"
	websocketMaxRetries      = 5               // Максимальное количество попыток подключения к WebSocket
	websocketRetryDelay      = 5 * time.Second // Задержка между попытками подключения к WebSocket
	MONITOR_DEFAULTTOPRIMARY = 1
	MONITORINFOF_PRIMARY     = 0x1
)

func init() {
	if runtime.GOOS == "windows" {
		initWindowsDPI()
	}
}

// --- Глобальные переменные ---
var (
	user32             = syscall.NewLazyDLL("user32.dll")
	setDPIAware        = user32.NewProc("SetProcessDPIAware")
	actualScreenWidth  int
	actualScreenHeight int
	activeDataChannel  *webrtc.DataChannel
	dcMutex            sync.RWMutex
	resolutionUpdates  = make(chan [2]int, 1)
	reResolution       = regexp.MustCompile(`(\d{3,5})x(\d{3,5})`)

	videoBytesSent  int64
	videoFramesSent int64
	videoStatsLock  sync.Mutex

	// Канал для сигнализации о перезапуске FFmpeg
	ffmpegRestartSignal = make(chan struct{}, 1)
	ffmpegMutex         sync.Mutex
	ffmpegStatsReset    = make(chan struct{}, 1)
	getDpiForWindow     = user32.NewProc("GetDpiForWindow")
	getDesktopWindow    = user32.NewProc("GetDesktopWindow")
	getSystemMetricsFor = user32.NewProc("GetSystemMetricsForDpi")
	// Добавлены для получения информации о мониторе
	monitorFromWindow = user32.NewProc("MonitorFromWindow")
	getMonitorInfo    = user32.NewProc("GetMonitorInfoW")
	// Функции для работы с окнами
	getForegroundWindow = user32.NewProc("GetForegroundWindow")
	getWindowRect       = user32.NewProc("GetWindowRect")
	getWindowText       = user32.NewProc("GetWindowTextW")
	getWindowTextLength = user32.NewProc("GetWindowTextLengthW")
	currentFFmpegCmd    *exec.Cmd
)

func initWindowsDPI() {
	setDPIAware.Call()
}

type MONITORINFOEX struct {
	CbSize    uint32
	RcMonitor RECT
	RcWork    RECT
	DwFlags   uint32
	SzDevice  [32]uint16 // TCHAR * 32
}

type RECT struct {
	Left   int32
	Top    int32
	Right  int32
	Bottom int32
}

type WindowInfo struct {
	Title   string
	X       int
	Y       int
	Width   int
	Height  int
	IsValid bool
}

// getPhysicalScreenSize получает разрешение экрана игнорируя системное масштабирование
// для текущего основного монитора.
func getPhysicalScreenSize() (int, int) {
	// Получаем десктопное окно
	hwnd, _, _ := getDesktopWindow.Call()
	if hwnd == 0 {
		return 0, 0
	}

	// Получаем хэндл монитора для десктопного окна
	hMonitor, _, _ := monitorFromWindow.Call(
		hwnd,
		uintptr(MONITOR_DEFAULTTOPRIMARY),
	)
	if hMonitor == 0 {
		return 0, 0
	}

	var mi MONITORINFOEX
	mi.CbSize = uint32(unsafe.Sizeof(mi))

	// Заполняем структуру информацией о мониторе
	ret, _, _ := getMonitorInfo.Call(hMonitor, uintptr(unsafe.Pointer(&mi)))
	if ret == 0 {
		return 0, 0
	}

	// RcMonitor содержит физические размеры монитора (без учета масштабирования DPI)
	width := int(mi.RcMonitor.Right - mi.RcMonitor.Left)
	height := int(mi.RcMonitor.Bottom - mi.RcMonitor.Top)

	if width == 0 || height == 0 {
		return 0, 0
	}

	return width, height
}

// sendScreenInfo отправляет информацию о разрешении экрана через DataChannel
func sendScreenInfo(dc *webrtc.DataChannel) {
	w, h := getPhysicalScreenSize()
	if w == 0 || h == 0 {
		w, h = detectResolution()
	}
	// Важно: actualScreenWidth и actualScreenHeight уже обновлены в других местах (init, screen watcher, ffmpeg parser).
	// Здесь мы просто отправляем текущие значения.
	info := map[string]interface{}{
		"type":   "screen_info",
		"width":  w, // Используем фактически найденные размеры
		"height": h, // Используем фактически найденные размеры
	}
	b, _ := json.Marshal(info)
	err := dc.SendText(string(b))
	if err != nil {
		log.Printf("[ERROR] Failed to send screen_info via DataChannel: %v", err)
	}
	log.Printf("[SCREEN] Reported size: %dx%d", w, h)
}

func getForegroundWindowInfo() WindowInfo {
	hwnd, _, _ := getForegroundWindow.Call()
	if hwnd == 0 {
		return WindowInfo{IsValid: false}
	}

	var rect RECT
	ret, _, _ := getWindowRect.Call(hwnd, uintptr(unsafe.Pointer(&rect)))
	if ret == 0 {
		return WindowInfo{IsValid: false}
	}

	length, _, _ := getWindowTextLength.Call(hwnd)
	title := ""
	if length > 0 {
		length++
		buf := make([]uint16, length)
		getWindowText.Call(hwnd, uintptr(unsafe.Pointer(&buf[0])), length)
		title = syscall.UTF16ToString(buf)
	}

	return WindowInfo{
		Title:   title,
		X:       int(rect.Left),
		Y:       int(rect.Top),
		Width:   int(rect.Right - rect.Left),
		Height:  int(rect.Bottom - rect.Top),
		IsValid: true,
	}
}

func sendWindowInfo(dc *webrtc.DataChannel) {
	if runtime.GOOS != "windows" {
		return
	}
	wi := getForegroundWindowInfo()
	if !wi.IsValid {
		return
	}
	info := map[string]interface{}{
		"type":   "window_info",
		"title":  wi.Title,
		"x":      wi.X,
		"y":      wi.Y,
		"width":  wi.Width,
		"height": wi.Height,
	}
	b, _ := json.Marshal(info)
	err := dc.SendText(string(b))
	if err != nil {
		log.Printf("[ERROR] Failed to send window_info via DataChannel: %v", err)
	}
}

// detectResolution определяет разрешение экрана, используя сначала ffmpeg (если есть),
// затем robotgo.
func detectResolution() (int, int) {
	var args []string
	if runtime.GOOS == "windows" {
		args = []string{"-f", "gdigrab", "-i", "desktop", "-vframes", "1", "-f", "null", "-"}
	} else {
		args = []string{"-f", "x11grab", "-i", ":0.0", "-vframes", "1", "-f", "null", "-"}
	}
	out, err := exec.Command("ffmpeg", args...).CombinedOutput()
	if err == nil {
		if m := reResolution.FindStringSubmatch(string(out)); len(m) == 3 {
			w, _ := strconv.Atoi(m[1])
			h, _ := strconv.Atoi(m[2])
			if w > 0 && h > 0 {
				log.Printf("[SCREEN] Detected resolution via ffmpeg: %dx%d", w, h)
				return w, h
			}
		}
	}
	// Если ffmpeg не смог определить или произошла ошибка, используем robotgo
	w, h := robotgo.GetScreenSize()
	log.Printf("[SCREEN] Fallback to RobotGo screen size: %dx%d", w, h)
	return w, h
}

// --- Основной запуск ---
func main() {
	log.SetFlags(log.LstdFlags | log.Lshortfile)
	log.Printf("Starting agent and connecting to signaling server: %s\n", serverURL)

	for i := 0; i < websocketMaxRetries; i++ {
		log.Printf("Attempt %d of %d to connect to WebSocket server...", i+1, websocketMaxRetries)
		err := runAgent()
		if err == nil {
			log.Println("Agent stopped gracefully.")
			break // Успешное завершение runAgent, выходим из цикла
		}
		log.Printf("Agent encountered an error: %v. Retrying in %v...", err, websocketRetryDelay)
		time.Sleep(websocketRetryDelay)
	}

	log.Printf("Exiting after %d failed WebSocket connection attempts.", websocketMaxRetries)
	os.Exit(1) // Выходим с кодом ошибки, если все попытки провалились
}

// runAgent содержит основную логику работы агента, включая WebSocket-соединение.
// Она возвращает nil при чистом закрытии WebSocket или ошибку при его разрыве.
func runAgent() error {
	ws, _, err := websocket.DefaultDialer.Dial(serverURL, nil)
	if err != nil {
		return fmt.Errorf("websocket connect error: %w", err)
	}
	defer ws.Close()
	log.Println("Connected to WebSocket server.")

	writeChan := make(chan []byte, 100)
	// Горутина для отправки сообщений через WebSocket
	go func() {
		for msg := range writeChan {
			err := ws.WriteMessage(websocket.TextMessage, msg)
			if err != nil {
				log.Printf("WebSocket write error: %v. Stopping write goroutine.", err)
				// В случае ошибки записи, закрываем канал, чтобы сообщить другим горутинам
				// о проблеме с WS и не блокироваться на writeChan.
				// Важно: не закрывать ws здесь, это делает defer в runAgent.
				return
			}
		}
	}()

	pcs := make(map[string]*webrtc.PeerConnection)
	var pcsLock sync.Mutex

	videoTrack, err := webrtc.NewTrackLocalStaticSample(
		webrtc.RTPCodecCapability{MimeType: webrtc.MimeTypeH264},
		"video", "rmm",
	)
	if err != nil {
		return fmt.Errorf("track create error: %w", err)
	}

	// Инициализация глобальных переменных разрешения перед первым запуском FFmpeg
	currentW, currentH := getPhysicalScreenSize()
	if currentW == 0 && currentH == 0 {
		currentW, currentH = detectResolution()
	}
	actualScreenWidth, actualScreenHeight = currentW, currentH
	log.Printf("Initial screen size set to: %dx%d", actualScreenWidth, actualScreenHeight)

	// Запускаем горутину, которая управляет жизненным циклом FFmpeg
	go manageFFmpegProcess(videoTrack)

	// Запускаем горутину, которая следит за разрешением экрана
	startScreenWatcher() // Теперь без DataChannel, следит только за изменением разрешения и сигнализирует FFmpeg

	// Запускаем горутину для вывода статистики
	startVideoStats()

	// Горутина, которая слушает обновления разрешения от FFmpeg
	go func() {
		for res := range resolutionUpdates {
			if actualScreenWidth == res[0] && actualScreenHeight == res[1] {
				// Разрешение совпадает с текущим, игнорируем
				continue
			}
			log.Printf("[FFmpeg] Video stream size detected %dx%d. Current: %dx%d. Signaling FFmpeg restart.",
				res[0], res[1], actualScreenWidth, actualScreenHeight)

			actualScreenWidth, actualScreenHeight = res[0], res[1] // Обновляем глобальные переменные

			// Сигнализируем о необходимости перезапустить FFmpeg
			select {
			case ffmpegRestartSignal <- struct{}{}:
				// Отправили сигнал, если канал не заблокирован
			default:
				// Канал заблокирован, значит, сигнал уже в очереди или процесс перезапуска уже идет
				log.Println("[FFmpeg] Restart signal already pending, skipping.")
			}

			// Также сразу отправляем информацию об изменении разрешения через DataChannel, если он активен
			dcMutex.RLock()
			dc := activeDataChannel
			dcMutex.RUnlock()

			if dc != nil {
				info := map[string]interface{}{
					"type":   "screen_info",
					"width":  res[0],
					"height": res[1],
				}
				b, _ := json.Marshal(info)
				err := activeDataChannel.SendText(string(b))
				if err != nil {
					log.Printf("[ERROR] Failed to send updated screen_info via DataChannel: %v", err)
				}
				log.Printf("[FFmpeg] Sent updated screen_info: %dx%d", res[0], res[1])
			}
		}
	}()

	// Основной цикл чтения сообщений из WebSocket
	for {
		_, msg, err := ws.ReadMessage()
		if err != nil {
			if websocket.IsCloseError(err, websocket.CloseNormalClosure, websocket.CloseGoingAway) {
				log.Println("WebSocket closed cleanly.")
				return nil // Чистое закрытие WS, можно не переподключаться
			}
			return fmt.Errorf("websocket read error: %w", err) // Ошибка чтения, вызываем переподключение
		}
		if handleSDP(msg, writeChan, pcs, &pcsLock, videoTrack) {
			continue
		}
		handleICE(msg, pcs, &pcsLock)
	}
}

// manageFFmpegProcess управляет жизненным циклом процесса FFmpeg.
// Он запускает FFmpeg, следит за ним и перезапускает по сигналу или при сбое.
func manageFFmpegProcess(videoTrack *webrtc.TrackLocalStaticSample) {
	for {
		log.Println("[FFmpeg Manager] Starting new FFmpeg process cycle...")

		// Канал для сигнализации горутинам чтения stdout/stderr, что надо завершаться.
		// Важно: он должен быть создан заново для каждого нового цикла FFmpeg,
		// чтобы корректно сигнализировать новым горутинам.
		quitSignal := make(chan struct{})

		ffmpegMutex.Lock()
		// Прежде чем запускать новый FFmpeg, убедимся, что предыдущий процесс полностью завершен
		// и его ссылки обнулены. Это дополнительная мера предосторожности.
		if currentFFmpegCmd != nil && currentFFmpegCmd.Process != nil {
			log.Println("[FFmpeg Manager] Warning: Previous FFmpeg process still active when starting new cycle. Terminating it.")
			_ = currentFFmpegCmd.Process.Kill()
		}
		ffmpegMutex.Unlock()

		var cmd *exec.Cmd
		var stdout io.ReadCloser
		var stderr io.ReadCloser
		var err error

		var args []string
		if runtime.GOOS == "windows" {
			args = []string{"-f", "gdigrab", "-framerate", "30", "-draw_mouse", "0", "-i", "desktop"}
		} else {
			args = []string{"-f", "x11grab", "-framerate", "30", "-draw_mouse", "0", "-i", ":0.0"}
		}

		if actualScreenWidth > 0 && actualScreenHeight > 0 {
			args = append(args, "-s", fmt.Sprintf("%dx%d", actualScreenWidth, actualScreenHeight))
		}

		args = append(args,
			"-vcodec", "libx264", "-preset", "ultrafast",
			"-tune", "zerolatency", "-tune", "zerolatency",
			"-pix_fmt", "yuv420p", "-g", "60", "-keyint_min", "30",
			"-b:v", "4M", "-maxrate", "6M", "-bufsize", "8M",
			"-fflags", "nobuffer",
			"-f", "h264", "-",
		)

		log.Printf("[FFmpeg Manager] Executing command: ffmpeg %v", strings.Join(args, " "))
		cmd = exec.Command("ffmpeg", args...)

		// Сохраняем ссылку на текущий процесс сразу после создания, чтобы он был доступен для остановки
		ffmpegMutex.Lock()
		currentFFmpegCmd = cmd
		ffmpegMutex.Unlock()

		stdout, err = cmd.StdoutPipe()
		if err != nil {
			log.Printf("[FFmpeg Manager] FFmpeg stdout pipe error: %v. Restarting in 5s.", err)
			time.Sleep(5 * time.Second)
			close(quitSignal) // Закрываем quitSignal для всех, кто мог начать его слушать (маловероятно здесь)
			continue
		}
		stderr, err = cmd.StderrPipe()
		if err != nil {
			log.Printf("[FFmpeg Manager] FFmpeg stderr pipe error: %v. Restarting in 5s.", err)
			_ = stdout.Close()
			time.Sleep(5 * time.Second)
			close(quitSignal)
			continue
		}
		// Не читаем stderr - просто закрываем чтобы избежать блокировки FFmpeg
		go func() { io.Copy(io.Discard, stderr) }()

		if err = cmd.Start(); err != nil {
			log.Printf("[FFmpeg Manager] FFmpeg command start error: %v. Restarting in 5s.", err)
			_ = stdout.Close()
			_ = stderr.Close()
			time.Sleep(5 * time.Second)
			close(quitSignal)
			continue
		}
		log.Println("[FFmpeg Manager] FFmpeg process started successfully.")

		var wg sync.WaitGroup
		wg.Add(1)

		// Горутина для стриминга видео (чтение stdout и отправка в WebRTC)
		go func() {
			defer wg.Done()
			streamVideo(stdout, videoTrack, quitSignal)
		}()

		// Горутина для ожидания завершения процесса FFmpeg.
		// Как только FFmpeg завершится, она пошлет сигнал завершения всем остальным горутинам
		// и затем дождется их.
		ffmpegMonitorDone := make(chan struct{})
		go func() {
			defer close(ffmpegMonitorDone) // Сигнализируем, что монитор FFmpeg завершился
			err := cmd.Wait()              // Ждем завершения FFmpeg
			if err != nil {
				log.Printf("[FFmpeg Manager] FFmpeg process exited with error: %v", err)
			} else {
				log.Println("[FFmpeg Manager] FFmpeg process exited normally.")
			}
			// Когда FFmpeg завершился, сигнализируем читающим горутинам, чтобы они тоже завершились.
			log.Println("[FFmpeg Manager] FFmpeg process finished. Sending quit signal to reader goroutines.")
			close(quitSignal) // Закрываем канал, чтобы все получатели завершились
		}()

		// Основной менеджер ждет одного из двух событий:
		// 1. Сигнал на перезапуск от screen watcher / resolutionUpdates.
		// 2. Штатное или ошибочное завершение FFmpeg (через ffmpegMonitorDone).
		select {
		case <-ffmpegRestartSignal:
			log.Println("[FFmpeg Manager] Received external restart signal. Terminating current FFmpeg process.")
			// Если мы получили сигнал на перезапуск, нам нужно "убить" текущий процесс FFmpeg.
			// Горутина ffmpegMonitorDone обнаружит это завершение и сама отправит quitSignal.
			ffmpegMutex.Lock()
			if cmd != nil && cmd.Process != nil {
				log.Println("[FFmpeg Manager] Terminating FFmpeg process...")
				if runtime.GOOS == "windows" {
					_ = cmd.Process.Kill()
				} else {
					if err := cmd.Process.Signal(syscall.SIGTERM); err != nil {
						log.Printf("[FFmpeg Manager] Failed to send SIGTERM to FFmpeg: %v. Trying Kill.", err)
						_ = cmd.Process.Kill()
					}
				}
			}
			ffmpegMutex.Unlock()
			select {
			case ffmpegStatsReset <- struct{}{}:
			default:
			}
		case <-ffmpegMonitorDone:
			log.Println("[FFmpeg Manager] FFmpeg process completed its lifecycle (exited or errored). Moving to next cycle.")
			// В этом случае quitSignal уже был отправлен горутиной ffmpegMonitorDone.
		}

		// Ждем, пока все дочерние горутины, связанные с этим циклом FFmpeg, завершатся.
		wg.Wait()
		<-ffmpegMonitorDone // Удостоверяемся, что горутина-монитор FFmpeg также завершилась.
		log.Println("[FFmpeg Manager] All components of previous FFmpeg cycle stopped. Preparing for next run.")
		time.Sleep(1 * time.Second) // Небольшая пауза перед следующим запуском
	}
}

// currentFFmpegCmd хранит ссылку на последний запущенный `ffmpeg.Cmd`.
// Доступ к нему должен быть синхронизирован через `ffmpegMutex`.

// --- SDP/ICE ---
func handleSDP(msg []byte, out chan []byte, pcs map[string]*webrtc.PeerConnection,
	lock *sync.Mutex, videoTrack *webrtc.TrackLocalStaticSample) bool {

	var sdp webrtc.SessionDescription
	if err := json.Unmarshal(msg, &sdp); err != nil || sdp.Type != webrtc.SDPTypeOffer {
		return false
	}

	lock.Lock()
	if old, ok := pcs["viewer"]; ok {
		log.Printf("Closing old PeerConnection for 'viewer'.")
		_ = old.Close()
	}
	pc, err := newPeerConnection(out, videoTrack)
	if err != nil {
		lock.Unlock()
		log.Printf("PeerConnection error: %v", err)
		return true
	}
	pcs["viewer"] = pc
	lock.Unlock()

	_ = pc.SetRemoteDescription(sdp)

	answer, err := pc.CreateAnswer(nil)
	if err != nil {
		log.Printf("CreateAnswer error: %v", err)
		return true
	}
	_ = pc.SetLocalDescription(answer)
	payload, _ := json.Marshal(answer)
	out <- payload

	return true
}

func newPeerConnection(out chan []byte,
	videoTrack *webrtc.TrackLocalStaticSample) (*webrtc.PeerConnection, error) {

	pc, err := webrtc.NewPeerConnection(webrtc.Configuration{
		ICEServers: []webrtc.ICEServer{},
	})
	if err != nil {
		return nil, err
	}

	if _, err := pc.AddTrack(videoTrack); err != nil {
		log.Printf("AddTrack error: %v", err)
	}

	pc.OnDataChannel(func(dc *webrtc.DataChannel) {
		activeDataChannel = dc
		dc.OnOpen(func() {
			log.Println("DataChannel opened")
			sendScreenInfo(dc)
		})
		dc.OnMessage(func(msg webrtc.DataChannelMessage) {
			handleControl(msg.Data)
		})
		dc.OnClose(func() {
			log.Println("DataChannel closed")
			activeDataChannel = nil
		})
	})

	pc.OnICECandidate(func(c *webrtc.ICECandidate) {
		if c != nil {
			if payload, err := json.Marshal(c.ToJSON()); err == nil {
				out <- payload
			}
		}
	})

	pc.OnConnectionStateChange(func(s webrtc.PeerConnectionState) {
		log.Printf("Peer Connection State has changed to %s\n", s.String())
		if s == webrtc.PeerConnectionStateFailed || s == webrtc.PeerConnectionStateClosed {
			log.Printf("PeerConnection %s, detaching activeDataChannel.", s.String())
			activeDataChannel = nil // Сбрасываем ссылку на активный канал при разрыве соединения
		}
	})

	return pc, nil
}

func handleICE(msg []byte, pcs map[string]*webrtc.PeerConnection, lock *sync.Mutex) {
	var ice webrtc.ICECandidateInit
	if err := json.Unmarshal(msg, &ice); err != nil || ice.Candidate == "" {
		// Некоторые сообщения могут быть не ICE-кандидатами, игнорируем их
		return
	}
	lock.Lock()
	defer lock.Unlock()
	for _, pc := range pcs {
		if pc.RemoteDescription() != nil { // Кандидаты можно добавлять только после установки RemoteDescription
			err := pc.AddICECandidate(ice)
			if err != nil {
				log.Printf("AddICECandidate error: %v", err)
			}
		}
	}
}

// startScreenWatcher периодически проверяет изменение размеров экрана и сигнализирует о перезапуске FFmpeg.
// Оно больше не принимает DataChannel.
func startScreenWatcher() {
	go func() {
		// Используем локальные переменные для отслеживания текущего состояния экрана
		//, чтобы не зависеть от активного DataChannel.
		prevW, prevH := actualScreenWidth, actualScreenHeight
		for {
			time.Sleep(3 * time.Second)

			w, h := getPhysicalScreenSize()
			// Добавим fallback на detectResolution если getPhysicalScreenSize возвращает 0,
			// т.к. getPhysicalScreenSize может не всегда работать.
			if w == 0 || h == 0 {
				w, h = detectResolution()
			}

			if w != prevW || h != prevH {
				log.Printf("[SCREEN] Detected screen size change: %dx%d -> %dx%d.", prevW, prevH, w, h)
				prevW, prevH = w, h
				// Обновляем глобальные переменные, которые используются FFmpeg
				actualScreenWidth, actualScreenHeight = w, h

				// Отправляем сигнал на перезапуск FFmpeg
				select {
				case ffmpegRestartSignal <- struct{}{}:
					log.Println("[SCREEN] Signaling FFmpeg restart due to resolution change.")
				default:
					// Канал заблокирован, значит, сигнал уже в очереди
					log.Println("[SCREEN] Restart signal already pending from screen watcher, skipping.")
				}

				// Если DataChannel активен, отправляем ему информацию об изменении разрешения.
				dcMutex.RLock()
				dc := activeDataChannel
				dcMutex.RUnlock()

				if dc != nil {
					info := map[string]interface{}{
						"type":   "screen_info",
						"width":  w,
						"height": h,
					}
					b, _ := json.Marshal(info)
					err := activeDataChannel.SendText(string(b))
					if err != nil {
						log.Printf("[ERROR] Failed to send screen_info via DataChannel: %v", err)
					}
					log.Printf("[SCREEN] Sent updated screen_info: %dx%d", w, h)
				}
			}
		}
	}()
}

// parseFFmpegResolution читает stderr FFmpeg и парсит разрешение.
// Оно завершается, если получает сигнал из канала `quit`.
func parseFFmpegResolution(r io.Reader, quit <-chan struct{}) {
	const maxCapacity = 64 * 1024 // 64KB - меньше чем 1MB для защиты от краха
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 64*1024), maxCapacity)

	for {
		select {
		case <-quit:
			return
		default:
		}
		line, ok := func() (string, bool) {
			defer func() {
				if r := recover(); r != nil {
					log.Printf("[FFmpeg Stderr] Recovered from panic: %v", r)
				}
			}()
			if !scanner.Scan() {
				return "", false
			}
			return scanner.Text(), true
		}()
		if !ok || line == "" {
			return
		}
		if strings.Contains(line, "Video:") {
			if m := reResolution.FindStringSubmatch(line); len(m) == 3 {
				w, _ := strconv.Atoi(m[1])
				h, _ := strconv.Atoi(m[2])
				if w > 0 && h > 0 {
					select {
					case resolutionUpdates <- [2]int{w, h}:
					default:
					}
				}
			}
		}
	}
}

// streamVideo считывает видеоданные из stdout FFmpeg и отправляет их в videoTrack.
// Оно завершается, если получает сигнал из канала `quit`.
func streamVideo(r io.Reader, videoTrack *webrtc.TrackLocalStaticSample, quit <-chan struct{}) {
	reader := bufio.NewReader(r)
	const maxNALUBufferSize = 2 * 1024 * 1024 // 2MB
	buf := make([]byte, 0, maxNALUBufferSize)

	tmp := make([]byte, 4096) // Буфер для чтения из io.Reader
	for {
		select {
		case <-quit:
			log.Println("[FFmpeg Video Stream] Quit signal received, stopping streaming.")
			return
		default:
			n, err := reader.Read(tmp)
			if err != nil {
				if !errors.Is(err, io.EOF) {
					log.Printf("[FFmpeg Video Stream] FFmpeg read error: %v", err)
				}
				log.Println("[FFmpeg Video Stream] EOF or pipe closed.")
				return // Завершаем горутину, FFmpeg, вероятно, прекратил работу
			}
			// Проверяем, не превысит ли добавление текущих данных максимально допустимый размер буфера.
			if len(buf)+n > maxNALUBufferSize {
				log.Printf("[FFmpeg Video Stream] NALU buffer exceeded max capacity (%d bytes). This indicates a problem like missing start codes or corrupted stream. Exiting streamer.", maxNALUBufferSize)
				// В данном случае, повреждение потока или отсутствие стартовых кодов может привести
				// к тому, что NALU никогда не будут найдены и буфер будет расти бесконечно.
				// Лучше дать manageFFmpegProcess перезапустить FFmpeg.
				return
			}
			buf = append(buf, tmp[:n]...)

			for {
				start := findStartCode(buf)
				if start == -1 {
					break // Неполный NALU, ждем еще данных
				}

				next := findStartCode(buf[start+4:])
				if next == -1 {
					break // Не нашли следующий стартовый код, ждем
				}
				next += start + 4 // Смещаем relative next pointer к абсолютному

				nalu := buf[start:next]

				// Дополнительная проверка на пустой NALU после findStartCode,
				// хотя по логике findStartCode(buf[start+4:]) это должно исключать.
				if len(nalu) == 0 {
					// Если по какой-то причине NALU оказался пустым, пропускаем его.
					buf = buf[next:] // Продолжаем поиск в оставшейся части
					continue
				}

				select {
				case <-quit:
					log.Println("[FFmpeg Video Stream] Quit signal received during NALU processing, stopping.")
					return
				default:
					_ = videoTrack.WriteSample(media.Sample{Data: nalu, Duration: time.Second / 30})

					videoStatsLock.Lock()
					videoBytesSent += int64(len(nalu))
					videoFramesSent++
					videoStatsLock.Unlock()
				}
				buf = buf[next:] // Обрезаем буфер, оставляя только необработанные данные
			}
		}
	}
}

// findStartCode находит стартовый код H.264 NALU (00 00 00 01).
func findStartCode(data []byte) int {
	for i := 0; i < len(data)-3; i++ {
		// Оптимизация: проверять только если первый байт 0, так как стартовый код начинается с 00 00 00 01
		if data[i] == 0 && data[i+1] == 0 && data[i+2] == 0 && data[i+3] == 1 {
			return i
		}
	}
	return -1
}

// --- Управление вводом ---
func handleControl(data []byte) {
	var ctl map[string]interface{}
	if err := json.Unmarshal(data, &ctl); err != nil {
		log.Printf("[CONTROL] bad json: %v", err)
		return
	}

	t, _ := ctl["type"].(string)
	// log.Printf("[CONTROL] %+v", ctl) // Закомментировано для уменьшения логов, если часто срабатывает

	switch t {
	case "request_screen_info":
		dcMutex.RLock()
		dc := activeDataChannel
		dcMutex.RUnlock()

		if dc != nil {
			sendScreenInfo(activeDataChannel)
		}

	case "request_window_info":
		dcMutex.RLock()
		dc := activeDataChannel
		dcMutex.RUnlock()

		if dc != nil {
			sendWindowInfo(activeDataChannel)
		}

	case "mouse_move":
		x, okX := ctl["x"].(float64)
		y, okY := ctl["y"].(float64)
		if !okX || !okY {
			return
		}
		// Используем actualScreenWidth/Height для масштабирования,
		// чтобы корректно преобразовать координаты от клиента
		// к текущему физическому разрешению.
		currentDisplayW, currentDisplayH := robotgo.GetScreenSize()
		// Можно добавить использование getPhysicalScreenSize, если оно надежнее
		// для получения ОС-актуального размера для mouse_move.
		// w, h := getPhysicalScreenSize()
		// if w == 0 || h == 0 { w, h = currentDisplayW, currentDisplayH }

		tw := actualScreenWidth // Разрешение, которое мы сообщаем клиенту
		th := actualScreenHeight
		if tw == 0 || th == 0 { // Fallback, если вдруг глобальные размеры еще не установлены
			tw, th = currentDisplayW, currentDisplayH
		}

		// Вычисляем коэффициент масштабирования
		scaleX := float64(currentDisplayW) / float64(tw)
		scaleY := float64(currentDisplayH) / float64(th)

		safeX := clampInt(int(x*scaleX), 0, currentDisplayW-1)
		safeY := clampInt(int(y*scaleY), 0, currentDisplayH-1)
		robotgo.MoveMouse(safeX, safeY)

	case "mouse_down", "mouse_up":
		// robotgo ожидает "left", "middle", "right"
		// Предполагаем, что "button" от 0 (left), 1 (middle), 2 (right)
		btnF, ok := ctl["button"].(float64)
		if !ok {
			log.Println("[CONTROL] invalid button")
			return
		}
		btn := int(btnF)
		names := []string{"left", "middle", "right"}
		if btn < 0 || btn >= len(names) {
			log.Printf("[CONTROL] Unknown mouse button: %d", btn)
			return
		}
		if t == "mouse_down" {
			robotgo.MouseDown(names[btn])
		} else {
			robotgo.MouseUp(names[btn])
		}

	case "mouse_toggle": // Некоторые реализации могут посылать mouse_toggle
		btnF, ok := ctl["button"].(float64)
		if !ok {
			log.Println("[CONTROL] invalid button")
			return
		}
		btn := int(btnF)
		state, ok := ctl["state"].(string)
		if !ok {
			return
		}
		names := []string{"left", "middle", "right"}
		if btn < 0 || btn >= len(names) {
			log.Printf("[CONTROL] Unknown mouse button: %d", btn)
			return
		}
		if state == "down" {
			robotgo.MouseDown(names[btn])
		} else if state == "up" {
			robotgo.MouseUp(names[btn])
		}

	case "mouse_click": // Некоторые реализации могут посылать mouse_click
		btnF, ok := ctl["button"].(float64)
		if !ok {
			log.Println("[CONTROL] invalid button")
			return
		}
		btn := int(btnF)
		names := []string{"left", "middle", "right"}
		if btn >= 0 && btn < len(names) {
			robotgo.Click(names[btn])
		}

	case "key_down":
		key_str, ok := ctl["key"].(string)
		if !ok {
			log.Println("[CONTROL] Key event missing 'key' field.")
			return
		}
		robotgo.KeyDown(key_str)

	case "key_up":
		key_str, ok := ctl["key"].(string)
		if !ok {
			log.Println("[CONTROL] Key event missing 'key' field.")
			return
		}
		robotgo.KeyUp(key_str)

	case "key_press": // Для имитации однократного нажатия
		key_str, ok := ctl["key"].(string)
		if !ok {
			log.Println("[CONTROL] Key event missing 'key' field.")
			return
		}
		robotgo.KeyTap(key_str)

	default:
		log.Printf("[CONTROL] Unhandled event type: %s", t)
	}
}

// clampInt ограничивает значение `v` между `min` и `max`.
func clampInt(v, min, max int) int {
	if v < min {
		return min
	}
	if v > max {
		return max
	}
	return v
}

// startVideoStats выводит статистику видеопотока каждые 3 секунды
func startVideoStats() {
	go func() {
		var prevBytes int64
		var prevFrames int64
		ticker := time.NewTicker(3 * time.Second)
		defer ticker.Stop()

		for {
			select {
			case <-ffmpegStatsReset:
				videoStatsLock.Lock()
				prevBytes = videoBytesSent
				prevFrames = videoFramesSent
				videoStatsLock.Unlock()
				log.Println("[STATS] Counters reset due to FFmpeg restart")
			case <-ticker.C:
				videoStatsLock.Lock()
				bytesDelta := videoBytesSent - prevBytes
				framesDelta := videoFramesSent - prevFrames
				prevBytes = videoBytesSent
				prevFrames = videoFramesSent
				videoStatsLock.Unlock()

				fps := float64(framesDelta) / 3.0
				mbps := float64(bytesDelta) * 8 / 1_000_000 / 3.0

				log.Printf("[STATS] FPS: %.1f | Traffic: %.2f Mbps | Frames: %d | Total: %s",
					fps, mbps, framesDelta, formatBytes(videoBytesSent))
			}
		}
	}()
}

func formatBytes(n int64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	div, exp := int64(unit), 0
	for n >= div {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %cB", float64(n)/float64(div), "KMGTPE"[exp])
}
