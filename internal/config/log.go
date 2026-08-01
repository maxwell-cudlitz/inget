// The log block, read on its own.
//
// Logging must be configured before anything else, including for commands that need no
// configuration at all — `inget version` has to work in a directory with no config.yaml.
// LoadLog therefore reads only the `log` block and treats a missing base file as empty
// rather than as a failure. Malformed YAML is still an error: a file that exists and
// cannot be parsed is a problem the operator wants to hear about immediately.
//
// The LOG_LEVEL, LOG_FORMAT and LOG_DESTINATION variables are applied afterwards by
// internal/logging, so they take precedence over their INGET_LOG__* equivalents.
package config

import (
	"github.com/maxwellcudlitz/inget/internal/logging"
)

// LoadLog returns the logger options from path's log block, layered and env-overridden
// like any other setting. Empty fields keep internal/logging's defaults.
func LoadLog(path string) (logging.Options, error) {
	if path == "" {
		path = DefaultPath
	}
	v, err := newViper(path, false)
	if err != nil {
		return logging.Options{}, err
	}
	return Log{
		Level:       v.GetString("log.level"),
		Format:      v.GetString("log.format"),
		Destination: v.GetString("log.destination"),
	}.Options(), nil
}

// Options converts the configured log block into logger options.
func (l Log) Options() logging.Options {
	return logging.Options{Level: l.Level, Format: l.Format, Destination: l.Destination}
}
