package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	xansi "github.com/charmbracelet/x/ansi"

	"github.com/n-delic/agent-watch/cli/agy"
	"github.com/n-delic/agent-watch/cli/codex"
)

// Every block renders within the width, whatever the kind, and markdown loses its markup.
func TestRender(t *testing.T) {
	lines := []Line{{Kind: "you", Text: "do it"}, {Kind: "text", Text: "## Summary\n- **bold** and `code`"},
		{Kind: "tool", Text: "Bash", Arg: strings.Repeat("x", 300)}, {Kind: "out", Text: "OK"}, {Kind: "err", Text: "bad"},
		{Kind: "think", Text: "hmm"}, {Kind: "done", Text: "turn 1 done"}, {Kind: "fail", Text: "turn 2 failed"}, {Kind: "info", Text: "note"}}
	out := render(lines, 40)
	for _, l := range strings.Split(out, "\n") {
		if w := lipgloss.Width(l); w > 40 {
			t.Errorf("line is %d wide: %q", w, l)
		}
	}
	if plain := xansi.Strip(out); strings.Contains(plain, "**") || strings.Contains(plain, "## ") {
		t.Errorf("markdown markup left in: %s", plain)
	}
}

func TestCommand(t *testing.T) {
	t.Setenv("PWD", "/somewhere/else")
	for _, tc := range []struct {
		a       Agent
		env     string // must be set
		arg     string // must be passed
		without string // must not be passed
	}{
		{Agent{Name: "claude", CLI: "claude"}, "PWD=/work/tree", "--session-id", "--model"},
		{Agent{Name: "opencode", CLI: "opencode", Model: "p/m", Effort: "max"}, "PWD=/work/tree", "p/m#max", "--standalone"},
		{Agent{Name: "work", CLI: "opencode", Data: "/data/work.db"}, "OPENCODE_DB=/data/work.db", "--standalone", "--model"},
		{Agent{Name: "claude-2", CLI: "claude", Data: "/cfg/two", Model: "opus"}, "CLAUDE_CONFIG_DIR=/cfg/two", "opus", "--standalone"},
	} {
		cmd := command(tc.a, cliOf(tc.a).NewSession(), true, "hi", "", "/work/tree", false)
		env := cmd.Environ()
		if !slices.Contains(env, tc.env) || slices.Contains(env, "PWD=/somewhere/else") {
			t.Errorf("%s: env lacks %s or kept a stale PWD", tc.a.Name, tc.env)
		}
		if cmd.Args[0] != tc.a.CLI || !slices.Contains(cmd.Args, tc.arg) || slices.Contains(cmd.Args, tc.without) {
			t.Errorf("%s: args %v, want %s and not %s", tc.a.Name, cmd.Args, tc.arg, tc.without)
		}
		// the subcommand comes first: opencode ignores flags before "run" and prints its help
		if want := map[string]string{"claude": "-p", "opencode": "run"}[tc.a.CLI]; cmd.Args[1] != want {
			t.Errorf("%s: args %v, want %q right after the CLI", tc.a.Name, cmd.Args, want)
		}
	}
}

func TestConfigCheck(t *testing.T) {
	ok := Config{Lead: Lead{CLI: "claude"}, Agents: []Agent{{Name: "claude", CLI: "claude"}, {Name: "work", CLI: "opencode"}}}
	if e := ok.check(); e != "" {
		t.Errorf("valid config: %s", e)
	}
	for _, bad := range []Config{
		{Lead: Lead{CLI: "gpt"}, Agents: ok.Agents},
		{Lead: Lead{CLI: "claude"}, Agents: append(slices.Clone(ok.Agents), Agent{Name: "work", CLI: "claude"})},
		{Lead: Lead{CLI: "claude"}, Agents: append(slices.Clone(ok.Agents), Agent{Name: "Bad Name", CLI: "claude"})},
	} {
		if bad.check() == "" {
			t.Errorf("%+v passed the check", bad)
		}
	}
}

