# hug

Route each phase of agentic coding work to the right model, across every agent you use, and never run out of subscription in the middle of a task.

`hug` is a small Go daemon plus CLI. It sits between your agents and the model APIs, sees which phase a request belongs to (plan, implement, ship), rewrites the model per your rules, and learns your subscription usage from the responses so it can step down to cheaper models before a window runs dry.

Works with, verified live:

| App | How hug gets in |
|---|---|
| Claude Code (CLI) | `env.ANTHROPIC_BASE_URL` in `~/.claude/settings.json` |
| Claude app, Code tab | same file (the app bundles Claude Code) |
| T3 Code | each provider instance home (`CLAUDE_CONFIG_DIR`, `CODEX_HOME`) |
| Codex CLI | top-level `openai_base_url` in `~/.codex/config.toml` |
| ChatGPT app (Codex) | same file (the app bundles codex-cli) |

Plain claude.ai chat has no endpoint override and is out of scope.

## Install

```sh
go install github.com/tauantcamargo/hug/cmd/hug@latest
hug init        # writes ~/.hug/hug.toml, wires the apps above, starts the daemon (launchd)
hug status
```

`go install` puts the binary in `$(go env GOPATH)/bin`. If `hug` is not found afterwards, add that
directory to your `PATH`, or symlink it somewhere already on it:

```sh
ln -sf "$(go env GOPATH)/bin/hug" ~/.local/bin/hug
```

Restart open agent sessions once; from then on every request flows through `127.0.0.1:4711`.

## Update

```sh
go install github.com/tauantcamargo/hug/cmd/hug@latest
hug daemon restart
```

The restart is not optional. `go install` replaces the binary on disk, but launchd keeps running the
copy it already started, so without it you get a new CLI talking to an old daemon. `hug status`
catches that and says so:

```
! the daemon is running v0.1.7 while this CLI is v0.1.8 — run `hug daemon install` to point launchd at this binary
```

Use `hug daemon install` rather than `restart` when the binary *path* changed, since that rewrites
the LaunchAgent instead of just kicking the service. App configs are untouched by an update, so
there is no need to re-run `hug wire`.

## Toggle

```sh
hug off                 # restore the apps' own endpoints; hug is out of the way
hug off --for 2h        # pause instead, re-enables itself
hug off codex           # per app: claude | codex
hug off ship            # per phase: plan | implement | ship
hug pin claude-sonnet-5 # force one model for everything
hug on
```

`hug off` and `hug on` move the app config files, because "off" has to mean your agents keep
working without hug — including after a reboot, or any time the daemon isn't running. `hug off`
leaves the daemon up, so sessions you already have open still reach it and pass through untouched;
only newly started ones read the restored config and go direct.

Scoped and timed toggles (`hug off codex`, `hug off --for 2h`) are a pause, not a detach: they only
write `~/.hug/state.json`, which the daemon reads on every request, so they take effect instantly
in live sessions and leave the wiring alone.

**hug never leaves an app pointed at a port with nothing behind it.** That is the one state that
breaks every agent on the machine at once, so `hug on` refuses to wire until the daemon actually
answers, `hug daemon stop` and `hug daemon uninstall` unwire before stopping, and `hug status`
names any config that got stranded anyway:

```
  !! 2 app config(s) still point at 127.0.0.1:4711 with nothing listening.
     Every request from these will fail with connection refused:
       /Users/you/.claude/settings.local.json
     Fix with `hug daemon start` (resume) or `hug off` (restore their own endpoints).
```

## Daemon

```sh
hug daemon start      # load the LaunchAgent and re-wire if hug is on
hug daemon stop       # unwire, then unload — safe to leave a machine in
hug daemon restart    # reload after upgrading the binary
hug daemon install    # write the LaunchAgent and start it
hug daemon uninstall  # unwire, stop, remove the LaunchAgent
hug daemon run        # foreground, for debugging or a non-macOS supervisor
```

### Which file hug writes

