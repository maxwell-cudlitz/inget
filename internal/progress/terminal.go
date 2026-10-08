// Terminal selection is explicit and conservative: redirected stderr keeps ordinary
// logs, dumb terminals use plain output, and Windows consoles default to plain output.
package progress

import (
	"fmt"
	"io"
	"os"
	"runtime"
	"strconv"

	"github.com/mattn/go-isatty"
)

func resolveMode(out io.Writer, mode string) (string, error) {
	switch mode {
	case "off", "plain":
		return mode, nil
	case "auto":
		file, ok := out.(*os.File)
		if !ok || !isatty.IsTerminal(file.Fd()) {
			return "off", nil
		}
		if os.Getenv("TERM") == "dumb" || runtime.GOOS == "windows" {
			return "plain", nil
		}
		return "terminal", nil
	default:
		return "", fmt.Errorf("invalid --progress %q: want auto, plain or off", mode)
	}
}

func terminalWidth(out io.Writer) int {
	if file, ok := out.(*os.File); ok {
		if width := fileWidth(file); width > 0 {
			return width
		}
	}
	if width, err := strconv.Atoi(os.Getenv("COLUMNS")); err == nil && width > 0 {
		return width
	}
	return 80
}