// onPath makes PATH hold only stand-ins for these CLIs, so tests don't depend on what's installed.
func onPath(t *testing.T, names ...string) {
	dir := t.TempDir()
	for _, n := range names {
		if runtime.GOOS == "windows" {
			n += ".exe"
		}
		if err := os.WriteFile(filepath.Join(dir, n), nil, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("PATH", dir)
}

// A lead on a CLI this machine doesn't have can't be saved, and opens the setup instead of failing every turn.
func TestLeadCLIMissing(t *testing.T) {
	onPath(t, "claude")
	c := Config{Lead: Lead{CLI: "codex"}, Agents: []Agent{{Name: "claude", CLI: "claude"}}}
	if c.check() != "" || !strings.Contains(c.missing(), "codex") {
		t.Errorf("check %q, missing %q: want a valid config whose lead is missing", c.check(), c.missing())
	}
}

// Editing an agent in the setup must not touch the lead, or recolor anything.
func TestSetupEditsLeaveLeadAlone(t *testing.T) {
	cfg := Config{Lead: Lead{CLI: "claude", Model: "opus"},
		Agents: []Agent{{Name: "claude", CLI: "claude"}, {Name: "work", CLI: "opencode"}}}
	setColors(cfg)
	before := agentColors["work"]
	m := &model{cfg: cfg, setup: &setupForm{cfg: cfg}}
	fs := m.setup.fields()
	nameRow := slices.IndexFunc(fs, func(f field) bool { return f.head })
	m.setup.row = nameRow
	m.setupKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("x")}) // rename the first agent
	m.setup.row = nameRow + 1
	m.setupKey(tea.KeyMsg{Type: tea.KeySpace, Runes: []rune(" ")}) // and switch its cli
	got := m.setup.cfg
	if got.Agents[0].Name != "claudex" || got.Agents[0].CLI != "opencode" {
		t.Fatalf("the edits didn't land: %+v", got.Agents[0])
	}
	if got.Lead != cfg.Lead {
		t.Errorf("editing an agent changed the lead: %+v", got.Lead)
	}
	if agentColors["work"] != before {
		t.Errorf("editing the first agent recolored the second")
	}
}

func TestDescribe(t *testing.T) {
	for a, want := range map[Agent]string{
		{Name: "work", CLI: "opencode", Model: "opencode-go/x", Effort: "max"}: "work opencode-go/x#max",
		{Name: "claude", CLI: "claude", Model: "sonnet", Effort: "high"}:       "claude sonnet high",
		{Name: "opencode", CLI: "opencode"}:                                    "opencode default model",
	} {
		if got := describe(a); got != want {
			t.Errorf("describe(%v) = %q, want %q", a, got, want)
		}
	}
}

// A saved run comes back with its sessions, transcripts and totals, and is found from its directory.
func TestSaveAndResume(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	runDir := filepath.Join(stateDir(), "20260101-120000")
	os.MkdirAll(runDir, 0o755)
	lead := &agentState{name: "lead", dir: "/repo", session: "uuid-1", cfg: Agent{Name: "claude", CLI: "claude"}, turns: 2,
		lines: []Line{{Kind: "you", Text: "add search"}}, cost: 0.5, in: 100, out: 20}
	worker := &agentState{name: "search", dir: "/repo-aw-search", session: "ses_x", cfg: Agent{Name: "work", CLI: "opencode", Data: "~/w.db"},
		turns: 1, running: true, lines: []Line{{Kind: "tool", Text: "edit", Arg: "todo.py"}}}
	m := &model{cwd: "/repo", runDir: runDir, lead: lead, workers: []*agentState{worker}, notes: []string{"note"}, queue: []string{"queued"}}
	m.saveRun()

	if runs := runsIn("/repo"); len(runs) != 1 || runs[0] != runDir {
		t.Fatalf("runsIn = %v, want [%s]", runs, runDir)
	}
	if runs := runsIn("/elsewhere"); len(runs) != 0 {
		t.Errorf("runsIn(/elsewhere) = %v, want none", runs)
	}
	r, err := loadRun(runDir)
	if err != nil {
		t.Fatal(err)
	}
	l, w := r.Lead.restore(), r.Agents[0].restore()
	if l.session != "uuid-1" || l.turns != 2 || l.cost != 0.5 || l.lines[0].Text != "add search" || l.running {
		t.Errorf("lead restored as %+v", l)
	}
	if w.session != "ses_x" || w.cfg.Data != "~/w.db" || w.lines[0].Arg != "todo.py" || w.running || w.stopReason == "" {
		t.Errorf("agent restored as %+v; a turn cut off by closing must show as interrupted", w)
	}
	if !slices.Equal(r.Notes, []string{"note", "queued"}) {
		t.Errorf("notes %v, want the lead's untold note and queued message", r.Notes)
	}
}

