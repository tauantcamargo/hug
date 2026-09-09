// hug routes each phase of agentic coding work (plan, implement, ship) to the model you
// choose, across Claude Code, Codex, T3 Code and the desktop apps, and protects your
// subscription windows by stepping down models as usage tightens.
package main

import (
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"runtime/debug"
	"sort"
	"strings"
	"time"

	"github.com/tauantcamargo/hug/internal/config"
	"github.com/tauantcamargo/hug/internal/daemon"
	"github.com/tauantcamargo/hug/internal/policy"
	"github.com/tauantcamargo/hug/internal/proxy"
	"github.com/tauantcamargo/hug/internal/state"
	"github.com/tauantcamargo/hug/internal/usage"
	"github.com/tauantcamargo/hug/internal/wire"
)

var version = "dev"

// init fills in the version from the module build info, so `go install ...@v0.1.0`
// reports v0.1.0 without needing ldflags.
func init() {
	if version != "dev" {
		return
	}
	if bi, ok := debug.ReadBuildInfo(); ok && bi.Main.Version != "" && bi.Main.Version != "(devel)" {
		version = bi.Main.Version
	}
}

const usageText = `hug — per-phase model routing for coding agents

Usage:
  hug init [--no-daemon] [--dry-run]   write config, wire installed apps, start the daemon
  hug on  [target...]                  enable routing (targets: claude codex plan implement ship)
  hug off [--for 2h] [target...]       disable routing globally, per app or per phase
  hug pin <model> | hug unpin          force one model for everything
  hug status [--json]                  switch state, usage per vendor, recent decisions
  hug daemon run|install|uninstall|restart
  hug wire | hug unwire [--dry-run]    (re)apply or remove app configuration only
  hug uninstall                        unwire apps and remove the daemon (keeps ~/.hug)
  hug version

Config: ` + "`hug.toml`" + ` in $HUG_HOME or ~/.hug
`

func main() {
	log.SetFlags(log.Ltime)
	proxy.Version = version
	if len(os.Args) < 2 {
		fmt.Print(usageText)
		os.Exit(2)
	}
	args := os.Args[2:]
	var err error
	switch os.Args[1] {
	case "init":
		err = cmdInit(args)
	case "on":
		err = cmdToggle(true, args)
	case "off":
		err = cmdToggle(false, args)
	case "pin":
		err = cmdPin(args)
	case "unpin":
		err = cmdPin(nil)
	case "status":
		err = cmdStatus(args)
	case "daemon":
		err = cmdDaemon(args)
	case "wire":
		err = cmdWire(true, args)
	case "unwire":
		err = cmdWire(false, args)
	case "uninstall":
		err = cmdUninstall()
	case "version", "--version", "-v":
		fmt.Println("hug", version)
	case "help", "-h", "--help":
		fmt.Print(usageText)
	default:
		fmt.Fprintf(os.Stderr, "unknown command %q\n\n%s", os.Args[1], usageText)
		os.Exit(2)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "hug:", err)
		os.Exit(1)
	}
}

func has(args []string, flag string) bool {
	for _, a := range args {
		if a == flag {
			return true
		}
	}
	return false
}

func cmdInit(args []string) error {
	dry := has(args, "--dry-run")
	if dry {
		if _, err := os.Stat(config.Path()); os.IsNotExist(err) {
			fmt.Println("  would write", config.Path())
		}
	} else {
		created, err := config.EnsureDefault()
		if err != nil {
			return err
		}
		if created {
			fmt.Println("wrote", config.Path())
		}
	}
	if err := cmdWire(true, args); err != nil {
		return err
	}
	if has(args, "--no-daemon") || dry {
		fmt.Println("\ndaemon not started. Run `hug daemon run` (foreground) or `hug daemon install`.")
		return nil
	}
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	exe, _ := os.Executable()
	exe, _ = filepath.EvalSymlinks(exe)
	if err := daemon.Install(exe); err != nil {
		return err
	}
	if !daemon.WaitReady(cfg.Listen, 5*time.Second) {
		return fmt.Errorf("daemon did not come up on %s; check %s", cfg.Listen, filepath.Join(config.Dir(), "daemon.log"))
	}
	fmt.Printf("\ndaemon running on %s (LaunchAgent %s)\n", cfg.Listen, daemon.Label)
	fmt.Println("restart your agent sessions so they pick up the new base URL. `hug status` shows routing live.")
	return nil
}

