package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"image/png"
	"io"
	"log"
	"os"
	"os/exec"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/go-vgo/robotgo"
	"github.com/gorilla/websocket"
	"github.com/pion/webrtc/v3"
	"github.com/pion/webrtc/v3/pkg/media"
)

const (
	serverURL           = "ws://192.168.2.222:8000/ws/agent/agent1"
	websocketMaxRetries = 5
	websocketRetryDelay = 5 * time.Second
)

func init() {
	if runtime.GOOS == "windows" {
		initWindowsDPI()
	}
}

// === PHASE 3: ADAPTIVE RESOLUTION SYSTEM ===
type ScreenInfo struct {
	Width  int `json:"width"`
	Height int `json:"height"`
}

type QualitySettings struct {
	Bitrate string `json:"bitrate"`
	Maxrate string `json:"maxrate"`
	Bufsize string `json:"bufsize"`
	FPS     int    `json:"fps"`
}

// PHASE 3: Quality levels (bitrate only, resolution = native)
var BITRATE_PRESETS = map[string]QualitySettings{
	"low": {
		Bitrate: "1M", Maxrate: "1.5M", Bufsize: "2M", FPS: 20,
	},
	"medium": {
		Bitrate: "3M", Maxrate: "4M", Bufsize: "6M", FPS: 25,
	},
	"high": {
		Bitrate: "6M", Maxrate: "8M", Bufsize: "12M", FPS: 30,
	},
	"ultra": {
		Bitrate: "12M", Maxrate: "16M", Bufsize: "24M", FPS: 30,
	},
}

// === GLOBAL VARIABLES ===
var (
	user32      = syscall.NewLazyDLL("user32.dll")
	setDPIAware = user32.NewProc("SetProcessDPIAware")

	videoBytesSent  int64
	videoFramesSent int64
	videoStatsLock  sync.Mutex

	ffmpegRestartSignal = make(chan struct{}, 1)
	ffmpegMutex         sync.Mutex
	ffmpegStatsReset    = make(chan struct{}, 1)
	currentFFmpegCmd    *exec.Cmd

	// PHASE 3: Native screen resolution and quality
	nativeScreen   ScreenInfo
	currentQuality = "medium"
	qualityMutex   sync.RWMutex
	screenDetected bool
)

// === CHANNEL MANAGER ===
type ChannelManager struct {
	controlChannel *webrtc.DataChannel
	binaryChannel  *webrtc.DataChannel
	mutex          sync.RWMutex
}

var channelManager = &ChannelManager{}

func initWindowsDPI() {
	setDPIAware.Call()
}

// === PHASE 3: NATIVE SCREEN DETECTION ===
func detectNativeResolution() (ScreenInfo, error) {
	width, height := robotgo.GetScreenSize()
	if width <= 0 || height <= 0 {
		return ScreenInfo{}, fmt.Errorf("invalid screen size: %dx%d", width, height)
	}

	screen := ScreenInfo{
		Width:  width,
		Height: height,
	}

	log.Printf("[SCREEN] Detected native resolution: %dx%d", width, height)
	return screen, nil
}

func initializeNativeScreen() error {
	screen, err := detectNativeResolution()
	if err != nil {
		return fmt.Errorf("failed to detect screen: %v", err)
	}

	nativeScreen = screen
	screenDetected = true

	log.Printf("[PHASE3] Native screen initialized: %dx%d", nativeScreen.Width, nativeScreen.Height)
	return nil
}

// === QUALITY MANAGEMENT ===
func getCurrentQuality() QualitySettings {
	qualityMutex.RLock()
	defer qualityMutex.RUnlock()

	if quality, exists := BITRATE_PRESETS[currentQuality]; exists {
		return quality
	}
	return BITRATE_PRESETS["medium"] // fallback
}

