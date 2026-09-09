package proxy

import (
	"os"
	"testing"
)

// The daemon reads the on/off switch from $HUG_HOME on every request, so without this the
// suite would inherit whatever `hug off` state the developer's own machine happens to be in.
func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "hug-test-")
	if err != nil {
		panic(err)
	}
	os.Setenv("HUG_HOME", dir)
	code := m.Run()
	os.RemoveAll(dir)
	os.Exit(code)
}
