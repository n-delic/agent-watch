package main

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/textinput"
	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	xansi "github.com/charmbracelet/x/ansi"
)

type model struct {
	cfg              Config // what runs: the global setup with the project's overrides on top
	global           Config // what the setup screen edits and saves
	project          Config // the project file's overrides
	projectPath      string
	runDir, cwd, exe string
	lead             *agentState
	workers          []*agentState
	sel              int
	queue            []string    // messages for the lead's next turn
	notes            []string    // told to the lead along with its next message
	focusList        bool        // keys go to the agent list instead of the input
	target           *agentState // the agent the input is messaging (m); nil = the lead
	full             bool        // selected agent fullscreen
	selecting        bool        // the mouse is left to the terminal, so text can be selected and copied
	quitArmed        bool
	setup            *setupForm
	input            textinput.Model
	images           []string // clipboard images of this session, shown as [Image #n] in the input
	mvp, svp         viewport.Model
	w, h             int
	status           string
	limits           map[string]*Limits // per account (accountKey): its usage windows, from its latest report
	spend            map[string]spend
	spendAt          time.Time
	models           map[string]modelList // per account, for the setup's model picker
	dirty            bool                 // the run changed since it was last saved
	savedAt          time.Time
}

type tickMsg struct{}

func tick() tea.Cmd {
	return tea.Tick(120*time.Millisecond, func(time.Time) tea.Msg { return tickMsg{} })
}

// runTUI opens a new run, or continues the saved one in resume.
func runTUI(resume string) error {
	global, saved := loadConfig()
	cwd, _ := os.Getwd()
	var project Config
	projectPath := findProject(cwd)
	if projectPath != "" {
		var err error
		if project, err = loadProject(projectPath); err != nil {
			return fmt.Errorf("%s: %w", projectPath, err)
		}
	}
	cfg := withProject(global, project)
	if e := cfg.check(); e != "" {
		return fmt.Errorf("%s: %s", projectPath, e)
	}
	setColors(cfg)
	exe, _ := os.Executable()
	runDir := filepath.Join(stateDir(), time.Now().Format("20060102-150405"))
	if resume != "" {
		runDir = resume
	}
	if err := os.MkdirAll(filepath.Join(runDir, "requests"), 0o755); err != nil {
		return err
	}
	in := textinput.New()
	in.Prompt = sAccent.Render("› ")
	in.PlaceholderStyle = sDim
	in.Cursor.Style = sAccent
	in.Placeholder = "message the lead…"
	in.Focus()
	m := &model{cfg: cfg, global: global, project: project, projectPath: projectPath, runDir: runDir, cwd: cwd, exe: exe, input: in, spend: map[string]spend{}, limits: map[string]*Limits{}, models: map[string]modelList{},
		lead: &agentState{name: "lead", dir: cwd, cfg: cfg.lead(), session: cliOf(cfg.lead()).NewSession()}}
	if resume != "" {
		r, err := loadRun(resume)
		if err != nil {
			return err
		}
		m.lead, m.notes = r.Lead.restore(), r.Notes
		for _, a := range r.Agents {
			m.workers = append(m.workers, a.restore())
		}
		m.lead.lines = append(m.lead.lines, Line{Kind: "info", Text: "resumed the run from " + r.Saved.Format("Jan 02 15:04")})
	}
	if missing := cfg.missing(); !saved || missing != "" {
		m.setup = m.newSetup()
		m.setup.err = missing
	}
	prog = tea.NewProgram(m, tea.WithAltScreen(), tea.WithMouseCellMotion())
	_, err := prog.Run()
	return err
}

func (m *model) Init() tea.Cmd {
	if m.setup != nil {
		return tea.Batch(tick(), m.refreshSpend(), m.prefetchModels())
	}
	return tea.Batch(tick(), m.refreshSpend())
}

// prefetchModels loads every opencode account's models in the background, so the setup's picker is ready.
func (m *model) prefetchModels() tea.Cmd {
	f := m.setup
	cmds := []tea.Cmd{f.fetch(f.cfg.lead())}
	for _, a := range f.cfg.Agents {
		cmds = append(cmds, f.fetch(a))
	}
	return tea.Batch(cmds...)
}

