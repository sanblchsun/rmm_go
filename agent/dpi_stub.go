//go:build !windows
// builder/agent/dpi_stub.go
package main

// Заглушки для других ОС
func initWindowsDPI()                   {}
func getPhysicalScreenSize() (int, int) { return 0, 0 }
