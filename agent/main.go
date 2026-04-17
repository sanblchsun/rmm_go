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

// === PHASE 1: SIMPLIFIED CONFIGURATION ===
const (
	serverURL           = "ws://192.168.2.222:8000/ws/agent/agent1"
	websocketMaxRetries = 5
	websocketRetryDelay = 5 * time.Second

	// PHASE 1: Fixed resolution for stability
	FIXED_WIDTH     = 1920
	FIXED_HEIGHT    = 1080
	FIXED_FRAMERATE = 30
)

func init() {
	if runtime.GOOS == "windows" {
		initWindowsDPI()
	}
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

// === COMMUNICATION FUNCTIONS ===
func sendControlMessage(data map[string]interface{}) error {
	channelManager.mutex.RLock()
	dc := channelManager.controlChannel
	channelManager.mutex.RUnlock()

	if dc == nil || dc.ReadyState() != webrtc.DataChannelStateOpen {
		return fmt.Errorf("control channel not available")
	}

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

// === PHASE 1: SIMPLIFIED VIDEO INFO ===
func sendVideoInfo() {
	info := map[string]interface{}{
		"type":   "video_info",
		"width":  FIXED_WIDTH,
		"height": FIXED_HEIGHT,
	}

	if err := sendControlMessage(info); err != nil {
		log.Printf("[ERROR] Failed to send video_info: %v", err)
	} else {
		log.Printf("[VIDEO] Sent video_info: %dx%d", FIXED_WIDTH, FIXED_HEIGHT)
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

	log.Printf("[SCREENSHOT] Captured: %d bytes", buf.Len())
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
	log.Printf("🚀 RMM Agent v2.0 - Phase 1: Simplified Coordinates")
	log.Printf("Fixed resolution: %dx%d", FIXED_WIDTH, FIXED_HEIGHT)
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

	log.Printf("[FFMPEG] Starting with fixed resolution: %dx%d", FIXED_WIDTH, FIXED_HEIGHT)
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
		sendVideoInfo() // Send fixed video dimensions
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
	case "request_video_info":
		sendVideoInfo()

	case "video_dimensions_confirmed":
		width, _ := ctl["width"].(float64)
		height, _ := ctl["height"].(float64)
		log.Printf("[CONTROL] Browser confirmed dimensions: %.0fx%.0f", width, height)

	case "ping":
		// Send pong response
		pong := map[string]interface{}{
			"type":      "pong",
			"timestamp": time.Now().Unix(),
		}
		if err := sendControlMessage(pong); err != nil {
			log.Printf("[CONTROL] Failed to send pong: %v", err)
		}

	case "mouse_move":
		x, okX := ctl["x"].(float64)
		y, okY := ctl["y"].(float64)
		if !okX || !okY {
			log.Printf("[CONTROL] Invalid mouse_move coordinates")
			return
		}

		// PHASE 1: Direct 1:1 coordinate mapping
		safeX := clampInt(int(x), 0, FIXED_WIDTH-1)
		safeY := clampInt(int(y), 0, FIXED_HEIGHT-1)
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

// === FFMPEG MANAGEMENT ===
func getFFmpegArgs() []string {
	var args []string
	if runtime.GOOS == "windows" {
		args = []string{"-f", "gdigrab", "-framerate", strconv.Itoa(FIXED_FRAMERATE), "-draw_mouse", "1", "-i", "desktop"}
	} else {
		args = []string{"-f", "x11grab", "-framerate", strconv.Itoa(FIXED_FRAMERATE), "-draw_mouse", "1", "-i", ":0.0"}
	}

	// PHASE 1: Fixed resolution
	args = append(args, "-s", fmt.Sprintf("%dx%d", FIXED_WIDTH, FIXED_HEIGHT))

	args = append(args,
		"-vcodec", "libx264", "-preset", "ultrafast", "-tune", "zerolatency",
		"-pix_fmt", "yuv420p", "-g", "60", "-keyint_min", "30",
		"-b:v", "4M", "-maxrate", "6M", "-bufsize", "8M",
		"-fflags", "nobuffer", "-f", "h264", "-",
	)

	return args
}

func manageFFmpegProcess(videoTrack *webrtc.TrackLocalStaticSample) {
	for {
		log.Println("[FFMPEG] Starting new process...")

		quitSignal := make(chan struct{})

		ffmpegMutex.Lock()
		if currentFFmpegCmd != nil && currentFFmpegCmd.Process != nil {
			log.Println("[FFMPEG] Terminating previous process")
			_ = currentFFmpegCmd.Process.Kill()
		}
		ffmpegMutex.Unlock()

		args := getFFmpegArgs()
		log.Printf("[FFMPEG] Command: ffmpeg %s", strings.Join(args, " "))

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

		log.Println("[FFMPEG] Process started successfully")

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
			log.Println("[FFMPEG] Restart signal received")
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
			log.Println("[FFMPEG] Process lifecycle completed")
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

	for {
		select {
		case <-quit:
			log.Println("[FFMPEG] Video streaming stopped")
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
					_ = videoTrack.WriteSample(media.Sample{
						Data:     nalu,
						Duration: time.Second / time.Duration(FIXED_FRAMERATE),
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

	_ = pc.SetRemoteDescription(sdp)

	answer, err := pc.CreateAnswer(nil)
	if err != nil {
		log.Printf("[WebRTC] CreateAnswer error: %v", err)
		return true
	}
	_ = pc.SetLocalDescription(answer)
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
				log.Println("[STATS] Counters reset")
			case <-ticker.C:
				videoStatsLock.Lock()
				bytesDelta := videoBytesSent - prevBytes
				framesDelta := videoFramesSent - prevFrames
				prevBytes = videoBytesSent
				prevFrames = videoFramesSent
				videoStatsLock.Unlock()

				fps := float64(framesDelta) / 5.0
				mbps := float64(bytesDelta) * 8 / 1_000_000 / 5.0

				log.Printf("[STATS] FPS: %.1f | Mbps: %.2f | Frames: %d | Total: %s",
					fps, mbps, framesDelta, formatBytes(videoBytesSent))
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