func (m *model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.w, m.h = msg.Width, msg.Height
		m.layout()
	case tickMsg:
		if m.dirty && time.Since(m.savedAt) > 2*time.Second {
			m.saveRun()
		}
		if m.pollRequests() {
			m.refresh()
		}
		for _, a := range m.workers {
			if a.running && a.stopReason == "" && time.Since(a.heard) > stallAfter {
				a.stop("stalled: no output for " + dur(stallAfter))
			}
		}
		if time.Since(m.spendAt) > 5*time.Minute {
			return m, tea.Batch(tick(), m.refreshSpend())
		}
		return m, tick()
	case linesMsg:
		m.dirty = true
		a, d := msg.a, msg.d
		a.heard = time.Now()
		a.lines = append(a.lines, msg.lines...)
		a.cost, a.turnCost = a.cost+d.Cost, a.turnCost+d.Cost
		a.in, a.out, a.turnIn, a.turnOut = a.in+d.In, a.out+d.Out, a.turnIn+d.In, a.turnOut+d.Out
		if d.Session != "" {
			a.session = d.Session // codex and agy name their session in their first turn
		}
		if d.Limits != nil {
			m.limits[accountKey(a.cfg)] = d.Limits
		}
		for _, l := range msg.lines {
			switch l.Kind {
			case "text":
				a.lastText = l.Text
			case "err":
				a.lastErr = l.Text
			}
		}
		m.refresh()
	case doneMsg:
		m.dirty = true
		m.finished(msg.a, msg.code)
		m.refresh()
		return m, afterTurn(msg.a)
	case limitsMsg:
		if msg.l != nil {
			m.limits[msg.account] = msg.l
		}
	case modelsMsg:
		m.models[msg.account] = modelList{list: msg.list, err: msg.err}
	case spendMsg:
		if msg.ok {
			m.spend[msg.account] = msg.s
		}
	case stopMsg:
		msg.a.stop(msg.reason)
	case tea.MouseMsg:
		return m.mouse(msg)
	case tea.KeyMsg:
		return m.key(msg)
	}
	return m, nil
}

// turnSummary is "1m 12s · ↑ 23.4k ↓ 1.2k · $0.004" for the last turn.
func turnSummary(a *agentState) string {
	return fmt.Sprintf("%s · ↑ %s ↓ %s · $%.3f", dur(a.lastDur), tokens(a.turnIn), tokens(a.turnOut), a.turnCost)
}

func (m *model) finished(a *agentState, code int) {
	a.running, a.exit = false, code
	a.lastDur = time.Since(a.started)
	a.totalDur += a.lastDur
	if a.status() == "finished" {
		a.lines = append(a.lines, Line{Kind: "done", Text: fmt.Sprintf("turn %d done in %s", a.turns, turnSummary(a))})
	} else {
		a.lines = append(a.lines, Line{Kind: "fail", Text: fmt.Sprintf("turn %d %s · %s", a.turns, a.status(), turnSummary(a))})
	}
	if a == m.lead {
		m.flushLead()
		return
	}
	reply := clip(a.lastText, 8000)
	if reply == "" {
		reply = "(no reply)"
	}
	m.lead.lines = append(m.lead.lines, Line{Kind: "info", Text: fmt.Sprintf("agent %s %s turn %d in %s", a.name, a.status(), a.turns, dur(a.lastDur))})
	report := fmt.Sprintf("[agent-watch] Agent %q (%s) %s on turn %d after %s.\nWorkdir: %s\nIts final reply:\n%s",
		a.name, describe(a.cfg), a.status(), a.turns, dur(a.lastDur), a.dir, reply)
	if n := len(a.pending); n > 0 {
		text := strings.Join(a.pending, "\n\n")
		a.pending = nil
		if a.status() != "finished" {
			// A stopped or failed turn doesn't get them: whoever stopped it should decide again.
			report += fmt.Sprintf("\n\nIt did NOT get the %d message(s) queued for it, because its turn didn't finish. Send them again if they still apply.", n)
		} else {
			a.lines = append(a.lines, Line{Kind: "you", Text: text})
			if err := a.start(text, "", nil, m.logPath(a)); err != nil {
				report += "\n\nIts queued message(s) could not be delivered: " + err.Error()
			} else {
				report += fmt.Sprintf("\n\nIts %d queued message(s) were delivered: it's working on turn %d now.", n, a.turns)
			}
		}
	}
	m.toLead(report)
}