// A message for a mid-turn agent waits for its turn to end, then starts the next one.
func TestSendToRunningAgentIsQueued(t *testing.T) {
	bin := t.TempDir()
	os.WriteFile(filepath.Join(bin, "opencode"), []byte("#!/bin/sh\nexit 0\n"), 0o755) // a fake agent CLI
	t.Setenv("PATH", bin)
	m := &model{runDir: t.TempDir(), lead: &agentState{name: "lead", running: true}} // a busy lead only queues
	prog = tea.NewProgram(m)
	a := &agentState{name: "search", dir: t.TempDir(), cfg: Agent{Name: "work", CLI: "opencode"}, session: "ses_x",
		turns: 1, running: true, started: time.Now()}
	m.workers = []*agentState{a}

	if ans := m.handle(request{Op: "send", Name: "search", Prompt: "main moved, rebase"}); !strings.HasPrefix(ans, "ok") || len(a.pending) != 1 {
		t.Fatalf("send to a running agent: %q, pending %v", ans, a.pending)
	}
	m.finished(a, 0)
	if !a.running || a.turns != 2 || len(a.pending) != 0 || !strings.Contains(a.lines[len(a.lines)-1].Text, "main moved, rebase") {
		t.Errorf("the queued message didn't start turn 2: running=%v turns=%d pending=%v", a.running, a.turns, a.pending)
	}
	if last := m.queue[len(m.queue)-1]; !strings.Contains(last, "were delivered") {
		t.Errorf("the lead wasn't told: %s", last)
	}

	a.pending = []string{"one more thing"} // and a turn that gets stopped keeps it from being sent
	a.stopReason = "stopped by the user"
	m.finished(a, -1)
	if a.running || len(a.pending) != 0 || !strings.Contains(m.queue[len(m.queue)-1], "did NOT get") {
		t.Errorf("a stopped turn must not deliver queued messages: running=%v, lead got %q", a.running, m.queue[len(m.queue)-1])
	}
}

