// Tests for the header parsers and the GitHub limiter's pause decisions.
//
// Nothing here sleeps. The limiter's clock is injected, so a test asserts what it decided to wait
// for rather than waiting for it; a test that actually slept would be slow and would still not
// prove the decision was right.
package ratelimit

import (
	"context"
	"net/http"
	"strconv"
	"testing"
	"time"
)

// fixedNow is the reference instant every test measures against.
var fixedNow = time.Date(2026, 8, 1, 12, 0, 0, 0, time.UTC)

func header(pairs ...string) http.Header {
	h := http.Header{}
	for i := 0; i+1 < len(pairs); i += 2 {
		h.Set(pairs[i], pairs[i+1])
	}
	return h
}

func TestRetryAfter(t *testing.T) {
	tests := []struct {
		name   string
		header http.Header
		want   time.Duration
		ok     bool
	}{
		{"absent", header(), 0, false},
		{"seconds", header("Retry-After", "60"), time.Minute, true},
		{"zero seconds", header("Retry-After", "0"), 0, true},
		{"negative seconds", header("Retry-After", "-5"), 0, false},
		{"garbage", header("Retry-After", "soon"), 0, false},
		{"http date in the future", header("Retry-After", fixedNow.Add(90*time.Second).Format(http.TimeFormat)), 90 * time.Second, true},
		{"http date in the past", header("Retry-After", fixedNow.Add(-time.Hour).Format(http.TimeFormat)), 0, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := RetryAfter(tt.header, fixedNow)
			if ok != tt.ok || got != tt.want {
				t.Errorf("RetryAfter() = %v, %v; want %v, %v", got, ok, tt.want, tt.ok)
			}
		})
	}
}

func TestUnixReset(t *testing.T) {
	tests := []struct {
		name  string
		value string
		want  time.Duration
		ok    bool
	}{
		{"absent", "", 0, false},
		{"future", intString(fixedNow.Add(5 * time.Minute).Unix()), 5 * time.Minute, true},
		{"past", intString(fixedNow.Add(-time.Minute).Unix()), 0, true},
		{"garbage", "later", 0, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := header()
			if tt.value != "" {
				h.Set(HeaderReset, tt.value)
			}
			got, ok := UnixReset(h, HeaderReset, fixedNow)
			if ok != tt.ok || got != tt.want {
				t.Errorf("UnixReset() = %v, %v; want %v, %v", got, ok, tt.want, tt.ok)
			}
		})
	}
}

// newTestGitHub builds a limiter with a frozen clock and a bucket wide enough that only the
// reactive pause is under test.
func newTestGitHub() *GitHub {
	g := NewGitHub(1000, 1000)
	g.now = func() time.Time { return fixedNow }
	return g
}

func TestGitHubObservePauses(t *testing.T) {
	tests := []struct {
		name   string
		header http.Header
		want   time.Duration
	}{
		{
			name:   "no headers does not pause",
			header: header(),
			want:   0,
		},
		{
			name:   "quota remaining does not pause",
			header: header(HeaderRemaining, "4321", HeaderReset, intString(fixedNow.Add(time.Hour).Unix())),
			want:   0,
		},
		{
			name:   "exhausted quota waits for the reset",
			header: header(HeaderRemaining, "0", HeaderReset, intString(fixedNow.Add(15*time.Minute).Unix())),
			want:   15 * time.Minute,
		},
		{
			name:   "retry-after wins over the quota counter",
			header: header("Retry-After", "30", HeaderRemaining, "0", HeaderReset, intString(fixedNow.Add(time.Hour).Unix())),
			want:   30 * time.Second,
		},
		{
			name:   "exhausted quota with no reset does not pause",
			header: header(HeaderRemaining, "0"),
			want:   0,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			g := newTestGitHub()
			g.Observe(&http.Response{StatusCode: http.StatusOK, Header: tt.header})
			if got := g.remainingPause(); got != tt.want {
				t.Errorf("remainingPause() = %v, want %v", got, tt.want)
			}
		})
	}
}

// A response carrying no rate-limit headers must not cancel a pause another response asked for:
// several requests are in flight at once, and the throttled one is the one that knows.
func TestGitHubPauseIsNeverShortened(t *testing.T) {
	g := newTestGitHub()
	g.Observe(&http.Response{Header: header("Retry-After", "120")})
	g.Observe(&http.Response{Header: header("Retry-After", "10")})
	g.Observe(&http.Response{Header: header()})

	if got := g.remainingPause(); got != 2*time.Minute {
		t.Errorf("remainingPause() = %v, want the longest pause observed (2m)", got)
	}
}

func TestGitHubObserveIgnoresNil(t *testing.T) {
	g := newTestGitHub()
	g.Observe(nil)
	if got := g.remainingPause(); got != 0 {
		t.Errorf("remainingPause() = %v after a nil response, want 0", got)
	}
}

// A limiter with no pause outstanding must not block, which is the common case on every request.
func TestGitHubWaitReturnsWithoutPause(t *testing.T) {
	g := NewGitHub(1000, 10)
	if err := g.Wait(t.Context()); err != nil {
		t.Fatalf("Wait() = %v, want nil", err)
	}
}

// A cancelled context ends a pause rather than sleeping it out, which is what makes SIGTERM
// during a rate-limit wait terminate promptly.
func TestGitHubWaitHonoursContext(t *testing.T) {
	g := NewGitHub(1000, 10)
	g.Observe(&http.Response{Header: header("Retry-After", "3600")})

	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if err := g.Wait(ctx); err == nil {
		t.Error("Wait() = nil on a cancelled context, want the context error")
	}
}

func TestUnlimitedNeverWaits(t *testing.T) {
	var l Limiter = Unlimited{}
	if err := l.Wait(t.Context()); err != nil {
		t.Fatalf("Unlimited.Wait() = %v, want nil", err)
	}
	l.Observe(&http.Response{Header: header("Retry-After", "3600")})
}

func intString(v int64) string { return strconv.FormatInt(v, 10) }
