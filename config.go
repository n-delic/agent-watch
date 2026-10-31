package main

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"

	"github.com/charmbracelet/lipgloss"
)

// Agent is one agent the lead can put to work: a CLI, an account and a model.
type Agent struct {
	Name    string `json:"name"`
	CLI     string `json:"cli"`            // one of clis
	Data    string `json:"data,omitempty"` // another account (opencode's database file, Claude's config dir, Codex's home); empty = the default one
	Model   string `json:"model,omitempty"`
	Effort  string `json:"effort,omitempty"`
	Enabled bool   `json:"enabled"` // the lead may start it
}

// Lead is the agent you talk to. It stands on its own: changing the agents never touches it.
type Lead struct {
	CLI    string `json:"cli"`
	Data   string `json:"data,omitempty"`
	Model  string `json:"model,omitempty"`
	Effort string `json:"effort,omitempty"`
	Edit   bool   `json:"edit,omitempty"` // the lead may edit files itself (small fixes, merges); off, it only briefs and checks
}

type Config struct {
	Lead   Lead    `json:"lead"`
	Agents []Agent `json:"agents"`
}

// defaultConfig has one agent per CLI with its default account and model, enabled if it's installed,
// and Claude Opus as the lead when Claude is installed.
func defaultConfig() Config {
	var c Config
	for _, cli := range clis {
		_, err := exec.LookPath(cli)
		c.Agents = append(c.Agents, Agent{Name: cli, CLI: cli, Enabled: err == nil})
		if err == nil && c.Lead.CLI == "" {
			c.Lead.CLI = cli
		}
	}
	if c.Lead.CLI == "" {
		c.Lead.CLI = "claude"
	}
	if c.Lead.CLI == "claude" {
		c.Lead.Model = "opus" // the lead's judgment is what makes cheap agents' work good
	}
	return c
}

func configPath() string {
	dir, err := os.UserConfigDir()
	if err != nil {
		dir = filepath.Join(os.Getenv("HOME"), ".config")
	}
	return filepath.Join(dir, "agent-watch", "config.json")
}

// loadConfig returns the saved config, or the defaults and false when none is saved yet.
func loadConfig() (Config, bool) {
	var c Config
	b, err := os.ReadFile(configPath())
	if err != nil || json.Unmarshal(b, &c) != nil || len(c.Agents) == 0 {
		return defaultConfig(), false
	}
	if c.Lead.CLI == "" { // a v0.1.0 config pointed the lead at an agent instead
		c.Lead.CLI = defaultConfig().Lead.CLI
	}
	return c, true
}

// projectFile overrides the global setup for one project; it lives in the project or a parent folder.
const projectFile = ".agent-watch.json"

// findProject returns the nearest project file at or above dir, or "".
func findProject(dir string) string {
	for d := dir; ; d = filepath.Dir(d) {
		p := filepath.Join(d, projectFile)
		if _, err := os.Stat(p); err == nil {
			return p
		}
		if filepath.Dir(d) == d {
			return ""
		}
	}
}

// loadProject reads a project file. A broken one is an error, so a typo isn't silently ignored.
func loadProject(p string) (Config, error) {
	var c Config
	b, err := os.ReadFile(p)
	if err == nil {
		err = json.Unmarshal(b, &c)
	}
	return c, err
}

// withProject puts a project's overrides on top of the global setup: each lead field it sets,
// and its agents by name, replacing a global agent of the same name or adding a new one.
func withProject(global, project Config) Config {
	c := global
	c.Agents = slices.Clone(global.Agents)
	for _, f := range [][2]*string{{&c.Lead.CLI, &project.Lead.CLI}, {&c.Lead.Data, &project.Lead.Data},
		{&c.Lead.Model, &project.Lead.Model}, {&c.Lead.Effort, &project.Lead.Effort}} {
		if *f[1] != "" {
			*f[0] = *f[1]
		}
	}
	c.Lead.Edit = c.Lead.Edit || project.Lead.Edit // a project can turn it on, not off
	for _, a := range project.Agents {
		if i := slices.IndexFunc(c.Agents, func(g Agent) bool { return g.Name == a.Name }); i >= 0 {
			c.Agents[i] = a
		} else {
			c.Agents = append(c.Agents, a)
		}
	}
	return c
}

