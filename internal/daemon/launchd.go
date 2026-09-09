// Package daemon installs the hug daemon as a macOS LaunchAgent so it is always running,
// which GUI apps require since they cannot start it on demand.
package daemon

import (
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"time"

	"github.com/tauantcamargo/hug/internal/config"
)

// Label is the launchd job label.
const Label = "dev.hug.daemon"

// PlistPath is the LaunchAgent file.
func PlistPath() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, "Library", "LaunchAgents", Label+".plist")
}

// Supported reports whether this platform has a supervisor integration.
func Supported() bool { return runtime.GOOS == "darwin" }

// Running probes the daemon health endpoint.
func Running(listen string) bool {
	c := http.Client{Timeout: 700 * time.Millisecond}
	res, err := c.Get("http://" + listen + "/hug/health")
	if err != nil {
		return false
	}
	res.Body.Close()
	return res.StatusCode == http.StatusOK
}

// Install writes the plist and (re)starts the job.
func Install(exe string) error {
	if !Supported() {
		return fmt.Errorf("automatic daemon install is only implemented for macOS; run `hug daemon run` under your supervisor")
	}
	logPath := filepath.Join(config.Dir(), "daemon.log")
	if err := os.MkdirAll(config.Dir(), 0o755); err != nil {
		return err
	}
	env := ""
	if h := os.Getenv("HUG_HOME"); h != "" {
		env = fmt.Sprintf("\n  <key>EnvironmentVariables</key>\n  <dict><key>HUG_HOME</key><string>%s</string></dict>", h)
	}
	plist := fmt.Sprintf(`<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
  <key>Label</key><string>%s</string>
  <key>ProgramArguments</key>
  <array><string>%s</string><string>daemon</string><string>run</string></array>
  <key>RunAtLoad</key><true/>
  <key>KeepAlive</key><true/>
  <key>StandardOutPath</key><string>%s</string>
  <key>StandardErrorPath</key><string>%s</string>%s
</dict>
</plist>
`, Label, exe, logPath, logPath, env)
	if err := os.MkdirAll(filepath.Dir(PlistPath()), 0o755); err != nil {
		return err
	}
	if err := os.WriteFile(PlistPath(), []byte(plist), 0o644); err != nil {
		return err
	}
	domain := fmt.Sprintf("gui/%d", os.Getuid())
	_ = exec.Command("launchctl", "bootout", domain, PlistPath()).Run()
	if out, err := exec.Command("launchctl", "bootstrap", domain, PlistPath()).CombinedOutput(); err != nil {
		return fmt.Errorf("launchctl bootstrap: %v: %s", err, out)
	}
	_ = exec.Command("launchctl", "kickstart", "-k", domain+"/"+Label).Run()
	return nil
}

// Uninstall stops the job and removes the plist.
func Uninstall() error {
	if !Supported() {
		return nil
	}
	domain := fmt.Sprintf("gui/%d", os.Getuid())
	_ = exec.Command("launchctl", "bootout", domain, PlistPath()).Run()
	if err := os.Remove(PlistPath()); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}

// Restart kicks the running job so it reloads config.
func Restart() error {
	if !Supported() {
		return fmt.Errorf("not supported on this platform")
	}
	domain := fmt.Sprintf("gui/%d", os.Getuid())
	out, err := exec.Command("launchctl", "kickstart", "-k", domain+"/"+Label).CombinedOutput()
	if err != nil {
		return fmt.Errorf("launchctl kickstart: %v: %s", err, out)
	}
	return nil
}

// WaitReady polls health until the deadline.
func WaitReady(listen string, d time.Duration) bool {
	deadline := time.Now().Add(d)
	for time.Now().Before(deadline) {
		if Running(listen) {
			return true
		}
		time.Sleep(150 * time.Millisecond)
	}
	return false
}