func (m *model) toLead(text string) {
	m.queue = append(m.queue, text)
	m.flushLead()
}

// flushLead starts the lead's next turn with everything queued, unless it is busy.
func (m *model) flushLead() {
	if m.lead.running || len(m.queue) == 0 {
		return
	}
	var first, rest []string // a slash command (a skill, a custom command) only works at the very start of the prompt
	for _, s := range append(m.notes, m.queue...) {
		if strings.HasPrefix(s, "/") {
			first = append(first, s)
		} else {
			rest = append(rest, s)
		}
	}
	msg := strings.Join(append(first, rest...), "\n\n")
	m.notes, m.queue = nil, nil
	m.lead.edits = m.cfg.Lead.Edit
	if err := m.lead.start(msg, m.systemPrompt(), []string{"AGENT_WATCH_DIR=" + m.runDir}, m.logPath(m.lead)); err != nil {
		m.lead.lines = append(m.lead.lines, Line{Kind: "fail", Text: "could not start the lead: " + err.Error()})
	}
}

const (
	noEditRule = "**Never edit project files yourself.** The user hasn't allowed it, and depending on your CLI you may have no edit tools at all. If an agent's work is wrong, that agent fixes it. Only the user can change this rule (in the setup)."
	editRule   = "**You may edit files, but keep it small.** The user lets you make fixes of a line or two that a feedback round isn't worth, resolve merge conflicts, and do housekeeping (docs, config). Feature work and anything substantial still goes to agents, and when an agent's work is wrong, it fixes it: give it feedback instead of fixing it yourself. Say in your report what you edited."
)

func (m *model) systemPrompt() string {
	var s []string
	for _, a := range m.cfg.Agents {
		if a.Enabled {
			s = append(s, "- "+a.Name+" ("+a.CLI+", runs "+strings.TrimPrefix(describe(a), a.Name+" ")+")")
		}
	}
	if len(s) == 0 {
		s = []string{"- none enabled: tell the user to enable agents in the setup (ctrl+s)"}
	}
	editing := noEditRule
	if m.cfg.Lead.Edit {
		editing = editRule
	}
	return strings.NewReplacer("{{EXE}}", m.exe, "{{CWD}}", m.cwd, "{{AGENTS}}", strings.Join(s, "\n"), "{{EDITING}}", editing).Replace(leadPrompt)
}

func (m *model) key(k tea.KeyMsg) (tea.Model, tea.Cmd) {
	if m.setup != nil {
		return m.setupKey(k)
	}
	if k.String() != "ctrl+c" {
		m.quitArmed = false
	}
	m.status = ""
	switch k.String() {
	case "ctrl+c":
		if m.running() && !m.quitArmed {
			m.quitArmed = true
			m.status = "agents are running: ctrl+c again stops them and quits"
			return m, nil
		}
		for _, a := range append(m.workers, m.lead) {
			a.stop("stopped: agent-watch quit")
		}
		if m.lead.turns == 0 && len(m.workers) == 0 {
			os.RemoveAll(m.runDir) // nothing happened: don't list an empty run in aw -r
		} else {
			m.saveRun()
		}
		return m, tea.Quit
	case "ctrl+s":
		m.setup = m.newSetup()
		return m, m.prefetchModels()
	case "ctrl+y":
		m.selecting = !m.selecting
		if m.selecting {
			return m, tea.DisableMouse
		}
		return m, tea.EnableMouseCellMotion
	case "tab":
		m.setFocusList(!m.focusList)
		return m, nil
	case "esc":
		m.target, m.full = nil, false
		m.setFocusList(false)
		m.layout()
		return m, nil
	case "pgup", "pgdown":
		vp := &m.mvp
		if m.focusList || m.full {
			vp = &m.svp
		}
		var cmd tea.Cmd
		*vp, cmd = vp.Update(k)
		return m, cmd
	}
	if m.focusList {
		s := m.workers[m.sel]
		switch k.String() {
		case "up", "k":
			m.selectWorker(m.sel - 1)
		case "down", "j":
			m.selectWorker(m.sel + 1)
		case "enter":
			m.full = !m.full
			m.layout()
		case "m":
			m.target = s
			m.setFocusList(false)
		case "x":
			if s.running {
				s.stop("stopped by the user")
			}
		case "d", "delete":
			if s.running {
				m.status = s.name + " is still working: x stops it, then d removes it"
			} else {
				m.remove(s)
			}
		}
		return m, nil
	}
	// Windows Terminal handles ctrl+v itself and sends nothing for an image, so alt+v is the reliable key.
	if key := k.String(); key == "ctrl+v" || key == "alt+v" {
		if path, ok := clipboardImage(filepath.Join(m.runDir, "images")); ok {
			m.images = append(m.images, path)
			m.input.SetValue(m.input.Value() + fmt.Sprintf("[Image #%d] ", len(m.images)))
			m.input.CursorEnd()
			m.status = "image attached: " + filepath.Base(path)
			return m, nil
		} else if key == "alt+v" {
			m.status = "no image on the clipboard"
			return m, nil
		}
	}
	if k.Type == tea.KeyEnter {
		return m, m.submit()
	}
	var cmd tea.Cmd
	m.input, cmd = m.input.Update(k)
	return m, cmd
}

