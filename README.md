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

## Toggle

```sh
hug off                 # pass-through, live sessions keep working
hug off --for 2h        # re-enables itself
hug off codex           # per app: claude | codex
hug off ship            # per phase: plan | implement | ship
hug pin claude-sonnet-5 # force one model for everything
hug on
```

Toggling only writes `~/.hug/state.json`; the daemon reads it on every request. App configs are only touched by `hug init`, `hug wire`, `hug unwire` and `hug uninstall`.

## Routing

`~/.hug/hug.toml`:

```toml
[phases.plan]
anthropic = ["claude-fable-5-1", "claude-opus-5", "claude-sonnet-5"]
openai    = ["gpt-6-astra", "gpt-5.5"]
effort    = "xhigh"

[phases.implement]
anthropic = ["claude-opus-5", "claude-sonnet-5"]
openai    = ["gpt-6-astra", "gpt-5.5"]
effort    = "medium"

[phases.ship]
anthropic = ["claude-sonnet-5", "claude-haiku-4-5-20251001"]
openai    = ["gpt-5.6-luna", "gpt-5.5"]
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

- Claude Code: `Plan mode is active` system-reminder in the user turn, cleared by an `ExitPlanMode` tool call
- Codex: `<collaboration_mode>Plan</collaboration_mode>` in developer instructions
- ship: keyword heuristic on the last user message (`[detect] ship_keywords`), off when the list is empty

## Usage awareness

- Anthropic: `anthropic-ratelimit-unified-{5h,7d}-utilization` and `-reset` response headers
- Codex: `codex.rate_limits` frames on the responses websocket (plan, primary/secondary windows, per-model limits)

Nothing is polled; hug only reads what the APIs already send. `hug status` shows windows, reset times, the current tier and the last decisions.

## How it works

- `/anthropic/*` is a reverse proxy to `api.anthropic.com`. `POST /v1/messages` bodies get their `model` rewritten. OAuth headers pass through untouched.
- `/openai/*` is a reverse proxy to `chatgpt.com/backend-api/codex` (subscription tokens) or `api.openai.com/v1` (API keys). Codex talks websocket; hug terminates it, rewrites `model` and `reasoning.effort` inside `response.create` frames, and keeps the `X-Codex-Routing-Hint` header consistent.
- Cross-vendor routing inside one session is not attempted; each vendor's chain only contains that vendor's models.

## Gotchas learned the hard way

- `openai_base_url` must be a top-level key in `config.toml`. After any `[table]` it silently belongs to that table. hug writes it at the top.
- GUI apps do not inherit your shell `PATH`, so hug never relies on shims; it edits the config files the apps already read.
- Claude Code reports usage under the *requested* model name, not the served one. `hug status` shows the truth.

## Development

```sh
go test ./...
go run ./cmd/hug daemon run   # foreground, logs every decision
```
