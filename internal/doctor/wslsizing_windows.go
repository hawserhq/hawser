//go:build windows

package doctor

import (
	"unsafe"

	"golang.org/x/sys/windows"
)

var procGlobalMemoryStatusEx = windows.NewLazySystemDLL("kernel32.dll").NewProc("GlobalMemoryStatusEx")

type memoryStatusEx struct {
	Length               uint32
	MemoryLoad           uint32
	TotalPhys            uint64
	AvailPhys            uint64
	TotalPageFile        uint64
	AvailPageFile        uint64
	TotalVirtual         uint64
	AvailVirtual         uint64
	AvailExtendedVirtual uint64
}

// hostRAM returns the machine's physical memory, which is what WSL's 50%
// default is half of. Zero when it cannot be read, which the check treats as
// "do not judge".
func hostRAM() uint64 {
	var m memoryStatusEx
	m.Length = uint32(unsafe.Sizeof(m))
	r, _, _ := procGlobalMemoryStatusEx.Call(uintptr(unsafe.Pointer(&m)))
	if r == 0 {
		return 0
	}
	return m.TotalPhys
}
