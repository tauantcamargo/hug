// Package notify sends a native desktop notification when hug's routing state changes
// in a way worth interrupting you for — today, a vendor entering or leaving a budget tier.
package notify

import (
	"os/exec"
	"runtime"
)

// Notifier sends one notification. Implementations must not block for long or panic.
type Notifier interface {
	Notify(title, message string) error
}

// New returns the best notifier for this platform: native on macOS, a no-op elsewhere.
// A no-op is not an error — hug's routing and logs work the same regardless.
func New() Notifier {
	if runtime.GOOS == "darwin" {
		return osascriptNotifier{}
	}
	return NoopNotifier{}
}

// NoopNotifier discards every notification. Used on unsupported platforms and in tests
// that only want to assert what hug *would* have sent.
type NoopNotifier struct{}

func (NoopNotifier) Notify(string, string) error { return nil }

type osascriptNotifier struct{}

func (osascriptNotifier) Notify(title, message string) error {
	script := `display notification ` + quote(message) + ` with title ` + quote(title)
	return exec.Command("osascript", "-e", script).Run()
}

// quote escapes a string for embedding in an AppleScript string literal.
func quote(s string) string {
	out := make([]byte, 0, len(s)+2)
	out = append(out, '"')
	for i := 0; i < len(s); i++ {
		if s[i] == '"' || s[i] == '\\' {
			out = append(out, '\\')
		}
		out = append(out, s[i])
	}
	out = append(out, '"')
	return string(out)
}

// Recording is a Notifier that stores every call, for tests.
type Recording struct{ Sent []string }

func (r *Recording) Notify(title, message string) error {
	r.Sent = append(r.Sent, title+": "+message)
	return nil
}
