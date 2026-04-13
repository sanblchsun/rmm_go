//go:build windows

// builder/agent/dpi_windows.go
package main

import (
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
	// Функции для работы с окнами
	getForegroundWindow = user32.NewProc("GetForegroundWindow")
	getWindowRect       = user32.NewProc("GetWindowRect")
	getWindowText       = user32.NewProc("GetWindowTextW")
	getWindowTextLength = user32.NewProc("GetWindowTextLengthW")
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
		return 0, 0
	}

	// Получаем хэндл монитора для десктопного окна
	hMonitor, _, _ := monitorFromWindow.Call(hwnd, MONITOR_DEFAULTTOPRIMARY)
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

type WindowInfo struct {
	Title   string
	X       int
	Y       int
	Width   int
	Height  int
	IsValid bool
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
