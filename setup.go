package main

import (
	"fmt"
	"slices"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	xansi "github.com/charmbracelet/x/ansi"
)

type setupForm struct {
	cfg      Config // the global setup being edited
	note     string // what the project file overrides, if there is one
	row      int
	err      string
	known    map[string]modelList // models per account, shared with the model so they're fetched once
	accounts map[string][]string  // per CLI, the other logins found on this machine (~ for home)
	open     bool                 // the model picker is showing under the current model row
	query    string               // what's typed into it
	pick     int                  // the highlighted model in it
}

// field is one setup row: text is typed or cycled through choices, toggle is switched, action runs on enter/space.
type field struct {
	label   string
	text    *string
	empty   string // shown for an empty text
	choices []string
	toggle  *bool
	action  func()
	agent   int // index of the agent this row belongs to, -1 for none
	head    bool
	of      func() Agent // model rows: whose models to offer
	clear   *string      // cli rows: the account, which belongs to the cli it was set for
}

func (f *setupForm) fields() []field {
	c := &f.cfg
	lead := func() Agent { return c.lead() }
	// account rows only for a CLI that can use another login, a choice of those found on this machine
	account := func(a Agent, data *string, agent int) []field {
		if !multiAccount(a) {
			return nil
		}
		return []field{{label: "account", text: data, choices: f.accountChoices(a), agent: agent}}
	}
	fs := []field{{label: "cli", text: &c.Lead.CLI, choices: clis, agent: -1, clear: &c.Lead.Data}}
	fs = append(fs, account(c.lead(), &c.Lead.Data, -1)...)
	fs = append(fs,
		field{label: "model", text: &c.Lead.Model, empty: "default", agent: -1, of: lead},
		field{label: "effort", text: &c.Lead.Effort, choices: f.effortChoices(c.lead()), agent: -1},
		field{label: "may edit files", toggle: &c.Lead.Edit, agent: -1})
	for i := range c.Agents {
		a := &c.Agents[i]
		fs = append(fs,
			field{label: "name", text: &a.Name, agent: i, head: true},
			field{label: "cli", text: &a.CLI, choices: clis, agent: i, clear: &a.Data})
		fs = append(fs, account(*a, &a.Data, i)...)
		fs = append(fs,
			field{label: "model", text: &a.Model, empty: "default", agent: i, of: func() Agent { return *a }},
			field{label: "effort", text: &a.Effort, choices: f.effortChoices(*a), agent: i},
			field{label: "lead may use it", toggle: &a.Enabled, agent: i})
	}
	return append(fs, field{label: "+ add an agent", agent: -1, action: func() {
		n := len(c.Agents) + 1
		for _, ok := c.find(fmt.Sprint("agent-", n)); ok; _, ok = c.find(fmt.Sprint("agent-", n)) {
			n++
		}
		c.Agents = append(c.Agents, Agent{Name: fmt.Sprint("agent-", n), CLI: "opencode", Enabled: true})
		f.row = slices.IndexFunc(f.fields(), func(fl field) bool { return fl.head && fl.agent == len(c.Agents)-1 })
	}})
}

// accountChoices are the logins the setup offers an agent: its CLI's default, the others found on this
// machine, and the one it has even if it wasn't found.
func (f *setupForm) accountChoices(a Agent) []string {
	choices := append([]string{""}, f.accounts[a.CLI]...)
	if !slices.Contains(choices, a.Data) {
		choices = append(choices, a.Data)
	}
	return choices
}

// newSetup opens the setup on the global config; the project's overrides are only shown.
func (m *model) newSetup() *setupForm {
	f := &setupForm{cfg: m.global, known: m.models, accounts: map[string][]string{}}
	for _, c := range allCLIs {
		found, _ := c.Accounts()
		for _, p := range found {
			f.accounts[c.Name()] = append(f.accounts[c.Name()], tilde(p))
		}
	}
	if o := overrides(m.project); o != "" {
		f.note = "this project overrides " + o + " (" + tilde(m.projectPath) + ")"
	}
	return f
}

