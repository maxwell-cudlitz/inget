// Duration is the configuration file's time type. It exists because the documented
// schema uses day suffixes (`retention.runs: 30d`) alongside ordinary Go durations
// (`timeout: 120s`), and time.ParseDuration rejects `d`.
//
// Decoding goes through encoding.TextUnmarshaler, which the loader registers as a
// mapstructure hook, so the same syntax works from YAML and from an environment
// override.
package config

import (
	"fmt"
	"strconv"
	"strings"
	"time"
)

// Day is the unit time.ParseDuration lacks. Configuration durations are wall-clock
// retention windows, so a fixed 24-hour day is the right approximation.
const Day = 24 * time.Hour

// Duration wraps time.Duration with day-aware text parsing.
type Duration time.Duration

// Duration returns the wrapped value.
func (d Duration) Duration() time.Duration { return time.Duration(d) }

// String renders the value using time.Duration's formatting.
func (d Duration) String() string { return time.Duration(d).String() }

// UnmarshalText parses a duration with an optional leading day component: "30d",
// "1d12h" and "120s" are all accepted.
func (d *Duration) UnmarshalText(text []byte) error {
	s := strings.TrimSpace(string(text))
	if s == "" {
		*d = 0
		return nil
	}

	var total time.Duration
	if days, rest, ok := splitDays(s); ok {
		n, err := strconv.ParseFloat(days, 64)
		if err != nil {
			return fmt.Errorf("parsing duration %q: bad day count %q", s, days)
		}
		total = time.Duration(n * float64(Day))
		s = rest
		if s == "" {
			*d = Duration(total)
			return nil
		}
	}

	parsed, err := time.ParseDuration(s)
	if err != nil {
		return fmt.Errorf("parsing duration %q: want a Go duration or a day suffix such as 30d", string(text))
	}
	*d = Duration(total + parsed)
	return nil
}

// MarshalText keeps a round trip stable for anything that serializes the config.
func (d Duration) MarshalText() ([]byte, error) { return []byte(d.String()), nil }

// splitDays separates a leading "<number>d" component from the remainder. It reports
// false when the string does not start with one.
func splitDays(s string) (days, rest string, ok bool) {
	for i, r := range s {
		switch {
		case r >= '0' && r <= '9', r == '.':
			continue
		case r == 'd' && i > 0:
			return s[:i], s[i+1:], true
		default:
			return "", s, false
		}
	}
	return "", s, false
}
