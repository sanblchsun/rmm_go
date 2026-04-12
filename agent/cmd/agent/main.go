package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/kardianos/service"
	"github.com/shirou/gopsutil/v4/disk"
	"github.com/shirou/gopsutil/v4/mem"
)

var (
	ServerURL string
	BuildSlug string
)

var httpClient = &http.Client{
	Timeout: 15 * time.Second,
}

// ==================== HELPERS ====================

func setupFileLogger() {
	exeDir := filepath.Dir(getExePath())
	logPath := filepath.Join(exeDir, "agent.log")

	f, err := os.OpenFile(logPath, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0644)
	if err != nil {
		return
	}

	log.SetOutput(f)
	log.SetFlags(log.LstdFlags | log.Lshortfile)
}

func getExePath() string {
	exe, err := os.Executable()
	if err != nil {
		log.Fatal(err)
	}
	abs, _ := filepath.Abs(exe)
	return abs
}

func loadOrCreateMachineUID() string {
	exeDir := filepath.Dir(getExePath())
	path := filepath.Join(exeDir, "machine_uid")

	if b, err := os.ReadFile(path); err == nil {
		return strings.TrimSpace(string(b))
	}

	uid := fmt.Sprintf("%d-%d", time.Now().UnixNano(), os.Getpid())
	_ = os.WriteFile(path, []byte(uid), 0644)
	return uid
}

func getLocalIP() string {
	ifaces, _ := net.Interfaces()
	for _, iface := range ifaces {
		if iface.Flags&net.FlagUp == 0 || iface.Flags&net.FlagLoopback != 0 {
			continue
		}
		addrs, _ := iface.Addrs()
		for _, addr := range addrs {
			if ipnet, ok := addr.(*net.IPNet); ok && ipnet.IP.To4() != nil {
				if ipnet.IP.IsPrivate() {
					return ipnet.IP.String()
				}
			}
		}
	}
	return ""
}

func getExternalIP() string {
	resp, err := httpClient.Get("https://api.ipify.org")
	if err != nil {
		log.Println("External IP error:", err)
		return ""
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return string(b)
}

// ==================== TELEMETRY ====================

func collectTelemetry() map[string]interface{} {
	data := map[string]interface{}{
		"system":           runtime.GOOS,
		"user_name":        getUsersAsString(),
		"ip_addr":          getLocalIP(),
		"external_ip":      getExternalIP(),
		"disks":            []map[string]interface{}{},
		"total_memory":     0,
		"available_memory": 0,
	}

	if vm, err := mem.VirtualMemory(); err == nil {
		data["total_memory"] = int(vm.Total / (1024 * 1024))
		data["available_memory"] = int(vm.Available / (1024 * 1024))
	}

	if parts, err := disk.Partitions(true); err == nil {
		var disksData []map[string]interface{}
		for _, p := range parts {
			if u, err := disk.Usage(p.Mountpoint); err == nil {
				disksData = append(disksData, map[string]interface{}{
					"name": p.Mountpoint,
					"size": int(u.Total / (1024 * 1024 * 1024)),
					"free": int(u.Free / (1024 * 1024 * 1024)),
				})
			}
		}
		data["disks"] = disksData
	}

	return data
}

func getUsersAsString() string {
	psCommand := `$OutputEncoding = [console]::InputEncoding = [console]::OutputEncoding = New-Object System.Text.UTF8Encoding; `
	psCommand += `Get-LocalUser | Where-Object { $_.Enabled -eq $true } | ForEach-Object { $_.Name }`

	cmd := exec.Command("powershell", "-Command", psCommand)
	output, err := cmd.Output()
	if err != nil {
		return ""
	}

	var users []string
	lines := strings.Split(strings.TrimSpace(string(output)), "\n")
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasSuffix(line, "$") {
			continue
		}
		if !isSystemAccount(line) {
			users = append(users, line)
		}
	}
	return strings.Join(users, ", ")
}

func isSystemAccount(username string) bool {
	lower := strings.ToLower(username)
	systemAccounts := []string{"administrator", "guest", "defaultaccount", "wdagutilityaccount", "system", "network service", "local service"}
	for _, acc := range systemAccounts {
		if strings.Contains(lower, acc) {
			return true
		}
	}
	return false
}

// ==================== HTTP HELPERS ====================

func postJSON(url string, payload interface{}) ([]byte, int, error) {
	b, _ := json.Marshal(payload)
	req, _ := http.NewRequest("POST", url, bytes.NewBuffer(b))
	req.Header.Set("Content-Type", "application/json")

	resp, err := httpClient.Do(req)
	if err != nil {
		return nil, 0, err
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)
	return body, resp.StatusCode, nil
}