func (m *model) setupKey(k tea.KeyMsg) (tea.Model, tea.Cmd) {
	f := m.setup
	fs := f.fields()
	cur := fs[f.row]
	f.err = ""
	editable := cur.text != nil && cur.choices == nil
	if cur.of != nil {
		if cmd, used := f.pickerKey(cur, k); used {
			return m, cmd
		}
	}
	switch k.String() {
	case "ctrl+c":
		return m, tea.Quit
	case "up", "shift+tab":
		f.row = max(f.row-1, 0)
	case "down", "tab":
		f.row = min(f.row+1, len(fs)-1)
	case "esc":
		m.setup = nil
		setColors(m.cfg) // drop the colors of the edits just cancelled
		return m, nil
	case "ctrl+d":
		if cur.agent >= 0 && len(f.cfg.Agents) > 1 {
			f.cfg.Agents = slices.Delete(f.cfg.Agents, cur.agent, cur.agent+1)
			f.row = min(f.row, len(f.fields())-1)
		}
	case "enter":
		if cur.action != nil {
			cur.action()
			break
		}
		cfg := withProject(f.cfg, m.project)
		if f.err = cfg.check(); f.err != "" {
			break
		}
		if f.err = cfg.missing(); f.err != "" {
			break
		}
		if l := cfg.lead(); l != m.lead.cfg {
			if m.lead.running {
				f.err = "the lead is mid-turn; change it when it's done"
				break
			}
			if accountKey(l) != accountKey(m.lead.cfg) {
				// a session can't move to another CLI or login: only then does the lead start over
				m.lead = &agentState{name: "lead", dir: m.cwd, cfg: l, session: cliOf(l).NewSession(),
					lines: []Line{{Kind: "info", Text: "new lead session: " + describe(l)}}}
			} else {
				// model and effort go with every turn, so the conversation carries on
				m.lead.cfg = l
				m.lead.lines = append(m.lead.lines, Line{Kind: "info", Text: "lead now runs " + describe(l)})
			}
		}
		m.global, m.cfg = f.cfg, cfg
		setColors(m.cfg)
		if err := saveConfig(m.global); err != nil {
			m.status = "could not save the config: " + err.Error()
		}
		m.setup = nil
		m.layout()
		return m, m.refreshSpend()
	case "left", "right", " ":
		switch {
		case cur.action != nil:
			cur.action()
		case cur.toggle != nil:
			*cur.toggle = !*cur.toggle
		case cur.choices != nil:
			step := 1
			if k.String() == "left" {
				step = len(cur.choices) - 1
			}
			*cur.text = cur.choices[(slices.Index(cur.choices, *cur.text)+step)%len(cur.choices)]
			if cur.clear != nil {
				*cur.clear = ""
			}
		case editable && k.String() == " ":
			*cur.text += " "
		}
	case "backspace":
		if editable && *cur.text != "" {
			r := []rune(*cur.text)
			*cur.text = string(r[:len(r)-1])
		}
	default:
		if editable && k.Type == tea.KeyRunes {
			*cur.text += string(k.Runes)
		}
	}
	setColors(withProject(f.cfg, m.project)) // colors follow what will run, project agents included
	return m, nil
}

// pickerKey handles keys on a model row. Typing searches the agent's models (the model itself only
// changes when one is chosen); while the list is open, ↑↓ pick, enter/tab choose and esc closes it.
// It reports whether it used the key.
func (f *setupForm) pickerKey(cur field, k tea.KeyMsg) (tea.Cmd, bool) {
	switch {
	case k.Type == tea.KeyRunes || k.Type == tea.KeySpace || k.Type == tea.KeyBackspace:
		if !f.open {
			f.open, f.query = true, ""
		}
		if q := []rune(f.query); k.Type == tea.KeyBackspace && len(q) > 0 {
			f.query = string(q[:len(q)-1])
		} else if k.Type != tea.KeyBackspace {
			f.query += string(k.Runes)
		}
		f.pick = 0
		return f.fetch(cur.of()), true
	case !f.open:
		return nil, false
	}
	found := f.found(cur)
	switch k.String() {
	case "up", "shift+tab":
		f.pick = max(f.pick-1, 0)
	case "down":
		f.pick = min(f.pick+1, max(len(found)-1, 0))
	case "enter", "tab":
		if f.pick < len(found) {
			f.choose(cur, found[f.pick].ID)
		} else if q := strings.TrimSpace(f.query); q != "" {
			f.choose(cur, q) // a model that isn't listed
		}
		f.open = false
	case "esc":
		f.open = false
	default:
		return nil, false
	}
	return nil, true
}

// found is the picker's list for the search: a "default" entry, then the agent's matching models.
func (f *setupForm) found(cur field) []modelInfo {
	l, _ := f.listFor(cur.of())
	return matches(append([]modelInfo{{ID: "", Price: "the CLI's default"}}, l.list...), f.query)
}

// choose sets the model, and drops an effort the new model doesn't have (the effort row is next).
func (f *setupForm) choose(cur field, id string) {
	*cur.text = id
	effort := f.fields()[f.row+1]
	if !slices.Contains(f.effortChoices(cur.of()), *effort.text) {
		*effort.text = ""
	}
}