// overrides says what a project file changes, for the setup screen: "lead model · agents ds3, local".
func overrides(project Config) string {
	var parts []string
	var lead []string
	for _, f := range []struct{ name, v string }{{"cli", project.Lead.CLI}, {"account", project.Lead.Data},
		{"model", project.Lead.Model}, {"effort", project.Lead.Effort}} {
		if f.v != "" {
			lead = append(lead, f.name)
		}
	}
	if project.Lead.Edit {
		lead = append(lead, "may edit files")
	}
	if len(lead) > 0 {
		parts = append(parts, "lead "+strings.Join(lead, ", "))
	}
	var names []string
	for _, a := range project.Agents {
		names = append(names, a.Name)
	}
	if len(names) > 0 {
		parts = append(parts, "agents "+strings.Join(names, ", "))
	}
	return strings.Join(parts, " · ")
}

func saveConfig(c Config) error {
	p := configPath()
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return err
	}
	b, _ := json.MarshalIndent(c, "", "  ")
	return os.WriteFile(p, b, 0o644)
}

func (c Config) find(name string) (Agent, bool) {
	for _, a := range c.Agents {
		if a.Name == name {
			return a, true
		}
	}
	return Agent{}, false
}

// lead is the lead as an agent; its name is its CLI, which is how it's shown.
func (c Config) lead() Agent {
	l := c.Lead
	return Agent{Name: l.CLI, CLI: l.CLI, Data: l.Data, Model: l.Model, Effort: l.Effort}
}

// enabled returns the agent the lead may start under this name.
func (c Config) enabled(name string) (Agent, bool) {
	a, ok := c.find(name)
	return a, ok && a.Enabled
}

// check returns what's wrong with a config before it's saved, or "".
func (c Config) check() string {
	seen := map[string]bool{}
	for _, a := range c.Agents {
		switch {
		case !validName.MatchString(a.Name):
			return "agent names must be lowercase letters, digits and dashes: " + a.Name
		case seen[a.Name]:
			return "two agents are named " + a.Name
		case !slices.Contains(clis, a.CLI):
			return a.Name + ": the cli must be one of " + strings.Join(clis, ", ")
		case a.Data != "" && !multiAccount(a):
			return a.Name + ": " + a.CLI + " can't use another account; leave account empty"
		}
		seen[a.Name] = true
	}
	switch {
	case !slices.Contains(clis, c.Lead.CLI):
		return "the lead's cli must be one of " + strings.Join(clis, ", ")
	case c.Lead.Data != "" && !multiAccount(c.lead()):
		return "the lead: " + c.Lead.CLI + " can't use another account; leave account empty"
	}
	return ""
}

// missing says the lead's CLI isn't installed, or "". Not part of check: a config is valid on any machine.
func (c Config) missing() string {
	if _, err := exec.LookPath(c.Lead.CLI); err != nil {
		return "the lead's cli, " + c.Lead.CLI + ", isn't installed here: pick another"
	}
	return ""
}

// describe is how an agent is shown to you and the lead: "personal opencode-go/x#max".
func describe(a Agent) string {
	model := a.Model
	if model == "" {
		model = "default model"
	}
	switch {
	case a.Effort == "":
		return a.Name + " " + model
	case a.CLI == "claude":
		return a.Name + " " + model + " " + a.Effort
	}
	return a.Name + " " + model + "#" + a.Effort
}

var palette = []lipgloss.Color{cBlue, cPurple, cGreen, cYellow, lipgloss.Color("#F778BA"), lipgloss.Color("#56D4DD")}

// agentColors gives each agent the color of its place in the list, so editing an agent never recolors
// anything. The lead always has the accent color.
var agentColors = map[string]lipgloss.Color{}

func setColors(c Config) {
	clear(agentColors)
	for i, a := range c.Agents {
		agentColors[a.Name] = palette[i%len(palette)]
	}
}

// multiAccount says whether an agent's CLI can use a login other than its default.
func multiAccount(a Agent) bool {
	_, ok := cliOf(a).Accounts()
	return ok
}

// account identifies the login an agent uses: agents on the same one share its limits and spend.
func accountKey(a Agent) string { return a.CLI + "\x00" + expand(a.Data) }
