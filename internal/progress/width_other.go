// Other platforms use COLUMNS or a conservative width; Windows uses plain output.
//go:build !(linux || darwin || freebsd || openbsd || netbsd || dragonfly)

package progress

import "os"

func fileWidth(_ *os.File) int { return 0 }