// picker is the model list shown under the current model row.
func (f *setupForm) picker(cur field, w int) []string {
	const indent, rows = "                     ", 8
	l, _ := f.listFor(cur.of())
	found := f.found(cur)
	search := indent + sAccent.Render("⌕ ") + sBold.Render(f.query) + sAccent.Render("▏")
	switch {
	case l.loading:
		return []string{search + sDim.Render("  loading models…")}
	case l.err != nil:
		return []string{search, indent + sRed.Render(xansi.Truncate("✗ "+l.err.Error(), w-len(indent), "…"))}
	case len(found) == 0:
		return []string{search + sDim.Render("  no model matches · enter uses what you typed")}
	}
	start := min(max(f.pick-rows/2, 0), max(len(found)-rows, 0))
	out := []string{search + sDim.Render(fmt.Sprintf("  %d · ↑↓ pick · enter choose · esc close", len(found)))}
	for i := start; i < min(start+rows, len(found)); i++ {
		mi := found[i]
		id := mi.ID
		if id == "" {
			id = "default"
		}
		row := fmt.Sprintf("%-42s %-17s ", clip(id, 42), mi.Price)
		st, mark := sDim, "  "
		if i == f.pick {
			st, mark = sBold, sAccent.Render("› ")
		}
		line := indent + mark + st.Render(row) + sDim.Render(strings.Join(mi.Variants, ","))
		out = append(out, xansi.Truncate(line, w, "…"))
	}
	return out
}

func (f *setupForm) view(w, h int) string {
	pw := min(w, 96)
	var lines []string
	cursor, agents := 0, false
	for i, fl := range f.fields() {
		if i == 0 {
			lines = append(lines, sBold.Render("Lead")+sDim.Render(" · the agent you talk to"))
		}
		if fl.agent >= 0 && !agents {
			agents = true
			lines = append(lines, "", sBold.Render("Agents")+sDim.Render(" · who the lead can put to work"))
		}
		if fl.head {
			lines = append(lines, "")
		}
		var v string
		switch {
		case fl.action != nil:
			v = ""
		case fl.toggle != nil && *fl.toggle:
			v = sGreen.Render("● yes")
		case fl.toggle != nil:
			v = sDim.Render("○ no")
		case fl.choices != nil:
			c := *fl.text
			if c == "" {
				c = "default"
			}
			label := sBold.Render(c)
			if i == 0 {
				label = sAccent.Render(c) // the lead's color never changes
			}
			v = sDim.Render("‹ ") + label + sDim.Render(" ›")
		case *fl.text == "":
			v = sDim.Render(fl.empty)
		case fl.head:
			v = lipgloss.NewStyle().Bold(true).Foreground(agentColors[*fl.text]).Render(*fl.text)
		default:
			v = sBold.Render(*fl.text)
		}
		label := sDim.Render(fmt.Sprintf("  %-18s", fl.label))
		if fl.action != nil {
			label = sAccent.Render("  " + fl.label)
		}
		line := " " + label + v
		if i == f.row {
			cursor = len(lines)
			line = sAccent.Render("›") + label + v
			if fl.of != nil && !f.open {
				line += sDim.Render("   type to search models")
			} else if fl.text != nil && fl.choices == nil && fl.of == nil {
				line += sAccent.Render("▏")
			}
			line = lipgloss.NewStyle().Background(cSelBg).Width(pw - 4).Render(line)
		}
		lines = append(lines, line)
		if i == f.row && f.open && fl.of != nil {
			lines = append(lines, f.picker(fl, pw-4)...)
		}
	}
	help := []string{"", sDim.Render("empty model or account = the CLI's default · type on a model row to pick from its list"),
		sDim.Render("account ←→ picks a login found here: opencode .db files, ~/.claude-*, ~/.codex-*")}
	if f.note != "" {
		help = append(help, sYellow.Render(xansi.Truncate("⚑ "+f.note, pw-4, "…")))
	}
	room := max(h-4-len(help), 5) // panel borders, keys line and error line
	start := min(max(cursor-room/2, 0), max(len(lines)-room, 0))
	lines = lines[start:min(start+room, len(lines))]
	keys := sAccent.Render("↑↓") + sDim.Render(" move  ") + sAccent.Render("←→ space") + sDim.Render(" change  ") +
		sAccent.Render("type") + sDim.Render(" edit  ") + sAccent.Render("^d") + sDim.Render(" remove agent  ") +
		sAccent.Render("enter") + sDim.Render(" save  ") + sAccent.Render("esc") + sDim.Render(" cancel")
	if f.err != "" {
		keys = sRed.Render("✗ "+f.err) + "\n" + keys
	}
	body := lipgloss.NewStyle().Width(pw - 4).Render(strings.Join(append(lines, help...), "\n"))
	box := panel(sAccent.Render("✻ ")+sBold.Render("Setup")+sDim.Render(" · who leads, who works"), body, pw, lipgloss.Height(body)+2, true)
	return lipgloss.Place(w, h, lipgloss.Center, lipgloss.Center, box+"\n"+keys)
}