// submit sends the input to the lead, or to the agent being messaged.
func (m *model) submit() tea.Cmd {
	text := strings.TrimSpace(m.input.Value())
	sent := m.expandImages(text)
	if text == "" {
		if m.target == nil && m.lead.running && len(m.queue) > 0 {
			m.lead.stop("interrupted to send your message now") // finished() then sends the queue as its next turn
		}
		return nil
	}
	if m.target == nil && strings.HasPrefix(text, "/") && m.command(text) {
		m.input.SetValue("")
		m.refresh()
		return nil
	}
	if t := m.target; t != nil {
		if t.running {
			m.hold(t, "the user", sent)
		} else {
			t.lines = append(t.lines, Line{Kind: "you", Text: text})
			if err := t.start(sent, "", nil, m.logPath(t)); err != nil {
				m.status = "could not start " + t.name + ": " + err.Error()
				return nil
			}
		}
		m.notes = append(m.notes, fmt.Sprintf("[agent-watch] The user sent agent %q this message directly:\n%s", t.name, sent))
		m.target = nil
		m.setFocusList(false)
	} else {
		m.lead.lines = append(m.lead.lines, Line{Kind: "you", Text: text})
		if m.lead.running {
			m.lead.lines = append(m.lead.lines, Line{Kind: "info", Text: "queued until the lead's turn ends · press enter again to interrupt it and send now"})
		}
		m.toLead(sent)
		m.dirty = true
	}
	m.input.SetValue("")
	m.refresh()
	return nil
}

// command runs agent-watch's own slash commands. The CLIs only see one turn at a time, so their /model or /clear
// would die with it. Anything else (a skill, a custom command) goes to the lead as typed.
func (m *model) command(text string) bool {
	name, arg, _ := strings.Cut(text, " ")
	arg = strings.TrimSpace(arg)
	info := func(s string) { m.lead.lines = append(m.lead.lines, Line{Kind: "info", Text: s}) }
	switch name {
	case "/model":
		if arg == "" {
			info("the lead runs " + describe(m.lead.cfg) + " · /model <name> changes it for this run")
			return true
		}
		m.lead.cfg.Model = arg
		if arg == "default" {
			m.lead.cfg.Model = ""
		}
		info("the lead now runs " + describe(m.lead.cfg) + " for this run; the setup (ctrl+s) keeps the default")
	case "/effort":
		effort := arg
		if arg == "default" {
			effort = ""
		}
		if levels := cliOf(m.lead.cfg).Efforts(); arg == "" || !slices.Contains(levels, effort) {
			info("/effort takes one of: default " + strings.Join(levels[1:], " "))
			return true
		}
		m.lead.cfg.Effort = effort
		info("the lead now runs " + describe(m.lead.cfg) + " for this run")
	case "/clear":
		if m.lead.running {
			info("the lead is mid-turn; /clear when it's done")
			return true
		}
		m.lead = &agentState{name: "lead", dir: m.cwd, cfg: m.lead.cfg, session: cliOf(m.lead.cfg).NewSession(),
			lines: []Line{{Kind: "info", Text: "a new conversation with the lead; the agents carry on"}}}
	case "/help":
		info("/model <name> · /effort <level> · /clear starts a new lead conversation · anything else, like a skill or a custom command, goes to the lead as typed")
	default:
		return false
	}
	m.dirty = true
	return true
}

