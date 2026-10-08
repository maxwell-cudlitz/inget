// Terminal dimensions are read without subprocesses; failures use the caller's fallback.
//go:build linux || darwin || freebsd || openbsd || netbsd || dragonfly

package progress

import (
	"os"

	"golang.org/x/sys/unix"
)

func fileWidth(file *os.File) int {
	if size, err := unix.IoctlGetWinsize(int(file.Fd()), unix.TIOCGWINSZ); err == nil {
		return int(size.Col)
	}
	return 0
}