// Typing on a model row searches its list without touching the model; choosing sets it and drops an effort it lacks.
func TestModelPicker(t *testing.T) {
	cfg := Config{Lead: Lead{CLI: "claude"}, Agents: []Agent{{Name: "work", CLI: "opencode", Model: "old/model", Effort: "xhigh"}}}
	known := map[string]modelList{accountKey(cfg.Agents[0]): {list: []modelInfo{
		{ID: "opencode-go/deepseek-v4.1-flash", Price: "$0.15/$0.6", Variants: []string{"low", "high", "max"}},
		{ID: "opencode/deepseek-v4.1-flash-free", Price: "free", Variants: []string{"low", "high", "max"}},
		{ID: "opencode/glm-5.3-flash-free", Price: "free", Variants: nil},
	}}}
	m := &model{cfg: cfg, setup: &setupForm{cfg: cfg, known: known}}
	f := m.setup
	f.row = slices.IndexFunc(f.fields(), func(fl field) bool { return fl.label == "model" && fl.agent == 0 })
	key := func(s string) {
		for _, r := range s {
			if r == ' ' {
				m.setupKey(tea.KeyMsg{Type: tea.KeySpace, Runes: []rune{r}})
			} else {
				m.setupKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
			}
		}
	}
	key("deep free")
	cur := f.fields()[f.row]
	if found := f.found(cur); !f.open || len(found) != 1 || found[0].ID != "opencode/deepseek-v4.1-flash-free" {
		t.Fatalf("open=%v found %v, want just the free deepseek", f.open, found)
	}
	if f.cfg.Agents[0].Model != "old/model" {
		t.Errorf("searching changed the model to %q", f.cfg.Agents[0].Model)
	}
	m.setupKey(tea.KeyMsg{Type: tea.KeyEnter})
	a := f.cfg.Agents[0]
	if f.open || a.Model != "opencode/deepseek-v4.1-flash-free" || a.Effort != "" {
		t.Errorf("after choosing: open=%v model=%q effort=%q; want the model set and xhigh (which it lacks) cleared", f.open, a.Model, a.Effort)
	}
	if got := f.effortChoices(a); !slices.Equal(got, []string{"", "low", "high", "max"}) {
		t.Errorf("effort choices %v, want the model's own", got)
	}
	key("x") // esc closes only the picker
	m.setupKey(tea.KeyMsg{Type: tea.KeyEsc})
	if f.open || m.setup == nil {
		t.Errorf("esc should close the picker and keep the setup open")
	}
	m.setupKey(tea.KeyMsg{Type: tea.KeyBackspace}) // backspace lists everything; the first entry is "default"
	m.setupKey(tea.KeyMsg{Type: tea.KeyEnter})
	if f.cfg.Agents[0].Model != "" {
		t.Errorf("choosing default left the model %q", f.cfg.Agents[0].Model)
	}
	key("my/own-model") // a model that isn't listed is kept as typed
	m.setupKey(tea.KeyMsg{Type: tea.KeyEnter})
	if f.cfg.Agents[0].Model != "my/own-model" {
		t.Errorf("an unlisted model became %q", f.cfg.Agents[0].Model)
	}
}

// A project file overrides lead fields it sets and agents by name, and saving the setup never writes it into the global file.
func TestProjectOverrides(t *testing.T) {
	global := Config{Lead: Lead{CLI: "claude", Model: "opus", Effort: "high"},
		Agents: []Agent{{Name: "ds3", CLI: "opencode", Model: "a/ds", Enabled: true}, {Name: "haiku", CLI: "claude", Model: "haiku"}}}
	project := Config{Lead: Lead{Model: "sonnet"},
		Agents: []Agent{{Name: "ds3", CLI: "opencode", Model: "a/ds-pro", Enabled: true}, {Name: "local", CLI: "opencode", Model: "x/y", Enabled: true}}}
	c := withProject(global, project)
	if c.Lead != (Lead{CLI: "claude", Model: "sonnet", Effort: "high"}) {
		t.Errorf("lead %+v, want the project's model on the global lead", c.Lead)
	}
	if len(c.Agents) != 3 || c.Agents[0].Model != "a/ds-pro" || c.Agents[1].Name != "haiku" || c.Agents[2].Name != "local" {
		t.Errorf("agents %+v, want ds3 replaced, haiku kept, local added", c.Agents)
	}
	if global.Agents[0].Model != "a/ds" {
		t.Errorf("withProject changed the global config")
	}
	if got := overrides(project); got != "lead model · agents ds3, local" {
		t.Errorf("overrides = %q", got)
	}

	root := t.TempDir()
	sub := filepath.Join(root, "src", "deep")
	os.MkdirAll(sub, 0o755)
	if findProject(sub) != "" {
		t.Errorf("found a project file where there is none")
	}
	os.WriteFile(filepath.Join(root, projectFile), []byte(`{"lead": {"model": "sonnet"}}`), 0o644)
	if p := findProject(sub); p != filepath.Join(root, projectFile) {
		t.Errorf("findProject from a subfolder = %q", p)
	}
	os.WriteFile(filepath.Join(root, projectFile), []byte(`{"lead": {`), 0o644)
	if _, err := loadProject(filepath.Join(root, projectFile)); err == nil {
		t.Errorf("a broken project file loaded without an error")
	}

	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	m := &model{cfg: c, global: global, project: project, projectPath: "/p/.agent-watch.json", lead: &agentState{name: "lead", cfg: c.lead()}}
	m.setup = m.newSetup()
	if !strings.Contains(m.setup.note, "agents ds3, local") {
		t.Errorf("the setup doesn't say what the project overrides: %q", m.setup.note)
	}
	m.setupKey(tea.KeyMsg{Type: tea.KeyEnter})
	saved, _ := loadConfig()
	if len(saved.Agents) != 2 || saved.Agents[0].Model != "a/ds" || saved.Lead.Model != "opus" {
		t.Errorf("the project's overrides leaked into the global config: %+v", saved)
	}
	if len(m.cfg.Agents) != 3 {
		t.Errorf("after saving, the running config lost the project's agents: %+v", m.cfg.Agents)
	}
}

