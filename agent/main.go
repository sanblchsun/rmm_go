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
	websocketMaxRetries      = 5
	websocketRetryDelay      = 5 * time.Second
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

	ffmpegRestartSignal = make(chan struct{}, 1)
	ffmpegMutex         sync.Mutex
	ffmpegStatsReset    = make(chan struct{}, 1)
	getDpiForWindow     = user32.NewProc("GetDpiForWindow")
	getDesktopWindow    = user32.NewProc("GetDesktopWindow")
	getSystemMetricsFor = user32.NewProc("GetSystemMetricsForDpi")
	monitorFromWindow   = user32.NewProc("MonitorFromWindow")
	getMonitorInfo      = user32.NewProc("GetMonitorInfoW")
	getForegroundWindow = user32.NewProc("GetForegroundWindow")
	getWindowRect       = user32.NewProc("GetWindowRect")
	getWindowText       = user32.NewProc("GetWindowTextW")
	getWindowTextLength = user32.NewProc("GetWindowTextLengthW")

	// ИСПРАВЛЕНО #10: комментарий перенесён к объявлению переменной.
	// currentFFmpegCmd хранит ссылку на последний запущенный exec.Cmd FFmpeg.
	// Доступ к нему должен быть синхронизирован через ffmpegMutex.
	currentFFmpegCmd *exec.Cmd
)

func initWindowsDPI() {
	setDPIAware.Call()
}

type MONITORINFOEX struct {
	CbSize    uint32
	RcMonitor RECT
	RcWork    RECT
	DwFlags   uint32
	SzDevice  [32]uint16
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

func getPhysicalScreenSize() (int, int) {
	hwnd, _, _ := getDesktopWindow.Call()
	if hwnd == 0 {
		return 0, 0
	}

	hMonitor, _, _ := monitorFromWindow.Call(
		hwnd,
		uintptr(MONITOR_DEFAULTTOPRIMARY),
	)
	if hMonitor == 0 {
		return 0, 0
	}

	var mi MONITORINFOEX
	mi.CbSize = uint32(unsafe.Sizeof(mi))

	ret, _, _ := getMonitorInfo.Call(hMonitor, uintptr(unsafe.Pointer(&mi)))
	if ret == 0 {
		return 0, 0
	}

	width := int(mi.RcMonitor.Right - mi.RcMonitor.Left)
	height := int(mi.RcMonitor.Bottom - mi.RcMonitor.Top)

	if width == 0 || height == 0 {
		return 0, 0
	}

	return width, height
}

func sendScreenInfo(dc *webrtc.DataChannel) {
	w, h := getPhysicalScreenSize()
	if w == 0 || h == 0 {
		w, h = detectResolution()
	}
	info := map[string]interface{}{
		"type":   "screen_info",
		"width":  w,
		"height": h,
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
			break
		}
		log.Printf("Agent encountered an error: %v. Retrying in %v...", err, websocketRetryDelay)
		time.Sleep(websocketRetryDelay)
	}

	log.Printf("Exiting after %d failed WebSocket connection attempts.", websocketMaxRetries)
	os.Exit(1)
}