func (m *model) setFocusList(on bool) {
	m.focusList = on && len(m.workers) > 0
	m.input.Placeholder = "message the lead…"
	if m.target != nil {
		m.input.Placeholder = "message agent " + m.target.name + " directly…  (esc cancels)"
	}
	if m.focusList {
		m.input.Blur()
	} else {
		m.input.Focus()
	}
}

// remove takes a finished agent off the list; its workdir and log stay. The lead is told, so it
// doesn't message it, and the name is free for a new agent.
func (m *model) remove(a *agentState) {
	i := slices.Index(m.workers, a)
	m.workers = slices.Delete(m.workers, i, i+1)
	if m.target == a {
		m.target = nil
	}
	m.sel = min(m.sel, max(len(m.workers)-1, 0))
	m.notes = append(m.notes, fmt.Sprintf("[agent-watch] The user removed agent %q from the list: it can't be messaged any more, and its workdir %s is untouched.", a.name, a.dir))
	if len(m.workers) == 0 {
		m.full = false
	}
	m.setFocusList(m.focusList)
	m.dirty = true
	m.svp.GotoBottom()
	m.layout()
}

func (m *model) selectWorker(i int) {
	if i < 0 || i >= len(m.workers) || i == m.sel {
		return
	}
	m.sel = i
	m.svp.GotoBottom()
	m.refresh()
}

// geometry of the main screen, shared by View and mouse hit-testing.
func (m *model) bodyH() int    { return m.h - 5 } // header (1), input box (3), footer (1)
func (m *model) leftW() int    { return m.w * 55 / 100 }
func (m *model) listRows() int { return max(1, min(len(m.workers), 8)) }
func (m *model) listH() int    { return m.listRows() + 2 }
func (m *model) listStart() int {
	return max(m.sel-m.listRows()+1, 0)
}

func (m *model) mouse(e tea.MouseMsg) (tea.Model, tea.Cmd) {
	if m.setup != nil {
		return m, nil
	}
	inLeft := !m.full && e.X < m.leftW() && e.Y >= 1 && e.Y <= m.bodyH()
	inList := !m.full && e.X >= m.leftW() && e.Y >= 1 && e.Y < 1+m.listH()
	inDetail := e.Y >= 1 && e.Y <= m.bodyH() && (m.full || e.X >= m.leftW() && e.Y >= 1+m.listH())
	switch {
	case e.Button == tea.MouseButtonWheelUp || e.Button == tea.MouseButtonWheelDown:
		vp := &m.mvp
		if inDetail || inList {
			vp = &m.svp
		}
		if e.Button == tea.MouseButtonWheelUp {
			vp.ScrollUp(3)
		} else {
			vp.ScrollDown(3)
		}
	case e.Action != tea.MouseActionPress || e.Button != tea.MouseButtonLeft:
	case inList:
		if i := m.listStart() + e.Y - 2; e.Y >= 2 && i < len(m.workers) {
			if i == m.sel && m.focusList {
				m.full = true // a click on the selected agent opens it fullscreen
				m.layout()
			}
			m.selectWorker(i)
		}
		m.setFocusList(true)
	case m.full && e.Y == 1:
		m.full = false // the fullscreen title closes it
		m.layout()
	case inDetail:
		m.setFocusList(true)
	case inLeft || e.Y > m.bodyH():
		m.setFocusList(false)
	}
	return m, nil
}

func (m *model) running() bool {
	for _, a := range append(m.workers, m.lead) {
		if a.running {
			return true
		}
	}
	return false
}

func (m *model) find(name string) *agentState {
	for _, a := range m.workers {
		if a.name == name {
			return a
		}
	}
	return nil
}

func (m *model) enabled() []string {
	var names []string
	for _, a := range m.cfg.Agents {
		if a.Enabled {
			names = append(names, a.Name)
		}
	}
	return names
}