// Opening and cancelling the setup keeps the colors of agents that only the project file has.
func TestSetupKeepsProjectColors(t *testing.T) {
	global := Config{Lead: Lead{CLI: "claude"}, Agents: []Agent{{Name: "claude", CLI: "claude"}}}
	project := Config{Agents: []Agent{{Name: "ds3", CLI: "opencode", Enabled: true}}}
	m := &model{global: global, project: project, cfg: withProject(global, project)}
	setColors(m.cfg)
	want := agentColors["ds3"]
	m.setup = m.newSetup()
	m.setupKey(tea.KeyMsg{Type: tea.KeyDown})
	if agentColors["ds3"] != want {
		t.Errorf("moving around the setup recolored ds3")
	}
	m.setupKey(tea.KeyMsg{Type: tea.KeyEsc})
	if _, ok := agentColors["ds3"]; !ok || agentColors["ds3"] != want {
		t.Errorf("cancelling the setup lost ds3's color")
	}
}

// The lead only gets edit tools, and the rule that comes with them, when the setup allows it.
func TestLeadMayEdit(t *testing.T) {
	for _, edits := range []bool{false, true} {
		claude := strings.Join(command(Agent{CLI: "claude"}, "u", true, "hi", "rules", "/w", edits).Args, " ")
		opencode := strings.Join(command(Agent{CLI: "opencode"}, "ses_x", true, "hi", "rules", "/w", edits).Args, " ")
		if strings.Contains(claude, "Edit Write") != edits || strings.Contains(opencode, "--agent plan") == edits {
			t.Errorf("edits=%v: claude %q, opencode %q", edits, claude, opencode)
		}
		m := &model{cfg: Config{Lead: Lead{CLI: "claude", Edit: edits}}}
		if p := m.systemPrompt(); strings.Contains(p, "You may edit files") != edits || strings.Contains(p, "Never edit project files") == edits {
			t.Errorf("edits=%v: the lead's instructions have the wrong rule", edits)
		}
	}
	// agents always edit: the toggle is only about the lead
	if a := strings.Join(command(Agent{CLI: "claude"}, "u", true, "hi", "", "/w", false).Args, " "); !strings.Contains(a, "bypassPermissions") {
		t.Errorf("an agent lost its permissions: %q", a)
	}
}

func TestRelative(t *testing.T) {
	for in, want := range map[string]string{
		"/w/repo/a.go":                          "a.go",
		"cd /w/repo-aw-x && git diff":           "cd ../repo-aw-x && git diff",
		"/w/repo":                               "../repo",
		"grep -r x /etc/hosts /w/repo/src/m.go": "grep -r x /etc/hosts src/m.go",
	} {
		if got := relative(in, "/w/repo"); got != want {
			t.Errorf("relative(%q) = %q, want %q", in, got, want)
		}
	}
}