func cmdWire(enable bool, args []string) error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	dry := has(args, "--dry-run")
	verb := "wired"
	if !enable {
		verb = "unwired"
	}
	for _, t := range wire.Discover() {
		base := cfg.BaseURL("anthropic")
		if t.App == "codex" {
			base = cfg.BaseURL("openai")
		}
		if dry {
			fmt.Printf("  would %s %-40s %s\n", strings.TrimSuffix(verb, "d"), t.Label, t.Path)
			continue
		}
		changed, err := wire.Apply(t, base, enable)
		if err != nil {
			return fmt.Errorf("%s: %w", t.Label, err)
		}
		mark := "·"
		if changed {
			mark = "✓"
		}
		fmt.Printf("  %s %-8s %-40s %s\n", mark, verb, t.Label, t.Path)
	}
	return nil
}

func cmdToggle(enable bool, args []string) error {
	st := state.Load()
	var targets []string
	var until *time.Time
	for i := 0; i < len(args); i++ {
		switch {
		case args[i] == "--for" && i+1 < len(args):
			d, err := time.ParseDuration(args[i+1])
			if err != nil {
				return fmt.Errorf("bad duration %q (try 2h, 45m)", args[i+1])
			}
			t := time.Now().Add(d)
			until = &t
			i++
		case strings.HasPrefix(args[i], "--for="):
			d, err := time.ParseDuration(strings.TrimPrefix(args[i], "--for="))
			if err != nil {
				return err
			}
			t := time.Now().Add(d)
			until = &t
		default:
			targets = append(targets, args[i])
		}
	}
	if len(targets) == 0 {
		st.Enabled = enable
		st.Until = nil
		if !enable {
			st.Until = until
		}
	}
	for _, t := range targets {
		switch t {
		case "claude", "codex":
			if st.Apps == nil {
				st.Apps = map[string]bool{}
			}
			if enable {
				delete(st.Apps, t)
			} else {
				st.Apps[t] = false
			}
		case "plan", "implement", "ship":
			if st.Phases == nil {
				st.Phases = map[string]bool{}
			}
			if enable {
				delete(st.Phases, t)
			} else {
				st.Phases[t] = false
			}
		default:
			return fmt.Errorf("unknown target %q (use claude, codex, plan, implement, ship)", t)
		}
	}
	if err := state.Save(st); err != nil {
		return err
	}
	fmt.Println("hug:", st.Summary(time.Now()))
	return nil
}

func cmdPin(args []string) error {
	st := state.Load()
	st.Pin = ""
	if len(args) > 0 {
		st.Pin = args[0]
	}
	if err := state.Save(st); err != nil {
		return err
	}
	fmt.Println("hug:", st.Summary(time.Now()))
	return nil
}

func cmdDaemon(args []string) error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	sub := "run"
	if len(args) > 0 {
		sub = args[0]
	}
	switch sub {
	case "run":
		store := usage.NewStore(filepath.Join(config.Dir(), "usage.json"))
		dlog := proxy.NewDecisionLog(filepath.Join(config.Dir(), "decisions.jsonl"), 200)
		srv := proxy.New(cfg, store, dlog)
		log.Printf("hug %s listening on %s (anthropic -> %s, openai -> %s | %s)", version, cfg.Listen, cfg.Upstreams.Anthropic, cfg.Upstreams.OpenAIChatGPT, cfg.Upstreams.OpenAIAPI)
		return http.ListenAndServe(cfg.Listen, srv.Handler())
	case "install":
		exe, _ := os.Executable()
		exe, _ = filepath.EvalSymlinks(exe)
		if err := daemon.Install(exe); err != nil {
			return err
		}
		if !daemon.WaitReady(cfg.Listen, 5*time.Second) {
			return fmt.Errorf("daemon did not come up; see %s", filepath.Join(config.Dir(), "daemon.log"))
		}
		fmt.Println("daemon installed and running on", cfg.Listen)
	case "uninstall":
		if err := daemon.Uninstall(); err != nil {
			return err
		}
		fmt.Println("daemon removed")
	case "restart":
		if err := daemon.Restart(); err != nil {
			return err
		}
		fmt.Println("daemon restarted")
	default:
		return fmt.Errorf("unknown daemon subcommand %q", sub)
	}
	return nil
}