func setCurrentQualityLevel(quality string) {
	qualityMutex.Lock()
	defer qualityMutex.Unlock()

	if _, exists := BITRATE_PRESETS[quality]; exists {
		currentQuality = quality
		log.Printf("[QUALITY] Changed to: %s", quality)
	} else {
		log.Printf("[QUALITY] Unknown quality: %s, keeping: %s", quality, currentQuality)
	}
}

// === COMMUNICATION FUNCTIONS ===
func sendControlMessage(data map[string]interface{}) error {
	channelManager.mutex.RLock()
	dc := channelManager.controlChannel
	channelManager.mutex.RUnlock()

	if dc == nil || dc.ReadyState() != webrtc.DataChannelStateOpen {
		return fmt.Errorf("control channel not available")
	}

	// Add timestamp for latency measurement
	data["timestamp"] = time.Now().UnixMilli()

	jsonData, err := json.Marshal(data)
	if err != nil {
		return fmt.Errorf("json marshal error: %v", err)
	}

	return dc.SendText(string(jsonData))
}

func sendBinaryData(messageType string, payload []byte) error {
	channelManager.mutex.RLock()
	dc := channelManager.binaryChannel
	channelManager.mutex.RUnlock()

	if dc == nil || dc.ReadyState() != webrtc.DataChannelStateOpen {
		return fmt.Errorf("binary channel not available")
	}

	typeBytes := []byte(messageType)
	if len(typeBytes) < 4 {
		typeBytes = append(typeBytes, make([]byte, 4-len(typeBytes))...)
	} else if len(typeBytes) > 4 {
		typeBytes = typeBytes[:4]
	}

	message := append(typeBytes, payload...)
	return dc.Send(message)
}

// === PHASE 3: NATIVE VIDEO INFO ===
func sendNativeVideoInfo() {
	if !screenDetected {
		log.Printf("[VIDEO] Screen not detected yet, skipping video info")
		return
	}

	quality := getCurrentQuality()

	info := map[string]interface{}{
		"type":            "native_video_info",
		"width":           nativeScreen.Width,
		"height":          nativeScreen.Height,
		"quality_level":   currentQuality,
		"bitrate":         quality.Bitrate,
		"fps":             quality.FPS,
		"coordinate_mode": "native_1_to_1",
		"phase":           "3_adaptive",
	}

	if err := sendControlMessage(info); err != nil {
		log.Printf("[ERROR] Failed to send native_video_info: %v", err)
	} else {
		log.Printf("[VIDEO] Sent native video info: %dx%d (%s)", nativeScreen.Width, nativeScreen.Height, currentQuality)
	}
}

// === SCREENSHOT FUNCTIONS ===
func captureScreenshot() ([]byte, error) {
	log.Println("[SCREENSHOT] Capturing screen...")

	bitmap := robotgo.CaptureScreen()
	if bitmap == nil {
		return nil, fmt.Errorf("failed to capture screen")
	}
	defer robotgo.FreeBitmap(bitmap)

	img := robotgo.ToImage(bitmap)
	if img == nil {
		return nil, fmt.Errorf("failed to convert bitmap to image")
	}

	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		return nil, fmt.Errorf("failed to encode PNG: %v", err)
	}

	log.Printf("[SCREENSHOT] Captured: %d bytes (%dx%d)", buf.Len(), nativeScreen.Width, nativeScreen.Height)
	return buf.Bytes(), nil
}

func handleScreenshotRequest(payload []byte) {
	log.Println("[BINARY] Processing screenshot request...")

	screenshot, err := captureScreenshot()
	if err != nil {
		log.Printf("[BINARY] Screenshot error: %v", err)
		errorMsg := fmt.Sprintf("ERROR: %v", err)
		if err := sendBinaryData("SCRN", []byte(errorMsg)); err != nil {
			log.Printf("[BINARY] Failed to send error: %v", err)
		}
		return
	}

	if err := sendBinaryData("SCRN", screenshot); err != nil {
		log.Printf("[BINARY] Failed to send screenshot: %v", err)
	} else {
		log.Printf("[BINARY] Screenshot sent: %d bytes", len(screenshot))
	}
}