// An agent whose output shows nothing for stallAfter is stopped, not left to the turn timeout.
func TestStalledAgentIsStopped(t *testing.T) {
	m := &model{runDir: t.TempDir(), lead: &agentState{name: "lead"}, spendAt: time.Now()}
	sleep := exec.Command("sleep", "60")
	ownGroup(sleep)
	if err := sleep.Start(); err != nil {
		t.Fatal(err)
	}
	quiet := &agentState{name: "quiet", cfg: Agent{CLI: "claude"}, cmd: sleep, running: true, heard: time.Now().Add(-stallAfter - time.Second)}
	busy := &agentState{name: "busy", cfg: Agent{CLI: "claude"}, running: true, heard: time.Now()}
	m.workers = []*agentState{quiet, busy}
	m.Update(tickMsg{})
	if !strings.HasPrefix(quiet.stopReason, "stalled") || busy.stopReason != "" {
		t.Errorf("stop reasons: quiet %q, busy %q", quiet.stopReason, busy.stopReason)
	}
	if err := sleep.Wait(); err == nil {
		t.Error("the stalled agent's process is still running")
	}
}

// d removes a finished agent from the list and tells the lead; a working one stays.
func TestRemoveAgent(t *testing.T) {
	m := &model{lead: &agentState{name: "lead"}}
	done := &agentState{name: "scout", dir: "/w/repo-aw-scout", turns: 1}
	busy := &agentState{name: "fixer", turns: 1, running: true}
	m.workers, m.focusList, m.sel, m.target = []*agentState{busy, done}, true, 1, done
	d := tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("d")}

	m.key(d)
	if len(m.workers) != 1 || m.workers[0] != busy || m.sel != 0 || m.target != nil {
		t.Fatalf("after d on scout: workers %d, sel %d, target %v", len(m.workers), m.sel, m.target)
	}
	if len(m.notes) != 1 || !strings.Contains(m.notes[0], `"scout"`) {
		t.Errorf("the lead isn't told: %q", m.notes)
	}
	m.key(d)
	if len(m.workers) != 1 || !strings.Contains(m.status, "still working") {
		t.Errorf("a working agent was removed, or no hint: %d agents, status %q", len(m.workers), m.status)
	}
	if m.handle(request{Op: "send", Name: "scout", Prompt: "hi"}) != "error: no agent named scout" {
		t.Error("the removed agent can still be messaged")
	}
}

// codex and agy start sessions themselves and resume them by the id they announced; the prompt goes last.
func TestCommandCodexAgy(t *testing.T) {
	for _, tc := range []struct {
		a             Agent
		session, want string
	}{
		{Agent{CLI: "codex", Model: "gpt-x", Effort: "high"}, "", `codex exec --json --skip-git-repo-check --dangerously-bypass-approvals-and-sandbox -m gpt-x -c model_reasoning_effort="high" hi`},
		{Agent{CLI: "codex"}, "th1", "codex exec resume --json --skip-git-repo-check --dangerously-bypass-approvals-and-sandbox th1 hi"},
		{Agent{CLI: "codex", Effort: `x"; evil="1`}, "", "codex exec --json --skip-git-repo-check --dangerously-bypass-approvals-and-sandbox hi"},
		{Agent{CLI: "agy", Model: "gemini-x", Effort: "low"}, "", "agy --output-format stream-json --dangerously-skip-permissions --model gemini-x --effort low -p hi"},
		{Agent{CLI: "agy"}, "c1", "agy --output-format stream-json --dangerously-skip-permissions --conversation c1 -p hi"},
	} {
		if got := strings.Join(command(tc.a, tc.session, tc.session == "", "hi", "", "/w", false).Args, " "); got != tc.want {
			t.Errorf("got  %s\nwant %s", got, tc.want)
		}
	}
	if (codex.CLI{}).NewSession() != "" || (agy.CLI{}).NewSession() != "" {
		t.Errorf("codex and agy must start without a session id: they assign their own")
	}
	if e := (Config{Lead: Lead{CLI: "agy", Data: "/x"}, Agents: []Agent{{Name: "a", CLI: "claude"}}}).check(); e == "" {
		t.Errorf("an agy lead on another account passed the check")
	}
}

