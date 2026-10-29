package server

import (
	"syscall"
	"unsafe"
)

// Windows wall/monotonic time can be coarser than these local HTTP reads.
func dashboardPerfMilliseconds() float64 {
	kernel := syscall.NewLazyDLL("kernel32.dll")
	var count, frequency int64
	if ok, _, _ := kernel.NewProc("QueryPerformanceCounter").Call(uintptr(unsafe.Pointer(&count))); ok == 0 {
		panic("QueryPerformanceCounter")
	}
	if ok, _, _ := kernel.NewProc("QueryPerformanceFrequency").Call(uintptr(unsafe.Pointer(&frequency))); ok == 0 || frequency <= 0 {
		panic("QueryPerformanceFrequency")
	}
	return float64(count) * 1000 / float64(frequency)
}