// === MAIN FUNCTION ===
func main() {
	log.SetFlags(log.LstdFlags | log.Lshortfile)
	log.Printf("🚀 RMM Agent v3.0 - Phase 3: Adaptive Native Resolution")

	// PHASE 3: Initialize native screen detection
	if err := initializeNativeScreen(); err != nil {
		log.Fatalf("Failed to initialize screen: %v", err)
	}

	// ИСПРАВЛЕНО: Удалена неиспользуемая переменная quality
	log.Printf("Native resolution: %dx%d, Quality: %s",
		nativeScreen.Width, nativeScreen.Height, currentQuality)
	log.Printf("Connecting to: %s", serverURL)

	for i := 0; i < websocketMaxRetries; i++ {
		log.Printf("Connection attempt %d/%d...", i+1, websocketMaxRetries)
		err := runAgent()
		if err == nil {
			log.Println("Agent stopped gracefully")
			break
		}
		log.Printf("Error: %v. Retrying in %v...", err, websocketRetryDelay)
		time.Sleep(websocketRetryDelay)
	}

	log.Printf("Exiting after %d failed attempts", websocketMaxRetries)
	os.Exit(1)
}

func runAgent() error {
	ws, _, err := websocket.DefaultDialer.Dial(serverURL, nil)
	if err != nil {
		return fmt.Errorf("websocket connect error: %w", err)
	}
	defer ws.Close()
	log.Println("[WS] Connected to signaling server")

	writeChan := make(chan []byte, 100)
	go func() {
		for msg := range writeChan {
			err := ws.WriteMessage(websocket.TextMessage, msg)
			if err != nil {
				log.Printf("[WS] Write error: %v", err)
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

	log.Printf("[FFMPEG] Starting native streaming: %dx%d (%s)",
		nativeScreen.Width, nativeScreen.Height, currentQuality)
	go manageFFmpegProcess(videoTrack)
	startVideoStats()

	// Main WebRTC signaling loop
	for {
		_, msg, err := ws.ReadMessage()
		if err != nil {
			if websocket.IsCloseError(err, websocket.CloseNormalClosure, websocket.CloseGoingAway) {
				log.Println("[WS] Closed cleanly")
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

// === MESSAGE HANDLING ===
func handleControlMessage(data []byte) {
	var ctl map[string]interface{}
	if err := json.Unmarshal(data, &ctl); err != nil {
		log.Printf("[CONTROL] Invalid JSON: %v", err)
		return
	}

	msgType, _ := ctl["type"].(string)
	log.Printf("[CONTROL] Received: %s", msgType)

	switch msgType {
	case "request_native_video_info":
		sendNativeVideoInfo()

	case "browser_video_confirmed":
		width, _ := ctl["width"].(float64)
		height, _ := ctl["height"].(float64)
		log.Printf("[CONTROL] Browser confirmed video: %.0fx%.0f", width, height)

		// Verify browser received correct native resolution
		if int(width) != nativeScreen.Width || int(height) != nativeScreen.Height {
			log.Printf("[WARNING] Resolution mismatch! Native: %dx%d, Browser: %.0fx%.0f",
				nativeScreen.Width, nativeScreen.Height, width, height)
		} else {
			log.Printf("[SUCCESS] Browser-Agent resolution synchronized: %dx%d", nativeScreen.Width, nativeScreen.Height)
		}

	// === PHASE 3: QUALITY CHANGE (BITRATE ONLY) ===
	case "change_quality":
		quality, ok := ctl["quality_level"].(string)
		if !ok {
			log.Printf("[CONTROL] Invalid quality change request")
			return
		}

		log.Printf("[QUALITY] Received bitrate change request: %s", quality)

		// Validate quality level
		if _, exists := BITRATE_PRESETS[quality]; !exists {
			log.Printf("[QUALITY] Unknown quality level: %s", quality)
			return
		}

		// Update current quality
		oldQuality := currentQuality
		setCurrentQualityLevel(quality)

		if oldQuality != currentQuality {
			log.Printf("[QUALITY] Quality level changed: %s -> %s", oldQuality, currentQuality)

			// Restart FFmpeg with new bitrate settings
			select {
			case ffmpegRestartSignal <- struct{}{}:
				log.Printf("[QUALITY] FFmpeg restart signal sent for bitrate change")
			default:
				log.Printf("[QUALITY] FFmpeg restart signal already pending")
			}

			// Send confirmation
			newQuality := getCurrentQuality()
			confirmation := map[string]interface{}{
				"type":          "quality_changed",
				"quality_level": currentQuality,
				"bitrate":       newQuality.Bitrate,
				"fps":           newQuality.FPS,
				"width":         nativeScreen.Width,
				"height":        nativeScreen.Height,
			}

			if err := sendControlMessage(confirmation); err != nil {
				log.Printf("[QUALITY] Failed to send confirmation: %v", err)
			}

			// Send updated video info
			go func() {
				time.Sleep(2 * time.Second) // Wait for FFmpeg to restart
				sendNativeVideoInfo()
			}()
		}

	case "ping":
		pong := map[string]interface{}{
			"type": "pong",
		}
		if err := sendControlMessage(pong); err != nil {
			log.Printf("[CONTROL] Failed to send pong: %v", err)
		}

	// === PHASE 3: NATIVE 1:1 COORDINATE MAPPING ===
	case "mouse_move":
		// ИСПРАВЛЕНО: Проверка screenDetected перед использованием nativeScreen
		if !screenDetected {
			log.Printf("[CONTROL] Screen not detected, ignoring mouse_move")
			return
		}

		x, okX := ctl["x"].(float64)
		y, okY := ctl["y"].(float64)
		if !okX || !okY {
			log.Printf("[CONTROL] Invalid mouse_move coordinates")
			return
		}

		// PHASE 3: Direct 1:1 mapping (video coordinates = screen coordinates)
		safeX := clampInt(int(x), 0, nativeScreen.Width-1)
		safeY := clampInt(int(y), 0, nativeScreen.Height-1)
		robotgo.MoveMouse(safeX, safeY)

	case "mouse_down", "mouse_up":
		btnF, ok := ctl["button"].(float64)
		if !ok {
			log.Printf("[CONTROL] Invalid mouse button")
			return
		}
		btn := int(btnF)
		names := []string{"left", "middle", "right"}
		if btn < 0 || btn >= len(names) {
			log.Printf("[CONTROL] Unknown mouse button: %d", btn)
			return
		}
		if msgType == "mouse_down" {
			robotgo.MouseDown(names[btn])
		} else {
			robotgo.MouseUp(names[btn])
		}

	case "key_down":
		keyStr, ok := ctl["key"].(string)
		if !ok {
			log.Printf("[CONTROL] Missing key field")
			return
		}
		robotgo.KeyDown(keyStr)

	case "key_up":
		keyStr, ok := ctl["key"].(string)
		if !ok {
			log.Printf("[CONTROL] Missing key field")
			return
		}
		robotgo.KeyUp(keyStr)

	default:
		log.Printf("[CONTROL] Unhandled event: %s", msgType)
	}
}

func handleBinaryMessage(data []byte) {
	if len(data) < 4 {
		log.Printf("[BINARY] Message too short: %d bytes", len(data))
		return
	}

	msgType := string(data[:4])
	payload := data[4:]

	log.Printf("[BINARY] Received: '%s', %d bytes", msgType, len(payload))

	switch strings.TrimSpace(msgType) {
	case "SCRN":
		handleScreenshotRequest(payload)
	default:
		log.Printf("[BINARY] Unknown message type: '%s'", msgType)
	}
}

// === PHASE 3: NATIVE FFMPEG CONFIGURATION ===
func getNativeFFmpegArgs() []string {
	quality := getCurrentQuality()

	var args []string
	if runtime.GOOS == "windows" {
		args = []string{"-f", "gdigrab", "-framerate", strconv.Itoa(quality.FPS), "-draw_mouse", "1", "-i", "desktop"}
	} else {
		args = []string{"-f", "x11grab", "-framerate", strconv.Itoa(quality.FPS), "-draw_mouse", "1", "-i", ":0.0"}
	}

	// PHASE 3: NO forced resolution - stream native screen size
	// FFmpeg will automatically use the native desktop resolution

	args = append(args,
		"-vcodec", "libx264", "-preset", "ultrafast", "-tune", "zerolatency",
		"-pix_fmt", "yuv420p", "-g", "60", "-keyint_min", "30",
		// PHASE 3: Only bitrate settings change, resolution is native
		"-b:v", quality.Bitrate, "-maxrate", quality.Maxrate, "-bufsize", quality.Bufsize,
		"-fflags", "nobuffer", "-f", "h264", "-",
	)

	return args
}

func manageFFmpegProcess(videoTrack *webrtc.TrackLocalStaticSample) {
	for {
		quality := getCurrentQuality()
		log.Printf("[FFMPEG] Starting native process: %dx%d, quality: %s (bitrate: %s)",
			nativeScreen.Width, nativeScreen.Height, currentQuality, quality.Bitrate)

		quitSignal := make(chan struct{})

		ffmpegMutex.Lock()
		if currentFFmpegCmd != nil && currentFFmpegCmd.Process != nil {
			log.Println("[FFMPEG] Terminating previous process")
			_ = currentFFmpegCmd.Process.Kill()
		}
		ffmpegMutex.Unlock()

		args := getNativeFFmpegArgs()
		log.Printf("[FFMPEG] Native command: ffmpeg %s", strings.Join(args, " "))

		cmd := exec.Command("ffmpeg", args...)

		ffmpegMutex.Lock()
		currentFFmpegCmd = cmd
		ffmpegMutex.Unlock()

		stdout, err := cmd.StdoutPipe()
		if err != nil {
			log.Printf("[FFMPEG] Stdout pipe error: %v", err)
			time.Sleep(5 * time.Second)
			close(quitSignal)
			continue
		}

		stderr, err := cmd.StderrPipe()
		if err != nil {
			log.Printf("[FFMPEG] Stderr pipe error: %v", err)
			_ = stdout.Close()
			time.Sleep(5 * time.Second)
			close(quitSignal)
			continue
		}
		go func() { io.Copy(io.Discard, stderr) }()

		if err = cmd.Start(); err != nil {
			log.Printf("[FFMPEG] Start error: %v", err)
			_ = stdout.Close()
			_ = stderr.Close()
			time.Sleep(5 * time.Second)
			close(quitSignal)
			continue
		}

		log.Printf("[FFMPEG] Native process started: %dx%d @ %s", nativeScreen.Width, nativeScreen.Height, quality.Bitrate)

		var wg sync.WaitGroup
		wg.Add(1)

		go func() {
			defer wg.Done()
			streamVideo(stdout, videoTrack, quitSignal)
		}()

		ffmpegDone := make(chan struct{})
		go func() {
			defer close(ffmpegDone)
			err := cmd.Wait()
			if err != nil {
				log.Printf("[FFMPEG] Process exited with error: %v", err)
			} else {
				log.Println("[FFMPEG] Process exited normally")
			}
			close(quitSignal)
		}()

		select {
		case <-ffmpegRestartSignal:
			log.Println("[FFMPEG] Restart signal received for bitrate change")
			ffmpegMutex.Lock()
			if cmd != nil && cmd.Process != nil {
				if runtime.GOOS == "windows" {
					_ = cmd.Process.Kill()
				} else {
					if err := cmd.Process.Signal(syscall.SIGTERM); err != nil {
						_ = cmd.Process.Kill()
					}
				}
			}
			ffmpegMutex.Unlock()
			select {
			case ffmpegStatsReset <- struct{}{}:
			default:
			}
		case <-ffmpegDone:
			log.Println("[FFMPEG] Native process lifecycle completed")
		}

		wg.Wait()
		<-ffmpegDone
		time.Sleep(1 * time.Second)
	}
}

func streamVideo(r io.Reader, videoTrack *webrtc.TrackLocalStaticSample, quit <-chan struct{}) {
	reader := bufio.NewReader(r)
	const maxNALUBufferSize = 2 * 1024 * 1024
	buf := make([]byte, 0, maxNALUBufferSize)
	tmp := make([]byte, 4096)

	// ИСПРАВЛЕНО: Используем качество напрямую в коде, где это необходимо
	for {
		select {
		case <-quit:
			log.Println("[FFMPEG] Native video streaming stopped")
			return
		default:
			n, err := reader.Read(tmp)
			if err != nil {
				if !errors.Is(err, io.EOF) {
					log.Printf("[FFMPEG] Read error: %v", err)
				}
				return
			}

			if len(buf)+n > maxNALUBufferSize {
				log.Printf("[FFMPEG] Buffer overflow, resetting")
				buf = buf[:0]
				continue
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
					return
				default:
					// ИСПРАВЛЕНО: Получаем качество только когда используем
					quality := getCurrentQuality()
					_ = videoTrack.WriteSample(media.Sample{
						Data:     nalu,
						Duration: time.Second / time.Duration(quality.FPS),
					})

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

// === WebRTC SETUP ===
func newPeerConnection(out chan []byte, videoTrack *webrtc.TrackLocalStaticSample) (*webrtc.PeerConnection, error) {
	pc, err := webrtc.NewPeerConnection(webrtc.Configuration{
		ICEServers: []webrtc.ICEServer{},
	})
	if err != nil {
		return nil, err
	}

	if _, err := pc.AddTrack(videoTrack); err != nil {
		log.Printf("[WebRTC] AddTrack error: %v", err)
	}

	pc.OnDataChannel(func(dc *webrtc.DataChannel) {
		channelManager.mutex.Lock()
		defer channelManager.mutex.Unlock()

		switch dc.Label() {
		case "control":
			channelManager.controlChannel = dc
			setupControlChannel(dc)
			log.Println("[DATACHANNEL] Control channel established")

		case "binary":
			channelManager.binaryChannel = dc
			setupBinaryChannel(dc)
			log.Println("[DATACHANNEL] Binary channel established")

		default:
			log.Printf("[DATACHANNEL] Unknown channel: %s", dc.Label())
		}
	})

	pc.OnICECandidate(func(c *webrtc.ICECandidate) {
		if c != nil {
			if payload, err := json.Marshal(c.ToJSON()); err == nil {
				out <- payload
			}
		}
	})

	pc.OnConnectionStateChange(func(s webrtc.PeerConnectionState) {
		log.Printf("[WebRTC] Connection state: %s", s.String())
		if s == webrtc.PeerConnectionStateFailed || s == webrtc.PeerConnectionStateClosed {
			log.Printf("[WebRTC] Connection %s, detaching channels", s.String())
			channelManager.mutex.Lock()
			channelManager.controlChannel = nil
			channelManager.binaryChannel = nil
			channelManager.mutex.Unlock()
		}
	})

	return pc, nil
}

// === DATACHANNEL SETUP ===
func setupControlChannel(dc *webrtc.DataChannel) {
	dc.OnOpen(func() {
		log.Println("[CONTROL] DataChannel opened")
		sendNativeVideoInfo() // Send native screen dimensions
	})

	dc.OnMessage(func(msg webrtc.DataChannelMessage) {
		handleControlMessage(msg.Data)
	})

	dc.OnClose(func() {
		log.Println("[CONTROL] DataChannel closed")
		channelManager.mutex.Lock()
		channelManager.controlChannel = nil
		channelManager.mutex.Unlock()
	})

	dc.OnError(func(err error) {
		log.Printf("[CONTROL] DataChannel error: %v", err)
	})
}

func setupBinaryChannel(dc *webrtc.DataChannel) {
	dc.OnOpen(func() {
		log.Println("[BINARY] DataChannel opened")
	})

	dc.OnMessage(func(msg webrtc.DataChannelMessage) {
		handleBinaryMessage(msg.Data)
	})

	dc.OnClose(func() {
		log.Println("[BINARY] DataChannel closed")
		channelManager.mutex.Lock()
		channelManager.binaryChannel = nil
		channelManager.mutex.Unlock()
	})

	dc.OnError(func(err error) {
		log.Printf("[BINARY] DataChannel error: %v", err)
	})
}

// === SDP/ICE HANDLING ===
func handleSDP(msg []byte, out chan []byte, pcs map[string]*webrtc.PeerConnection,
	lock *sync.Mutex, videoTrack *webrtc.TrackLocalStaticSample) bool {

	var sdp webrtc.SessionDescription
	if err := json.Unmarshal(msg, &sdp); err != nil || sdp.Type != webrtc.SDPTypeOffer {
		return false
	}

	lock.Lock()
	if old, ok := pcs["viewer"]; ok {
		log.Println("[WebRTC] Closing old PeerConnection")
		_ = old.Close()
	}
	pc, err := newPeerConnection(out, videoTrack)
	if err != nil {
		lock.Unlock()
		log.Printf("[WebRTC] PeerConnection error: %v", err)
		return true
	}
	pcs["viewer"] = pc
	lock.Unlock()

	// ИСПРАВЛЕНО: Проверяем ошибки SetRemoteDescription и SetLocalDescription
	if err := pc.SetRemoteDescription(sdp); err != nil {
		log.Printf("[WebRTC] SetRemoteDescription error: %v", err)
		return true
	}

	answer, err := pc.CreateAnswer(nil)
	if err != nil {
		log.Printf("[WebRTC] CreateAnswer error: %v", err)
		return true
	}

	if err := pc.SetLocalDescription(answer); err != nil {
		log.Printf("[WebRTC] SetLocalDescription error: %v", err)
		return true
	}

	payload, _ := json.Marshal(answer)
	out <- payload

	return true
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
				log.Printf("[WebRTC] AddICECandidate error: %v", err)
			}
		}
	}
}

// === UTILITY FUNCTIONS ===
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
		ticker := time.NewTicker(5 * time.Second)
		defer ticker.Stop()

		for {
			select {
			case <-ffmpegStatsReset:
				videoStatsLock.Lock()
				prevBytes = videoBytesSent
				prevFrames = videoFramesSent
				videoStatsLock.Unlock()
				log.Println("[STATS] Counters reset for new quality")
			case <-ticker.C:
				videoStatsLock.Lock()
				bytesDelta := videoBytesSent - prevBytes
				framesDelta := videoFramesSent - prevFrames
				prevBytes = videoBytesSent
				prevFrames = videoFramesSent
				videoStatsLock.Unlock()

				fps := float64(framesDelta) / 5.0
				mbps := float64(bytesDelta) * 8 / 1_000_000 / 5.0

				quality := getCurrentQuality()
				log.Printf("[STATS] Native: %dx%d | Quality: %s | FPS: %.1f | Mbps: %.2f | Target: %s@%dfps | Frames: %d | Total: %s",
					nativeScreen.Width, nativeScreen.Height, currentQuality, fps, mbps,
					quality.Bitrate, quality.FPS, framesDelta, formatBytes(videoBytesSent))
			}
		}
	}()
}

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
