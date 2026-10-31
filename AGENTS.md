# Working on agent-watch

A Go terminal app (Bubble Tea) where a **lead** agent puts coding **agents** to work and the user watches them all. Say "lead" and "agents", never "master"/"slave".

## Commands

```bash
go vet ./... && go test ./...      # run both before every commit
go build -o agent-watch .          # ~/.local/bin/aw links to this binary
gofmt -l .                         # must print nothing
```

Go isn't on every PATH here; it may live in `/usr/local/go/bin`. Keep `go.mod`'s `go` line at the lowest version the dependencies need (now 1.24.2), not the toolchain's.

## Map

| File | What |
|---|---|
| `main.go` | flags (`-c`, `-r`, `-v`, `-h`); anything else goes to `leadCLI` |
| `config.go` | `Config`: the `Lead` (its own cli/account/model/effort) and the `Agents` list (types, any number of agents each); a project's `.agent-watch.json` on top (`withProject`); colors; `accountKey` |
| `cli/cli.go` | the template every CLI follows: the `CLI` interface (`Command`, `NewParser`, sessions, `Accounts`, `Models`, `Usage`/`Spend`/`Limits`, `Interrupt`), `Base` for what a CLI doesn't do, and the shared types (`Line`, `Delta`, `Limits`, `Turn`) |
| `cli/<name>/` | one CLI each (`claude`, `opencode`, `codex`, `agy`, `zcode`): its command, its output parser, and its own tests against real output in `testdata/` |
| `agent.go` | `allCLIs` (the setup's order), `cliOf`, and `command`, which turns a CLI's `Command` into a process |
| `run.go` | `agentState.start/stop`; the request files behind `aw spawn/send/stop`; `handle` validates them |
| `tui.go` | the model, keys, mouse, layout, views |
| `look.go` | colors, Claude Code style transcript (`⏺`, `⎿`), markdown via glamour, spinner, `panel` |
| `setup.go` | the setup screen (`ctrl+s`), with the model picker on model rows |
| `models.go` | the picker's lists, from each CLI's `Models` per account; `matches` |
| `usage.go` | header: each account's usage, as its CLI's `Usage` says (limit windows, spend, or this run's tokens) |
| `persist.go` | `state.json` per run; `aw -c` / `aw -r` |
| `lead.md` | the lead's instructions, embedded; `{{EXE}}`, `{{CWD}}`, `{{AGENTS}}` are filled in |

How a run works: the lead runs `aw spawn|send|stop` through its Bash tool. That writes `<run>/requests/<id>.json`; the TUI polls the folder, `handle`s it and writes `<id>.res`, which the command prints. When an agent's turn ends, the TUI queues an `[agent-watch] ...` message and starts the lead's next turn with it. Each turn is a fresh `claude -p --resume` / `opencode run --session` process.

## Things that will bite you

All of these were real bugs. Each has a test or a comment; keep both.

- **opencode flags go after `run`.** `opencode --standalone run ...` prints help instead of running. `TestCommand` checks the subcommand comes first.
- **opencode hangs on an open non-tty stdin.** Agent processes get no stdin (nil = /dev/null). Never give them the TUI's.
- **opencode reads its directory from `$PWD`**, not the process cwd. `command` sets `PWD` to the workdir.
- **opencode's shared background service keeps running a session after its client dies.** Stopping or timing out a default-account opencode turn must also call `session.interrupt` (opencode's `Interrupt`). `--standalone` (another account) dies with its client.
- **Signals and child processes:** agents run in their own process group and are stopped by killing that group. On Unix that is `Setpgid` plus `SIGTERM` to the group (`proc_unix.go`); on Windows it is `CREATE_NEW_PROCESS_GROUP` plus `taskkill /T /F` (`proc_windows.go`).
- **Session ids:** Claude wants a v4 UUID; opencode wants its own shape, `ses_` + 12 hex + 14 base62 (each CLI's `NewSession`). Any other shape and opencode's free models answer `403 FreeTierError`.
- **A hung provider is silent.** opencode retries a dead model every 15 minutes without printing anything, so an agent whose output shows nothing for `stallAfter` (10m) is stopped and the lead is told. `opencode-go/beam-free` hangs like this; `opencode/beam-free` works.
- **Codex, agy and zcode name their own sessions.** Their `NewSession` is `Base`'s ""; the id comes back in their first turn's output (Codex `thread.started`, agy `init`, zcode's `sessionId` on every event) as `Delta.Session`, and later turns resume it (`codex exec resume <id>`, `agy --conversation <id>`, `zcode --resume <id>`). None has a system-prompt flag, so the lead's instructions go in front of its first prompt.
- **Where usage comes from** (`accountUsage`, by each CLI's `Usage`): Claude's `rate_limit_event`; Codex's session file (`~/.codex/sessions/.../rollout-*-<id>.jsonl`, codex's `Limits`, read when a turn ends, since `exec --json` leaves limits out); opencode's `stats`; agy reports nothing until its quota runs out (`agyQuota`). All become `Limits` windows labeled by length (`windowLabel`).
- **agy's `result` usage covers the whole conversation**, not the turn: tokens are summed from each step's usage. Its text arrives in pieces per step (state kept by its `NewParser`, one per turn), and its `--mode plan` runs no commands at all, so it's no use for a lead.
- **Codex effort goes into TOML** (`-c model_reasoning_effort="..."`): only words from codex's `Efforts` may reach it.
- **Another account:** opencode uses `OPENCODE_DB=<file>` plus `--standalone`; Codex uses `CODEX_HOME=<dir>`; Claude uses `CLAUDE_CONFIG_DIR=<dir>`. agy has one login, so it keeps `Base`'s `Accounts` and the setup shows it no account row. The setup offers what each CLI's `Accounts` finds (opencode's other `.db` files, `~/.claude-*`, `~/.codex-*`) and a cli change clears the account, which belonged to the old one.
- **zcode is the npm `zcode-app-cli`**, not `/usr/bin/zcode` (ZCode's desktop app, which opens a window). On Z.ai's Start Plan it needs the runtime patched (`zcode-start-plan`, from Puyodead1/zcode-cli's `feat/start-plan`), and the plan takes one request at a time per account: a second zcode agent, or the ZCode app in use, gets `concurrency limit exceeded`.
- **glamour's dark style keeps `##` before headings** and colors inline code red; `markdown` overrides both. The user cares how agent markdown looks.
- **The lead has no edit tools by default** (`--allowedTools "Bash Read Grep Glob"`, opencode's `plan` agent): its job is to brief and verify, not to fix agents' code. Only the user's **may edit files** setting (`Lead.Edit`) gives them back, along with the small-fixes rule (`editRule`). Keep `--append-system-prompt` right after `--allowedTools`: the tool list takes several values and would swallow a prompt that followed it.

- **The lead does the agents' work if `lead.md` lets it.** Asked to "spin up agents to check" a PR, it read the PR itself and answered before spawning anyone. Its "Delegate, don't do" section counts checking, reviewing and research as agent work. Keep any investigation step limited to what the brief needs.
- **Slash commands:** every lead turn is a fresh `claude -p` (or `codex exec`, ...), so the CLI's own `/model` or `/clear` would only last that turn. `command` handles those in the app; anything else passes through, and `flushLead` puts it first in the prompt, the only place Claude reads it.

## Adding a CLI

Make `cli/<name>/` with a `CLI` type that embeds `cli.Base` and has `Name`, `Command` and `NewParser`; override only what it really does (a session id shape, other accounts, a model list, usage). Put real, sanitized output in its `testdata/` and test the parser with `clitest`. Then add it to `allCLIs` in `agent.go`; the setup, the header and the lead's commands pick it up from there.

## Keep compatible

- The request JSON between `leadCLI` and the TUI: a running TUI may be an older binary than the `aw` the lead calls.
- `config.json` and `state.json`: users have them. Loading an older shape must keep working (see the v0.1.0 lead in `loadConfig`).
- The setup edits only the global config (`model.global`); what runs is `model.cfg`, the global plus the project file. Never save `cfg`, or a project's overrides leak into every project.

## Style

- Read the surrounding code and match it. Comments say why, in one line; most code has none.
- The smallest change that fully works. No options or abstractions nobody asked for.
- New logic gets a test. UI changes get looked at: the TUI can be driven in a pseudo-terminal and rendered to PNG to check what it really looks like.
- Colors: the lead always uses the accent (`cAccent`); agents get `palette` colors by their place in the list, so editing an agent never recolors anything.

## Releasing

Push a version tag: `git tag v0.3.0 && git push origin v0.3.0`. `.github/workflows/release.yml` tests, builds Linux, macOS and Windows (amd64, arm64; Windows as zip) with `-X main.version=<tag>`, and publishes the archives and `checksums.txt`.