func cmdUninstall() error {
	if err := cmdWire(false, nil); err != nil {
		return err
	}
	if err := daemon.Uninstall(); err != nil {
		return err
	}
	fmt.Println("hug removed from apps and launchd. Config kept in", config.Dir())
	return nil
}

func cmdStatus(args []string) error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	now := time.Now()
	c := http.Client{Timeout: time.Second}
	res, err := c.Get("http://" + cfg.Listen + "/hug/status")
	if err != nil {
		st := state.Load()
		fmt.Printf("hug: %s\ndaemon: NOT RUNNING on %s (apps pointed at hug will fail until it starts: `hug daemon install`)\n", st.Summary(now), cfg.Listen)
		return nil
	}
	defer res.Body.Close()
	var s proxy.Status
	if err := json.NewDecoder(res.Body).Decode(&s); err != nil {
		return err
	}
	if has(args, "--json") {
		b, _ := json.MarshalIndent(s, "", "  ")
		fmt.Println(string(b))
		return nil
	}
	fmt.Printf("hug: %s\ndaemon: running on %s, up %s, version %s\n", s.State.Summary(now), s.Listen, s.Uptime, s.Version)
	fmt.Println("\nwiring:")
	for _, t := range wire.Discover() {
		mark := "✗"
		if wire.Wired(t) {
			mark = "✓"
		}
		fmt.Printf("  %s %-40s %s\n", mark, t.Label, t.Path)
	}
	fmt.Println("\nusage:")
	vendors := make([]string, 0, len(s.Usage))
	for v := range s.Usage {
		vendors = append(vendors, v)
	}
	sort.Strings(vendors)
	if len(vendors) == 0 {
		fmt.Println("  no data yet — usage is learned from the first request that flows through hug")
	}
	for _, v := range vendors {
		sn := s.Usage[v]
		var parts []string
		for _, w := range sn.Windows {
			p := fmt.Sprintf("%s %.0f%%", w.Name, w.Utilization*100)
			if !w.ResetAt.IsZero() {
				p += " (resets " + fmtReset(w.ResetAt, now) + ")"
			}
			parts = append(parts, p)
		}
		tier := s.Tiers[v]
		fmt.Printf("  %-10s %-55s tier: %s — %s\n", v, strings.Join(parts, ", "), tier.Tier, tier.Reason)
		for name, w := range sn.PerModel {
			if w.Status == "rejected" || w.Utilization >= 0.5 {
				fmt.Printf("             %s %.0f%% %s\n", name, w.Utilization*100, w.Status)
			}
		}
	}
	fmt.Println("\nrecent decisions:")
	if len(s.Recent) == 0 {
		fmt.Println("  none yet")
	}
	start := 0
	if len(s.Recent) > 10 {
		start = len(s.Recent) - 10
	}
	for _, d := range s.Recent[start:] {
		arrow := "="
		if d.Rewritten {
			arrow = "→"
		}
		fmt.Printf("  %s %-6s %-9s %-26s %s %-26s %-9s %s\n", d.Time.Local().Format("15:04:05"), d.App, d.Phase, d.Requested, arrow, d.Model, d.Tier, d.Reason)
	}
	_ = policy.TierNormal
	return nil
}

func fmtReset(t, now time.Time) string {
	if t.Sub(now) < 24*time.Hour {
		return t.Local().Format("15:04")
	}
	return t.Local().Format("Mon 15:04")
}