For Claude, hug writes `settings.local.json`, not `settings.json`. Claude Code merges both, but the
shared one is commonly committed to a dotfiles repo — and a `127.0.0.1` daemon URL that follows you
to a second machine fails every request there. If an older hug put the URL in `settings.json`, the
next `hug on` moves it. `hug status` reads both, so a hand-moved `env` block still shows ✓.

## Routing

`~/.hug/hug.toml`:

```toml
[phases.plan]
anthropic = ["claude-fable-5-1", "claude-opus-5", "claude-sonnet-5"]
openai    = ["gpt-6-astra", "gpt-5.6-sol", "gpt-5.6-luna"]
effort    = "xhigh"

[phases.implement]
anthropic = ["claude-opus-5", "claude-sonnet-5"]
openai    = ["gpt-6-astra", "gpt-5.6-terra", "gpt-5.6-luna"]
effort    = "medium"

[phases.ship]
anthropic = ["claude-sonnet-5", "claude-haiku-4-5-20251001"]
openai    = ["gpt-5.6-luna"]
effort    = "low"

[budget]
conserve_at = 0.70   # tightest window at 70% → step down one model
critical_at = 0.90   # at 90% → cheapest model in the chain
pace        = true   # also step down when burn rate projects exhaustion before reset
pace_factor = 1.15
```

Each phase is a fallback chain, best model first. The `[budget]` tier picks the index:

- **normal**: first model, configured effort
- **conserve**: second model, effort one step lower. Triggered by the utilization threshold or by pace: if 40% is used with 90% of the window still ahead, the burn rate projects 400% and hug conserves early
- **critical / exhausted**: last model, effort two steps lower. Codex buckets that are per model (`additional_rate_limits`) are skipped individually

Phase detection uses markers the agents inject themselves, so it is deterministic:

- Claude Code: a `Plan mode is active` system-reminder in the **newest** user message. Claude Code rebuilds that reminder from the live permission mode on every request, so reading the newest turn is both sufficient and self-clearing — nothing has to detect leaving plan mode. The `<system-reminder>` wrapper is required, so typing, quoting or reading the words does not route you as planning
- Codex: `<collaboration_mode>Plan</collaboration_mode>` in developer instructions
- ship: either the agent has actually run a shipping command (`git commit`, `git push`, `gh pr create`), which is unambiguous, or the newest user message *orders* one. A keyword alone is not enough, since "commit" is a noun about as often as a verb: `commit this` and `update the changelog` are ship, while `the commit message convention`, `look at the last commit` and `should we commit?` are not. Configure the terms with `[detect] ship_keywords`; empty the list to turn the wording half off entirely

If the vendor rejects the model hug picked — a chain entry your CLI is too old for, a model your
account cannot reach — hug retries the turn down the rest of the chain, ending at the model the app
originally asked for, and remembers the dud so later requests skip it. Errors any model would hit
(auth, rate limits, oversized context) are passed straight back untouched.

Agents also fire side calls around each turn: conversation titles, classifiers, cache warmups. They
arrive with no tool schema, so hug leaves them on whatever cheap model the app already picked
instead of promoting them to the phase's top model. They show up as the `aux` phase in `hug status`.
Measured on Claude Code 2.1.266, that is one extra premium call per turn avoided.

"No tool schema" is not the same as "no `tools` array". Codex over websocket sends its schema
exactly once, as an `additional_tools` item in the thread's opening frame; every turn after that is
an incremental frame chained by `previous_response_id` that inherits it server-side. Reading those
as toolless files every real Codex turn as `aux` and routes nothing at all, so hug counts either
signal as a schema being in play.

### Codex models come in two wire protocols

OpenAI splits its Codex models across two incompatible wire protocols. `gpt-6-astra` and the
`gpt-5.6-*` family speak "responses lite"; `gpt-5.5` and `gpt-5.3-codex-spark` do not. Codex picks
the protocol from the model **you** selected and sends the matching handshake, so a request built
for a lite model that names a non-lite one is rejected outright:

```
{"type":"error","error":{"code":"unsupported_value",
 "message":"This model is not supported when using X-OpenAI-Internal-Codex-Responses-Lite."}}
```