func runAgent() error {
	ws, _, err := websocket.DefaultDialer.Dial(serverURL, nil)
	if err != nil {
		return fmt.Errorf("websocket connect error: %w", err)
	}
	defer ws.Close()
	log.Println("Connected to WebSocket server.")

	writeChan := make(chan []byte, 100)
	go func() {
		for msg := range writeChan {
			err := ws.WriteMessage(websocket.TextMessage, msg)
			if err != nil {
				log.Printf("WebSocket write error: %v. Stopping write goroutine.", err)
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

	currentW, currentH := getPhysicalScreenSize()
	if currentW == 0 && currentH == 0 {
		currentW, currentH = detectResolution()
	}
	actualScreenWidth, actualScreenHeight = currentW, currentH
	log.Printf("Initial screen size set to: %dx%d", actualScreenWidth, actualScreenHeight)

	go manageFFmpegProcess(videoTrack)
	startScreenWatcher()
	startVideoStats()

	go func() {
		for res := range resolutionUpdates {
			if actualScreenWidth == res[0] && actualScreenHeight == res[1] {
				continue
			}
			log.Printf("[FFmpeg] Video stream size detected %dx%d. Current: %dx%d. Signaling FFmpeg restart.",
				res[0], res[1], actualScreenWidth, actualScreenHeight)

			actualScreenWidth, actualScreenHeight = res[0], res[1]

			select {
			case ffmpegRestartSignal <- struct{}{}:
			default:
				log.Println("[FFmpeg] Restart signal already pending, skipping.")
			}

			// ИСПРАВЛЕНО #8: используем локальную переменную dc (прочитанную
			// под мьютексом), а не activeDataChannel напрямую.
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
				err := dc.SendText(string(b)) // ИСПРАВЛЕНО #8
				if err != nil {
					log.Printf("[ERROR] Failed to send updated screen_info via DataChannel: %v", err)
				}
				log.Printf("[FFmpeg] Sent updated screen_info: %dx%d", res[0], res[1])
			}
		}
	}()

	for {
		_, msg, err := ws.ReadMessage()
		if err != nil {
			if websocket.IsCloseError(err, websocket.CloseNormalClosure, websocket.CloseGoingAway) {
				log.Println("WebSocket closed cleanly.")
				return nil
			}
			return fmt.Errorf("websocket read error: %w", err)
		}
		if handleSDP(msg, writeChan, pcs, &pcsLock, videoTrack) {
			continue
		}
		handleICE(msg, pcs, &pcsLock)
	}
}

func manageFFmpegProcess(videoTrack *webrtc.TrackLocalStaticSample) {
	for {
		log.Println("[FFmpeg Manager] Starting new FFmpeg process cycle...")

		quitSignal := make(chan struct{})

		ffmpegMutex.Lock()
		if currentFFmpegCmd != nil && currentFFmpegCmd.Process != nil {
			log.Println("[FFmpeg Manager] Warning: Previous FFmpeg process still active. Terminating it.")
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

		// ИСПРАВЛЕНО #5: удалён дублирующийся флаг "-tune zerolatency".
		args = append(args,
			"-vcodec", "libx264", "-preset", "ultrafast",
			"-tune", "zerolatency",
			"-pix_fmt", "yuv420p", "-g", "60", "-keyint_min", "30",
			"-b:v", "4M", "-maxrate", "6M", "-bufsize", "8M",
			"-fflags", "nobuffer",
			"-f", "h264", "-",
		)

		log.Printf("[FFmpeg Manager] Executing command: ffmpeg %v", strings.Join(args, " "))
		cmd = exec.Command("ffmpeg", args...)

		ffmpegMutex.Lock()
		currentFFmpegCmd = cmd
		ffmpegMutex.Unlock()

		stdout, err = cmd.StdoutPipe()
		if err != nil {
			log.Printf("[FFmpeg Manager] FFmpeg stdout pipe error: %v. Restarting in 5s.", err)
			time.Sleep(5 * time.Second)
			close(quitSignal)
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
		// ИСПРАВЛЕНО #9: parseFFmpegResolution никогда не вызывалась —
		// stderr просто отбрасывается. Функция parseFFmpegResolution удалена
		// как мёртвый код.
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

		go func() {
			defer wg.Done()
			streamVideo(stdout, videoTrack, quitSignal)
		}()

		ffmpegMonitorDone := make(chan struct{})
		go func() {
			defer close(ffmpegMonitorDone)
			err := cmd.Wait()
			if err != nil {
				log.Printf("[FFmpeg Manager] FFmpeg process exited with error: %v", err)
			} else {
				log.Println("[FFmpeg Manager] FFmpeg process exited normally.")
			}
			log.Println("[FFmpeg Manager] FFmpeg process finished. Sending quit signal to reader goroutines.")
			close(quitSignal)
		}()

		select {
		case <-ffmpegRestartSignal:
			log.Println("[FFmpeg Manager] Received external restart signal. Terminating current FFmpeg process.")
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
			log.Println("[FFmpeg Manager] FFmpeg process completed its lifecycle. Moving to next cycle.")
		}

		wg.Wait()
		<-ffmpegMonitorDone
		log.Println("[FFmpeg Manager] All components of previous FFmpeg cycle stopped. Preparing for next run.")
		time.Sleep(1 * time.Second)
	}
}

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
		// ИСПРАВЛЕНО #7: запись activeDataChannel защищена мьютексом.
		dcMutex.Lock()
		activeDataChannel = dc
		dcMutex.Unlock()

		dc.OnOpen(func() {
			log.Println("DataChannel opened")
			sendScreenInfo(dc)
		})
		dc.OnMessage(func(msg webrtc.DataChannelMessage) {
			handleControl(msg.Data)
		})
		dc.OnClose(func() {
			log.Println("DataChannel closed")
			// ИСПРАВЛЕНО #7: обнуление activeDataChannel защищено мьютексом.
			dcMutex.Lock()
			activeDataChannel = nil
			dcMutex.Unlock()
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
			// ИСПРАВЛЕНО #7: обнуление activeDataChannel защищено мьютексом.
			dcMutex.Lock()
			activeDataChannel = nil
			dcMutex.Unlock()
		}
	})

	return pc, nil
}

func handleICE(msg []byte, pcs map[string]*webrtc.PeerConnection, lock *sync.Mutex) {
	var ice webrtc.ICECandidateInit
	if err := json.Unmarshal(msg, &ice); err != nil || ice.Candidate == "" {
		return
	}
	lock.Lock()
	defer lock.Unlock()
	for _, pc := range pcs {
		if pc.RemoteDescription() != nil {
			err := pc.AddICECandidate(ice)
			if err != nil {
				log.Printf("AddICECandidate error: %v", err)
			}
		}
	}
}

func startScreenWatcher() {
	go func() {
		prevW, prevH := actualScreenWidth, actualScreenHeight
		for {
			time.Sleep(3 * time.Second)

			w, h := getPhysicalScreenSize()
			if w == 0 || h == 0 {
				w, h = detectResolution()
			}

			if w != prevW || h != prevH {
				log.Printf("[SCREEN] Detected screen size change: %dx%d -> %dx%d.", prevW, prevH, w, h)
				prevW, prevH = w, h
				actualScreenWidth, actualScreenHeight = w, h

				select {
				case ffmpegRestartSignal <- struct{}{}:
					log.Println("[SCREEN] Signaling FFmpeg restart due to resolution change.")
				default:
					log.Println("[SCREEN] Restart signal already pending from screen watcher, skipping.")
				}

				// ИСПРАВЛЕНО #8: используем локальную переменную dc.
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
					err := dc.SendText(string(b)) // ИСПРАВЛЕНО #8
					if err != nil {
						log.Printf("[ERROR] Failed to send screen_info via DataChannel: %v", err)
					}
					log.Printf("[SCREEN] Sent updated screen_info: %dx%d", w, h)
				}
			}
		}
	}()
}

func streamVideo(r io.Reader, videoTrack *webrtc.TrackLocalStaticSample, quit <-chan struct{}) {
	reader := bufio.NewReader(r)
	const maxNALUBufferSize = 2 * 1024 * 1024
	buf := make([]byte, 0, maxNALUBufferSize)

	tmp := make([]byte, 4096)
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
				return
			}
			if len(buf)+n > maxNALUBufferSize {
				log.Printf("[FFmpeg Video Stream] NALU buffer exceeded max capacity (%d bytes). Exiting streamer.", maxNALUBufferSize)
				return
			}
			buf = append(buf, tmp[:n]...)

			for {
				start := findStartCode(buf)
				if start == -1 {
					break
				}

				next := findStartCode(buf[start+4:])
				if next == -1 {
					break
				}
				next += start + 4

				nalu := buf[start:next]

				if len(nalu) == 0 {
					buf = buf[next:]
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
				buf = buf[next:]
			}
		}
	}
}