func (m *model) logPath(a *agentState) string { return filepath.Join(m.runDir, a.name+".jsonl") }

func (m *model) layout() {
	lw := m.leftW()
	// the lead's pane: padding (2) wide; title, blank line and status line high
	m.mvp.Width, m.mvp.Height = max(lw-2, 1), max(m.bodyH()-3, 1)
	// agent panels: border (2) + padding (2) wide; border (2) + status line (1) high
	if m.full {
		m.svp.Width, m.svp.Height = max(m.w-4, 1), max(m.bodyH()-3, 1)
	} else {
		m.svp.Width, m.svp.Height = max(m.w-lw-4, 1), max(m.bodyH()-m.listH()-3, 1)
	}
	m.input.Width = max(m.w-5, 1)
	m.refresh()
}

func (m *model) refresh() {
	if len(m.lead.lines) == 0 {
		m.mvp.SetContent(m.welcome())
	} else {
		setContent(&m.mvp, m.lead.lines)
	}
	if len(m.workers) > 0 {
		setContent(&m.svp, m.workers[m.sel].lines)
	}
}

// welcome fills a new run's empty lead pane, like Claude Code's: a card, tips, and who can work.
func (m *model) welcome() string {
	w := max(m.mvp.Width, 20)
	card := lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).BorderForeground(cAccent).Padding(0, 1).Width(min(w, 72) - 2).Render(
		sAccent.Render("✻ ") + sBold.Render("Welcome to agent-watch!") + "\n\n" +
			sDim.Render("The lead plans and judges, agents do the work.") + "\n" + sDim.Render("cwd: "+tilde(m.cwd)))
	example := "use 2 agents"
	if e := m.enabled(); len(e) > 0 {
		example = "use 2 " + e[0] + " agents"
	}
	tips := []string{
		"Tell the lead what you want done. It reads the code, plans, and waits for your go.",
		"Say which agents and how many: \"" + example + "\". Each gets its own git worktree.",
		"Watch them on the right. " + sAccent.Render("tab") + " picks one, " + sAccent.Render("m") + " messages it, " + sAccent.Render("x") + " stops it.",
		"The lead checks every result itself and asks before it commits or merges.",
	}
	out := []string{card, "", sBold.Render("Tips for getting started:")}
	for i, t := range tips {
		out = append(out, hang(sDim.Render(fmt.Sprintf(" %d. ", i+1)), lipgloss.NewStyle().Foreground(cText).Width(w-4).Render(t)))
	}
	lead := m.cfg.lead()
	out = append(out, "", sBold.Render("Who works")+sDim.Render(" · ctrl+s to change"),
		"  "+sAccent.Render(fmt.Sprintf("%-12s", "lead"))+sDim.Render(lead.CLI+" "+strings.TrimPrefix(describe(lead), lead.Name+" ")))
	for _, a := range m.cfg.Agents {
		if a.Enabled {
			out = append(out, "  "+lipgloss.NewStyle().Foreground(agentColors[a.Name]).Render(fmt.Sprintf("%-12s", clip(a.Name, 11)))+
				sDim.Render(a.CLI+" "+strings.TrimPrefix(describe(a), a.Name+" ")))
		}
	}
	if len(m.enabled()) == 0 {
		out = append(out, sYellow.Render("  no agent types enabled yet: ctrl+s opens the setup"))
	}
	return strings.Join(out, "\n")
}

// tilde shortens a path in the home directory to ~/...
func tilde(p string) string {
	if home, err := os.UserHomeDir(); err == nil && (p == home || strings.HasPrefix(p, home+"/")) {
		return "~" + p[len(home):]
	}
	return p
}

// setContent re-renders a pane, staying at the bottom if it was there.
func setContent(vp *viewport.Model, lines []Line) {
	follow := vp.AtBottom()
	vp.SetContent(render(lines, vp.Width))
	if follow {
		vp.GotoBottom()
	}
}

func icon(a *agentState) string {
	switch {
	case a.running:
		return sAccent.Render(spinFrame())
	case a.turns == 0:
		return sDim.Render("○")
	case a.stopReason != "":
		return sYellow.Render("■")
	case a.exit != 0:
		return sRed.Render("✗")
	}
	return sGreen.Render("✓")
}