hug learns which side each model is on from the model list Codex already fetches through it
(cached in `~/.hug/models.json`) and never swaps across that line. A model it has never seen in a
list is never swapped, since guessing wrong costs you a failed turn while not swapping costs
nothing. A chain that mixes both sides is not an error — it just has fewer rungs to step down to,
and `hug watch` says so:

```
implement phase, critical tier: primary window at 53% (2 chain model(s) skipped: not known to
share the wire protocol of gpt-6-astra)
```

Keep each `openai` chain on one side of the split. The defaults above already do.

## Usage awareness

- Anthropic: `anthropic-ratelimit-unified-{5h,7d}-utilization` and `-reset` response headers
- Codex: `codex.rate_limits` frames on the responses websocket (plan, primary/secondary windows, per-model limits)

Nothing is polled; hug only reads what the APIs already send. `hug status` shows windows, reset times, the current tier and the last decisions.

## Visibility

hug operates below every app's UI, so none of them — Claude Code, Codex, T3 Code, the desktop
apps — know a swap happened. Claude Code's own usage bookkeeping is keyed to the model *you
requested*, not the one the response actually came from, even inside its own SDK stream that T3
renders; there is no in-app badge to hook into without patching the CLI binary itself, which hug
does not do. What actually works, uniformly, because it comes from the daemon rather than any one
app:

```sh
hug watch
```

Live-tails every routing decision and every budget tier change as they happen, regardless of which
app made the request:

```
15:24:56 claude implement claude-haiku-4-5-20251001  → claude-opus-5    normal    implement phase, normal tier: 5h window at 21%
15:25:47 claude aux       claude-haiku-4-5-20251001  = claude-haiku...  normal    auxiliary call (no tool schema) — left on the model the app chose
15:41:02          ── anthropic now conserve — 5h window at 71% ──
```

Desktop notifications fire on the same events (macOS only today; a no-op elsewhere, hug still
works):

```toml
[notify]
tier_changes    = true   # a vendor entered or left a budget tier
routing_changes = true   # a phase started being served by a different model than you picked
cooldown        = "5m"   # minimum gap between tier notifications for the same vendor
```

Since no app UI can show the served model, `routing_changes` is the only in-your-face signal that a
rewrite happened. It is keyed by app and phase, not by session, so opening a new chat does not
re-announce a routing you already know about.

A first observation at `normal` just seeds the baseline and stays quiet. A first observation
*already* in a degraded tier does notify ("already conserve"): that means the daemon came up mid-
window and the crossing happened while it was down, so staying silent would leave you downgraded
with no signal at all.

To check that notifications actually reach your screen, ask the daemon to send one — it runs under
launchd, and that is the process whose delivery path matters:

```sh
hug notify --test
```

## How it works

- `/anthropic/*` is a reverse proxy to `api.anthropic.com`. `POST /v1/messages` bodies get their `model` rewritten. OAuth headers pass through untouched.
- `/openai/*` is a reverse proxy to `chatgpt.com/backend-api/codex` (subscription tokens) or `api.openai.com/v1` (API keys). Codex talks websocket; hug terminates it, rewrites `model` and `reasoning.effort` inside `response.create` frames, and keeps the `X-Codex-Routing-Hint` header consistent.
- The Codex model list flowing back through `/openai/models` is copied into `~/.hug/models.json` on its way to the app, which is how hug knows each model's wire protocol. The response itself is passed through untouched.
- Cross-vendor routing inside one session is not attempted; each vendor's chain only contains that vendor's models.

## Gotchas learned the hard way

- `openai_base_url` must be a top-level key in `config.toml`. After any `[table]` it silently belongs to that table. hug writes it at the top.
- GUI apps do not inherit your shell `PATH`, so hug never relies on shims; it edits the config files the apps already read.
- Claude Code reports usage under the *requested* model name, not the served one. `hug status` and `hug watch` show the truth.
- Codex sends its tool schema once per thread, not once per turn. Anything that keys off "this request has no tools" has to account for that or it silently skips every Codex turn.
- Swapping a Codex model is not free-form: the request's wire protocol was already chosen from the model the app picked. See the split above.

## Development

```sh
go test ./...
go run ./cmd/hug daemon run   # foreground, logs every decision
```
