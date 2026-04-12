//go:build windows

// builder/agent/dpi_windows.go
package main

import (
	"log"
	"syscall"
	"unsafe"
)

var (
	user32              = syscall.NewLazyDLL("user32.dll")
	setDPIAware         = user32.NewProc("SetProcessDPIAware")
	getDpiForWindow     = user32.NewProc("GetDpiForWindow")
	getDesktopWindow    = user32.NewProc("GetDesktopWindow")
	getSystemMetricsFor = user32.NewProc("GetSystemMetricsForDpi")
	// Добавлены для получения информации о мониторе
	monitorFromWindow = user32.NewProc("MonitorFromWindow")
	getMonitorInfo    = user32.NewProc("GetMonitorInfoW")
)

const (
	MONITOR_DEFAULTTOPRIMARY = 1
	MONITORINFOF_PRIMARY     = 0x1
)

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

func initWindowsDPI() {
	setDPIAware.Call()
}

// getPhysicalScreenSize получает разрешение экрана игнорируя системное масштабирование
// для текущего основного монитора.
func getPhysicalScreenSize() (int, int) {
	// Получаем десктопное окно
	hwnd, _, _ := getDesktopWindow.Call()
	if hwnd == 0 {
		log.Println("[DPI] GetDesktopWindow failed, falling back to GetScreenSize.")
		return 0, 0
	}

	// Получаем хэндл монитора для десктопного окна
	hMonitor, _, _ := monitorFromWindow.Call(hwnd, MONITOR_DEFAULTTOPRIMARY)
	if hMonitor == 0 {
		log.Println("[DPI] MonitorFromWindow failed, falling back to GetScreenSize.")
		return 0, 0
	}

	var mi MONITORINFOEX
	mi.CbSize = uint32(unsafe.Sizeof(mi))

	// Заполняем структуру информацией о мониторе
	ret, _, _ := getMonitorInfo.Call(hMonitor, uintptr(unsafe.Pointer(&mi)))
	if ret == 0 {
		log.Println("[DPI] GetMonitorInfoW failed, falling back to GetScreenSize.")
		return 0, 0
	}

	// RcMonitor содержит физические размеры монитора (без учета масштабирования DPI)
	width := int(mi.RcMonitor.Right - mi.RcMonitor.Left)
	height := int(mi.RcMonitor.Bottom - mi.RcMonitor.Top)

	if width == 0 || height == 0 {
		log.Printf("[DPI] Detected 0 size from MONITORINFOEX, falling back to GetScreenSize.")
		return 0, 0
	}

	log.Printf("[DPI] Physical screen size from MONITORINFOEX: %dx%d", width, height)
	return width, height
}