// The header shows the lead's account always, and the selected agent's only when it's another one.
func TestUsageLine(t *testing.T) {
	lead := &agentState{name: "lead", cfg: Agent{Name: "claude", CLI: "claude"}}
	same := &agentState{name: "a", cfg: Agent{Name: "claude", CLI: "claude"}}
	gem := &agentState{name: "b", cfg: Agent{Name: "gm", CLI: "agy"}, in: 1200, out: 300}
	m := &model{lead: lead, workers: []*agentState{same, gem}, limits: map[string]*Limits{
		accountKey(lead.cfg): {Windows: []Window{{Label: "5h", Used: 0.5}, {Label: "7d", Used: 0.25}}}}}
	plain := xansi.Strip(m.usageLine())
	if !strings.Contains(plain, "lead 5h") || strings.Contains(plain, "│") {
		t.Errorf("same account as the lead: %q, want just the lead's", plain)
	}
	m.sel = 1
	plain = xansi.Strip(m.usageLine())
	if !strings.Contains(plain, "lead 5h") || !strings.Contains(plain, "gm ↑ 1.2k ↓ 300 this run") {
		t.Errorf("agy agent selected: %q, want the lead's limits and gm's tokens", plain)
	}
}

// Enter on an empty input while the lead is busy and a message is queued interrupts the lead to send it now.
func TestSendNow(t *testing.T) {
	cmd := exec.Command("sleep", "30")
	ownGroup(cmd)
	if err := cmd.Start(); err != nil {
		t.Skip("no sleep command:", err)
	}
	defer cmd.Process.Kill()
	m := &model{lead: &agentState{name: "lead", running: true, cmd: cmd}, queue: []string{"stop, use the other API"}}
	m.submit()
	if m.lead.stopReason == "" {
		t.Errorf("an empty enter with a queued message didn't interrupt the lead")
	}
	m2 := &model{lead: &agentState{name: "lead", running: true}}
	m2.submit()
	if m2.lead.stopReason != "" {
		t.Errorf("an empty enter with nothing queued interrupted the lead")
	}
}

func TestDur(t *testing.T) {
	for d, want := range map[time.Duration]string{
		12 * time.Second: "12s", 90 * time.Second: "1m 30s", 90 * time.Minute: "1h 30m", 716*time.Hour + 50*time.Minute: "29d 20h",
	} {
		if got := dur(d); got != want {
			t.Errorf("dur(%s) = %q, want %q", d, got, want)
		}
	}
}

// Changing the lead's model or effort keeps its conversation; only another CLI or account starts a new one.
func TestLeadChangeKeepsConversation(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	cfg := Config{Lead: Lead{CLI: "claude", Model: "opus", Effort: "high"}, Agents: []Agent{{Name: "claude", CLI: "claude"}}}
	m := &model{cfg: cfg, global: cfg, lead: &agentState{name: "lead", cfg: cfg.lead(), session: "uuid-1", turns: 3,
		lines: []Line{{Kind: "you", Text: "earlier"}}}}
	onPath(t, "claude", "codex")
	save := func(edit func(*Config)) {
		m.setup = m.newSetup()
		edit(&m.setup.cfg)
		m.setupKey(tea.KeyMsg{Type: tea.KeyEnter})
	}
	save(func(c *Config) { c.Lead.Effort, c.Lead.Model = "low", "sonnet" })
	if m.lead.session != "uuid-1" || m.lead.turns != 3 || m.lead.lines[0].Text != "earlier" || m.lead.cfg.Model != "sonnet" || m.lead.cfg.Effort != "low" {
		t.Errorf("model/effort change: session %q turns %d model %q effort %q", m.lead.session, m.lead.turns, m.lead.cfg.Model, m.lead.cfg.Effort)
	}
	save(func(c *Config) { c.Lead.CLI = "codex" })
	if m.lead.session == "uuid-1" || m.lead.cfg.CLI != "codex" {
		t.Errorf("switching CLI must start a new session: %+v", m.lead)
	}
}