// statusLine is a pane's last line: the live spinner while running, the totals when idle.
func statusLine(a *agentState, idle string) string {
	if a.running {
		s := sAccent.Render(spinFrame()) + " " + shimmer(a.verb+"…") +
			sDim.Render(fmt.Sprintf(" (%s · ↑ %s ↓ %s · $%.3f)", dur(time.Since(a.started)), tokens(a.turnIn), tokens(a.turnOut), a.turnCost))
		if quiet := time.Since(a.heard); quiet > time.Minute {
			s += sYellow.Render(" · no output for " + dur(quiet.Truncate(time.Minute)))
		}
		return s
	}
	if a.turns == 0 {
		return sDim.Render(idle)
	}
	turns := "turns"
	if a.turns == 1 {
		turns = "turn"
	}
	return icon(a) + sDim.Render(fmt.Sprintf(" %d %s · %s · ↑ %s ↓ %s · $%.3f", a.turns, turns, dur(a.totalDur), tokens(a.in), tokens(a.out), a.cost))
}

func agentTitle(a *agentState, name string) string {
	who := badge(a.cfg.Name)
	if a.name == "lead" {
		who = sAccent.Render(a.cfg.Name) // the lead's color never changes
	}
	return name + sDim.Render(" · ") + who + " " + sDim.Render(strings.TrimPrefix(describe(a.cfg), a.cfg.Name+" "))
}

// doing is an agent's latest tool call or message, so the list shows what each one is up to.
func doing(a *agentState) string {
	for i := len(a.lines) - 1; i >= 0; i-- {
		switch l := a.lines[i]; l.Kind {
		case "you":
			return sDim.Render("reading its brief")
		case "tool":
			return sGreen.Render(l.Text) + " " + sDim.Render(relative(l.Arg, a.dir))
		case "text":
			return lipgloss.NewStyle().Foreground(cText).Render(strings.NewReplacer("`", "", "**", "").Replace(oneLine(l.Text)))
		case "err", "fail":
			return sRed.Render(oneLine(l.Text))
		}
	}
	return ""
}

func (m *model) workerRows(w int) string {
	if len(m.workers) == 0 {
		return sDim.Render("none yet · the lead starts them")
	}
	var rows []string
	for i := m.listStart(); i < m.listStart()+m.listRows() && i < len(m.workers); i++ {
		s := m.workers[i]
		took := s.totalDur
		if s.running {
			took += time.Since(s.started)
		}
		mark := "  "
		if i == m.sel && m.focusList {
			mark = sAccent.Render("› ") // a pointer, not just a color, marks the selection
		}
		row := mark + icon(s) + " " +
			sBold.Render(fmt.Sprintf("%-10s", clip(s.name, 10))) + " " +
			lipgloss.NewStyle().Foreground(agentColors[s.cfg.Name]).Render(fmt.Sprintf("%-8s", clip(s.cfg.Name, 8))) + " " +
			sDim.Render(fmt.Sprintf("%6s $%.2f  ", dur(took), s.cost)) + doing(s)
		rows = append(rows, xansi.Truncate(row, w, "…"))
	}
	return strings.Join(rows, "\n")
}

func (m *model) header() string {
	logo := lipgloss.NewStyle().Background(cAccent).Foreground(lipgloss.Color("#1A1A1A")).Bold(true).Render(" ✻ agent-watch ")
	cost := m.lead.cost
	for _, a := range m.workers {
		cost += a.cost
	}
	run := sDim.Render("  run ") + sBold.Render(fmt.Sprintf("$%.2f", cost))
	usage := m.usageLine()
	// The folder gets what's left, cut from the left so the project's name stays.
	where := ""
	if room := m.w - lipgloss.Width(logo+run+usage) - 5; room > 8 {
		p := []rune(tilde(m.cwd))
		if len(p) > room {
			p = append([]rune("…"), p[len(p)-room+1:]...)
		}
		where = sDim.Render("  " + string(p))
	}
	gap := max(m.w-lipgloss.Width(logo+run+where+usage)-1, 1)
	return xansi.Truncate(logo+run+where+strings.Repeat(" ", gap)+usage, m.w, "…")
}

