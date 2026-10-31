<p align="center"><img src="assets/banner.svg" alt="agent-watch" width="720"></p>

A terminal app where a **lead** agent puts coding **agents** to work, and you watch all of them live.

You chat with the lead. When you agree on work, it shows you a plan. After your go, it gives each agent its own git worktree and a detailed brief, verifies their work itself (diffs, your tests, its own probes, cheat checks) and sends feedback until it's right. It never edits code itself: it has no edit tools, unless you switch on **may edit files** in the setup, which lets it make small fixes, resolve merge conflicts and do housekeeping while real work still goes to agents.

Claude Code and opencode can both lead or work, on any account and model, so Claude can lead opencode agents on cheap or free models, or the other way round.

<p align="center"><img src="assets/screenshot.png" alt="agent-watch: the lead on the left, two agents working on the right" width="900"></p>

## Quick start

**1. Have at least one agent CLI installed and logged in:** [Claude Code](https://claude.com/claude-code) (`claude`), [opencode](https://opencode.ai) (`opencode`), [Codex](https://github.com/openai/codex) (`codex`), [Antigravity](https://antigravity.google/docs/cli) (`agy`) or ZCode's [`zcode-app-cli`](https://www.npmjs.com/package/zcode-app-cli) (`zcode`), plus `git`.

**2. Install** a [release](https://github.com/n-delic/agent-watch/releases) for Linux or macOS:

```bash
tar -xzf agent-watch_*.tar.gz
mkdir -p ~/.local/bin
mv agent-watch_*/agent-watch ~/.local/bin/
ln -s ~/.local/bin/agent-watch ~/.local/bin/aw     # the short name
```

or build it with [Go](https://go.dev/dl/) 1.24+:

```bash
git clone https://github.com/n-delic/agent-watch && cd agent-watch
go build -o agent-watch .
ln -s "$PWD/agent-watch" ~/.local/bin/agent-watch
ln -s "$PWD/agent-watch" ~/.local/bin/aw
```

(`~/.local/bin` must be on your `PATH`.)

On Windows, download the `windows_amd64` (or `arm64`) zip from the release, unzip it and put `agent-watch.exe` on your `PATH`, copied as `aw.exe` for the short name. Or build it with Go:

```powershell
git clone https://github.com/n-delic/agent-watch; cd agent-watch
go build -o agent-watch.exe .
copy agent-watch.exe $HOME\.local\bin\aw.exe
```

Run it in [Windows Terminal](https://aka.ms/terminal); the legacy console host renders colors and borders poorly.

**3. Run it in the project you want to work on:**

```bash
cd ~/code/my-project
aw
```

**4. Pick your agents.** The first launch opens the setup: choose the lead (cli, account, model, effort) and which agents it may put to work, then `enter`. Change it any time with `ctrl+s`.

**5. Talk to the lead** like you would to Claude Code:

> add rate limiting to the API client, use two agents

It reads the code, proposes a plan (which agent does what, in which worktree) and waits. Say **go**, then watch the agents work on the right. When they're done and verified, it asks before committing or merging anything.

**6. Come back later:** `aw -c` continues the latest run in a directory, `aw -r` lets you pick an older one. The lead and its agents pick up their conversations where they left off.

What you see:

- Transcripts in Claude Code style (`⏺` messages and tool calls, `⎿` output), with agents' markdown rendered: headings, lists, tables, code.
- The agent list shows what each agent is doing right now: its latest tool call, or the start of its reply.
- A live spinner per agent with elapsed time, tokens (↑ in ↓ out) and cost; every turn ends with how long it took. An agent that shows nothing for a minute says so; after 10 minutes it's stopped and the lead is told (a hung model would otherwise sit there until the 2-hour timeout).
- A header with what the run has cost so far and the lead's usage, plus the selected agent's when it's on another account: Claude's 5-hour and 7-day windows (from its rate limit reports), Codex's plan windows (from its session files), opencode's spend today and over 7 days (`opencode stats`; it has no limits API), for zcode, the tokens this run used, and for agy, which reports nothing, the same, or its quota and reset time once it runs out.

## Agents

An agent is a CLI, an account and a model. A fresh setup has one per installed CLI on its default account and model; add as many as you like in the setup (`ctrl+s`, then **+ add an agent**). Each has:

| Field | |
|---|---|
| name | what you and the lead call it, e.g. `work` |
| cli | `claude`, `opencode`, `codex`, `agy` or `zcode` |
| account | `←→` picks a login found on this machine, or the CLI's default. opencode: the other databases next to its own (e.g. `~/.local/share/opencode/work.db`, run with `--standalone`). Claude: config dirs named `~/.claude-<name>` (as `CLAUDE_CONFIG_DIR`). Codex: homes named `~/.codex-<name>` (`CODEX_HOME`). agy and zcode have one login, so they have no account row. |
| model, effort | empty for the CLI's default. Type on a model row to search a list: opencode's own models for that account (with prices and effort levels; `free` finds the free ones), agy's (`agy models`), Claude's, or zcode's (Z.ai plan models, as `account:<plan>/<model>`); Codex can't list its models, so type its model name. Effort then offers only that model's levels. |
| lead may use it | whether the lead can put it to work |

The lead has its own cli, account, model and effort, separate from the agents: changing an agent never changes the lead. A fresh setup makes Claude Opus the lead: the idea is Opus's judgment on cheap agents' work. **may edit files** (off by default) gives it edit tools for small fixes, merges and housekeeping; a project file can switch it on with `"lead": {"edit": true}`. The setup is saved to `~/.config/agent-watch/config.json`.

Agents are types, not a team that always runs together: tell the lead which ones you want and how many, e.g. *"use 3 ds3 for the parsers and one opus for the review"*, and it starts just those. If you don't say, it picks a type and names it in its plan.

### Per project

A project can change the setup with an `.agent-watch.json` in its folder (or any parent). It has the same shape as the global config and only needs what changes: lead fields it sets win, and an agent with the same name as a global one replaces it; new names are added for this project only.

```json
{
  "lead": { "model": "sonnet" },
  "agents": [
    { "name": "ds3", "cli": "opencode", "model": "opencode-go/deepseek-v4.1-flash", "effort": "max", "enabled": true }
  ]
}
```

The setup screen edits the global config and shows what the project overrides; edit the project file itself to change those.

## Keys

| Key | |
|---|---|
| `enter` | send to the lead (or to the agent you're messaging). While the lead is busy your message waits for its turn to end; `enter` again on the empty input interrupts it and sends now. |
| `tab` | switch between input and agent list |
| `↑↓` | select an agent (in the list) |
| `enter` (list) | selected agent fullscreen |
| `m` (list) | message the selected agent directly; the lead is told |
| `x` (list) | stop the selected agent |
| `d` (list) | remove the selected agent once it's done; its worktree stays, and the lead is told |
| `pgup` / `pgdn` | scroll the lead (or the agent when the list is focused) |
| `esc` | back |
| `alt+v` | paste an image from the clipboard (`ctrl+v` too where the terminal passes it on); a dragged-in image path works as well |
| `ctrl+s` | setup |
| `ctrl+c` | quit (asks again if agents are running, then stops them) |
| click | an agent selects it, again opens it fullscreen; the fullscreen title closes it; a pane or the input focuses it |
| wheel | scrolls the pane under the pointer |

Mouse capture means selecting text needs `shift` + drag in most terminals.

Slash commands for the lead: `/model <name>` and `/effort <level>` change it for the rest of the run without losing the conversation, `/clear` starts a new conversation (the agents carry on), `/help` lists them. Anything else, like a skill or a custom command (`/review`), goes to the lead as typed; a Claude lead runs those.

## How it works

- The lead gets `lead.md` as its instructions and drives agents with `aw spawn|send|stop`. Those commands hand a request file to the running app, which checks it against your setup and starts the agent.
- When an agent finishes a turn, the app wakes the lead with the agent's final reply.
- Each turn has a 2-hour timeout. A stopped or timed-out turn is also interrupted on opencode's shared service, which would otherwise keep running it.
- Every run is saved in `~/.local/state/agent-watch/<run>/`: its state (sessions, transcripts, totals) and every agent's raw output.

## Update

Download the new release, or `git pull && go build -o agent-watch .`. Releases are built by GitHub Actions when a version tag is pushed: `git tag v0.2.0 && git push origin v0.2.0`.

## Limits

- Linux, macOS and Windows (amd64 and arm64).
- Agents run with full permissions (`--auto` / `bypassPermissions`), which is why the lead gives each its own worktree.
- Claude's and Codex's token counts arrive at the end of each of their turns; opencode's and agy's update after every step.
- Codex's limits update when one of its turns ends; agy shows only this run's tokens until its quota runs out.
- A Codex or agy lead can always edit files (neither CLI can hold back edits and still run commands); without **may edit files** only its instructions keep it from editing.
- zcode on Z.ai's Start Plan takes one request at a time per account, so run one zcode agent at a time, and not while the ZCode app is working.
- Not yet supported: local model servers, another agy or zcode account.

## License

[MIT](LICENSE)
