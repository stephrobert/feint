//go:build linux

package cli

import (
	"os"
	"syscall"
	"unsafe"
)

// isTerminal asks the kernel whether f is a terminal. A character-device test
// is not enough: /dev/null is one, and `feint doctor --fix </dev/null` then
// prompted and read an empty answer, measured on 2026-11-16 while writing #974.
// TestFixRunsNothingWithoutATerminal drives the non-terminal branch.
func isTerminal(f *os.File) bool {
	var termios syscall.Termios
	_, _, errno := syscall.Syscall(syscall.SYS_IOCTL, f.Fd(), uintptr(syscall.TCGETS), uintptr(unsafe.Pointer(&termios))) //nolint:gosec // the documented way to ask without a dependency
	return errno == 0
}