func (m *model) footer() string {
	if m.status != "" {
		return sYellow.Render(xansi.Truncate(" "+m.status, m.w, "…"))
	}
	keys := [][2]string{{"enter", "send"}, {"tab", "agents"}, {"pgup/pgdn", "scroll"}, {"^y", "select text"}, {"^s", "setup"}, {"^c", "quit"}}
	switch {
	case m.selecting:
		keys = [][2]string{{"drag", "select text"}, {"enter or right-click", "copy it"}, {"pgup/pgdn", "scroll"}, {"^y", "mouse back to agent-watch"}}
	case m.full:
		keys = [][2]string{{"↑↓", "other agent"}, {"m", "message it"}, {"x", "stop it"}, {"d", "remove it"}, {"pgup/pgdn", "scroll"}, {"enter/esc", "close"}}
	case m.focusList:
		keys = [][2]string{{"↑↓", "select"}, {"enter", "fullscreen"}, {"m", "message it"}, {"x", "stop it"},
			{"d", "remove it"}, {"pgup/pgdn", "scroll"}, {"tab/esc", "back to input"}}
	case m.target != nil:
		keys = [][2]string{{"enter", "send to " + m.target.name}, {"esc", "cancel"}}
	}
	var parts []string
	for _, k := range keys {
		parts = append(parts, sAccent.Render(k[0])+" "+sDim.Render(k[1]))
	}
	return xansi.Truncate(" "+strings.Join(parts, sDim.Render("  ·  ")), m.w, "…")
}

func (m *model) View() string {
	if m.w == 0 {
		return ""
	}
	if m.setup != nil {
		return m.setup.view(m.w, m.h)
	}
	bodyH := m.bodyH()
	var body string
	if m.full {
		s := m.workers[m.sel]
		t := icon(s) + " " + agentTitle(s, sBold.Render(s.name)) + sDim.Render(" · esc or click here to close")
		body = panel(t, m.svp.View()+"\n"+statusLine(s, ""), m.w, bodyH, true)
	} else {
		lw := m.leftW()
		mt := sAccent.Render("✻ ") + agentTitle(m.lead, sBold.Render("Lead"))
		if m.cfg.Lead.Edit {
			mt += sDim.Render(" · may edit")
		}
		if !m.focusList {
			mt = sAccent.Render("✻ ") + agentTitle(m.lead, sAccent.Bold(true).Render("Lead"))
		}
		// Borderless like Claude Code's chat; the agents' panels frame the other side.
		left := lipgloss.NewStyle().Width(lw).Height(bodyH).MaxHeight(bodyH).Padding(0, 1).
			Render(mt + "\n\n" + m.mvp.View() + "\n" + statusLine(m.lead, "type below to talk to the lead"))
		run, done := 0, 0
		for _, s := range m.workers {
			if s.running {
				run++
			} else if s.turns > 0 {
				done++
			}
		}
		lt := sBold.Render("Agents") + sDim.Render(fmt.Sprintf(" · %d running · %d done", run, done))
		list := panel(lt, m.workerRows(m.w-lw-4), m.w-lw, m.listH(), m.focusList)
		detail := panel(sDim.Render("no agent yet"), sDim.Render(lipgloss.NewStyle().Width(max(m.w-lw-4, 1)).Render(
			"When the lead puts agents to work, they're listed above and the selected one's live transcript shows here.")), m.w-lw, bodyH-m.listH(), false)
		if len(m.workers) > 0 {
			s := m.workers[m.sel]
			detail = panel(icon(s)+" "+agentTitle(s, sBold.Render(s.name)), m.svp.View()+"\n"+statusLine(s, ""), m.w-lw, bodyH-m.listH(), m.focusList)
		}
		body = lipgloss.JoinHorizontal(lipgloss.Top, left, lipgloss.JoinVertical(lipgloss.Left, list, detail))
	}
	rule := lipgloss.NewStyle().Foreground(cBorder).Render(strings.Repeat("─", m.w))
	if !m.focusList {
		rule = sDim.Render(strings.Repeat("─", m.w))
	}
	input := rule + "\n " + m.input.View() + "\n" + rule
	return lipgloss.JoinVertical(lipgloss.Left, m.header(), body, input, m.footer())
}
