package main

// The agent CLIs, each in its own package under cli/, and one turn of one as a process.

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/n-delic/agent-watch/cli"
	"github.com/n-delic/agent-watch/cli/agy"
	"github.com/n-delic/agent-watch/cli/claude"
	"github.com/n-delic/agent-watch/cli/codex"
	"github.com/n-delic/agent-watch/cli/opencode"
	"github.com/n-delic/agent-watch/cli/zcode"
)

type (
	Line   = cli.Line
	Delta  = cli.Delta
	Limits = cli.Limits
	Window = cli.Window
)

// allCLIs are the CLIs agent-watch can run, in the setup's order.
var allCLIs = []cli.CLI{claude.CLI{}, opencode.CLI{}, codex.CLI{}, agy.CLI{}, zcode.CLI{}}

// clis are their names.
var clis = func() []string {
	var names []string
	for _, c := range allCLIs {
		names = append(names, c.Name())
	}
	return names
}()

// cliOf is the CLI an agent runs. Configs are checked on load, so an unknown name means a bug: it falls
// back to the first CLI rather than crash.
func cliOf(a Agent) cli.CLI {
	for _, c := range allCLIs {
		if c.Name() == a.CLI {
			return c
		}
	}
	return allCLIs[0]
}

var (
	clip    = cli.Clip
	oneLine = cli.OneLine
	expand  = cli.Expand
)

// command builds one turn. A non-empty system prompt marks the lead; edits says whether it may edit files.
func command(a Agent, session string, first bool, prompt, system, dir string, edits bool) *exec.Cmd {
	if strings.HasPrefix(prompt, "-") {
		prompt = " " + prompt // or the CLI reads it as a flag
	}
	prog, args, env := cliOf(a).Command(cli.Turn{Model: a.Model, Effort: a.Effort, Account: expand(a.Data), Session: session,
		First: first, Prompt: prompt, System: system, Edits: edits, Images: imagePaths(prompt)})
	cmd := exec.Command(prog, args...)
	// stdin stays nil (/dev/null): opencode waits forever on an open non-tty stdin.
	cmd.Dir = dir
	// opencode takes its directory from $PWD, which an explicit Env would leave stale.
	cmd.Env = append(append(os.Environ(), env...), "PWD="+dir)
	ownGroup(cmd) // so stop can kill the whole tree
	return cmd
}

// relative shows paths in and next to dir as Claude Code does: "x.go", "../repo-aw-x/x.go".
func relative(s, dir string) string {
	if dir == "" || dir == "/" {
		return s
	}
	s = strings.ReplaceAll(s, dir+"/", "")
	if parent := filepath.Dir(dir); parent != "/" {
		s = strings.ReplaceAll(s, parent+"/", "../")
	}
	return s
}