// ==================== SHA256 ====================

func sha256File(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()

	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// ==================== UPDATE ====================

type UpdateResponse struct {
	Update bool   `json:"update"`
	Build  string `json:"build"`
	URL    string `json:"url"`
	Sha256 string `json:"sha256"`
	Force  bool   `json:"force"`
}

func checkForUpdate(uuid, token string) {
	url := fmt.Sprintf("%s/api/agent/check-update?uuid=%s&token=%s", ServerURL, uuid, token)
	payload := map[string]interface{}{"build": BuildSlug}
	body, status, err := postJSON(url, payload)
	if err != nil || status != 200 {
		log.Println("Update check failed:", err, string(body))
		return
	}

	var updateResp UpdateResponse
	if err := json.Unmarshal(body, &updateResp); err != nil {
		log.Println("Invalid update response:", err)
		return
	}

	if !updateResp.Update {
		return
	}

	log.Println("New version available:", updateResp.Build)
	exePath := getExePath()
	tmpPath := exePath + ".new"

	resp, err := httpClient.Get(updateResp.URL)
	if err != nil || resp.StatusCode != 200 {
		log.Println("Download failed:", err, resp.Status)
		return
	}
	defer resp.Body.Close()

	out, err := os.Create(tmpPath)
	if err != nil {
		log.Println("Cannot create tmp file:", err)
		return
	}
	_, err = io.Copy(out, resp.Body)
	out.Close()
	if err != nil {
		log.Println("Download copy failed:", err)
		os.Remove(tmpPath)
		return
	}

	hash, err := sha256File(tmpPath)
	if err != nil || hash != updateResp.Sha256 {
		log.Println("SHA256 mismatch! Aborting update.")
		os.Remove(tmpPath)
		return
	}

	if err := os.Rename(exePath, exePath+".old"); err != nil {
		log.Println("Rename old failed:", err)
		return
	}
	if err := os.Rename(tmpPath, exePath); err != nil {
		log.Println("Rename new failed:", err)
		os.Rename(exePath+".old", exePath)
		return
	}

	postJSON(fmt.Sprintf("%s/api/agent/telemetry?uuid=%s&token=%s", ServerURL, uuid, token),
		map[string]interface{}{"exe_version": updateResp.Build})

	exec.Command(exePath).Start()
	os.Exit(0)
}

// ==================== MAIN LOGIC ====================

func mainLogic() {
	log.Println("Agent started", BuildSlug)
	machineUID := loadOrCreateMachineUID()
	hostname, _ := os.Hostname()

	var uuid, token string

	// Регистрация без company_id
	for {
		resp, code, err := postJSON(ServerURL+"/api/agent/register", map[string]interface{}{
			"name_pc":     hostname,
			"machine_uid": machineUID,
			"exe_version": BuildSlug,
			"external_ip": getExternalIP(),
		})
		if err == nil && code == 200 {
			var r struct {
				AgentUUID string `json:"agent_uuid"`
				Token     string `json:"token"`
			}
			json.Unmarshal(resp, &r)
			uuid, token = r.AgentUUID, r.Token
			break
		}
		time.Sleep(10 * time.Second)
	}

	telemetry := collectTelemetry()
	telemetry["exe_version"] = BuildSlug
	postJSON(fmt.Sprintf("%s/api/agent/telemetry?uuid=%s&token=%s", ServerURL, uuid, token), telemetry)

	ticker := time.NewTicker(10 * time.Second)
	updateTicker := time.NewTicker(60 * time.Second)

	for {
		select {
		case <-ticker.C:
			httpClient.Post(fmt.Sprintf("%s/api/agent/heartbeat?uuid=%s&token=%s", ServerURL, uuid, token), "application/json", nil)
		case <-updateTicker.C:
			checkForUpdate(uuid, token)
		}
	}
}

// ==================== SERVICE ====================

type Program struct{}

func (p *Program) Start(s service.Service) error { go mainLogic(); return nil }
func (p *Program) Stop(s service.Service) error  { return nil }

func main() {
	setupFileLogger()
	exe := getExePath()
	cfg := &service.Config{
		Name:        "SystemMonitoringAgent",
		DisplayName: "System Monitoring Agent",
		Executable:  exe,
	}

	prg := &Program{}
	s, _ := service.New(prg, cfg)

	if service.Interactive() {
		s.Install()
		s.Start()
		return
	}

	s.Run()
}