// agent-watch's own slash commands change the lead for the run; others go to the lead first in its prompt.
func TestSlashCommands(t *testing.T) {
	m := &model{lead: &agentState{name: "lead", cfg: Agent{Name: "claude", CLI: "claude", Model: "opus"}, session: "s1", lines: []Line{{Kind: "you", Text: "old"}}}}
	if !m.command("/model sonnet") || m.lead.cfg.Model != "sonnet" || m.lead.session != "s1" {
		t.Errorf("/model: %+v", m.lead.cfg)
	}
	if !m.command("/effort high") || m.lead.cfg.Effort != "high" {
		t.Errorf("/effort high: %q", m.lead.cfg.Effort)
	}
	if !m.command("/effort turbo") || m.lead.cfg.Effort != "high" {
		t.Errorf("/effort with an unknown level changed it to %q", m.lead.cfg.Effort)
	}
	if !m.command("/clear") || m.lead.session == "s1" || m.lead.lines[0].Text == "old" || m.lead.cfg.Model != "sonnet" {
		t.Errorf("/clear should start a new conversation with the same settings: %+v", m.lead)
	}
	if m.command("/ponytail-help") {
		t.Errorf("a skill was taken as agent-watch's command")
	}
}

// The account row is a choice of the logins found, only for a CLI that can use another; changing the cli
// drops the account, which belonged to the old one.
func TestSetupAccounts(t *testing.T) {
	cfg := Config{Lead: Lead{CLI: "claude"}, Agents: []Agent{
		{Name: "oc", CLI: "opencode", Data: "~/old.db"}, {Name: "gm", CLI: "agy"}}}
	f := &setupForm{cfg: cfg, accounts: map[string][]string{"opencode": {"~/work.db"}}}
	rows := map[string][]field{}
	for _, fl := range f.fields() {
		if fl.agent >= 0 {
			rows[f.cfg.Agents[fl.agent].Name] = append(rows[f.cfg.Agents[fl.agent].Name], fl)
		}
	}
	labels := func(fs []field) (l []string) {
		for _, fl := range fs {
			l = append(l, fl.label)
		}
		return l
	}
	if slices.Contains(labels(rows["gm"]), "account") || !slices.Contains(labels(rows["oc"]), "account") {
		t.Fatalf("account rows: oc %v, gm %v", labels(rows["oc"]), labels(rows["gm"]))
	}
	acct := rows["oc"][slices.Index(labels(rows["oc"]), "account")]
	if !slices.Equal(acct.choices, []string{"", "~/work.db", "~/old.db"}) {
		t.Errorf("account choices %q, want default, the one found, and the one it has", acct.choices)
	}

	m := &model{global: cfg, cfg: cfg, lead: &agentState{name: "lead", cfg: cfg.lead()}, setup: f}
	f.row = slices.IndexFunc(f.fields(), func(fl field) bool { return fl.agent == 0 && fl.label == "cli" })
	m.setupKey(tea.KeyMsg{Type: tea.KeyRight})
	if a := f.cfg.Agents[0]; a.CLI != "codex" || a.Data != "" {
		t.Errorf("after changing the cli: %+v, want codex on its default account", a)
	}
	if e := (Config{Lead: Lead{CLI: "claude"}, Agents: []Agent{{Name: "gm", CLI: "agy", Data: "/x"}}}).check(); e == "" {
		t.Error("an agy agent on another account passed the check")
	}
}
