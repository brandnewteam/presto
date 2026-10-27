//go:build windows

package cli

import (
	"os"
	"syscall"
)

var setConsoleMode = syscall.NewLazyDLL("kernel32.dll").NewProc("SetConsoleMode")

// enableVirtualTerminal turns on ANSI escape handling, which older consoles leave off.
func enableVirtualTerminal(file *os.File) bool {
	const enableVirtualTerminalProcessing = 0x0004
	handle := syscall.Handle(file.Fd())
	var mode uint32
	if err := syscall.GetConsoleMode(handle, &mode); err != nil {
		return false
	}
	if mode&enableVirtualTerminalProcessing != 0 {
		return true
	}
	ok, _, _ := setConsoleMode.Call(uintptr(handle), uintptr(mode|enableVirtualTerminalProcessing))
	return ok != 0
}