func findStartCode(data []byte) int {
	for i := 0; i < len(data)-3; i++ {
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

	switch t {
	case "request_screen_info":
		// ИСПРАВЛЕНО #8: используем локальную переменную dc вместо повторного
		// обращения к activeDataChannel после освобождения мьютекса.
		dcMutex.RLock()
		dc := activeDataChannel
		dcMutex.RUnlock()

		if dc != nil {
			sendScreenInfo(dc) // ИСПРАВЛЕНО #8
		}

	case "request_window_info":
		// ИСПРАВЛЕНО #8: аналогично.
		dcMutex.RLock()
		dc := activeDataChannel
		dcMutex.RUnlock()

		if dc != nil {
			sendWindowInfo(dc) // ИСПРАВЛЕНО #8
		}

	case "mouse_move":
		x, okX := ctl["x"].(float64)
		y, okY := ctl["y"].(float64)
		if !okX || !okY {
			return
		}
		currentDisplayW, currentDisplayH := robotgo.GetScreenSize()

		tw := actualScreenWidth
		th := actualScreenHeight
		if tw == 0 || th == 0 {
			tw, th = currentDisplayW, currentDisplayH
		}

		scaleX := float64(currentDisplayW) / float64(tw)
		scaleY := float64(currentDisplayH) / float64(th)

		safeX := clampInt(int(x*scaleX), 0, currentDisplayW-1)
		safeY := clampInt(int(y*scaleY), 0, currentDisplayH-1)
		robotgo.MoveMouse(safeX, safeY)

	case "mouse_down", "mouse_up":
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

	case "mouse_toggle":
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

	case "mouse_click":
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

	case "key_press":
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

func clampInt(v, min, max int) int {
	if v < min {
		return min
	}
	if v > max {
		return max
	}
	return v
}

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

// ИСПРАВЛЕНО #6: прежняя реализация делила на div уже после его инкремента
// в теле цикла, что давало результат ~0 для любого значения.
// Например: n=1500 → div начинает с 1024, цикл: 1500>=1024 → div=1048576,
// результат: 1500/1048576 ≈ 0.001 KB (неверно).
// Исправлено: цикл итерирует по уменьшающемуся n, div накапливает множитель.
func formatBytes(b int64) string {
	const unit = 1024
	if b < unit {
		return fmt.Sprintf("%d B", b)
	}
	div, exp := int64(unit), 0
	for n := b / unit; n >= unit; n /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %cB", float64(b)/float64(div), "KMGTPE"[exp])
}
