package zcode

import (
	"slices"
	"testing"

	"github.com/n-delic/agent-watch/cli"
	"github.com/n-delic/agent-watch/cli/clitest"
)

// Real output of `zcode -p --output-format stream-json` on the Start Plan: think, read a.txt, think, answer.
func TestParse(t *testing.T) {
	got, sum := clitest.Parse(t, CLI{}, "testdata/zcode.jsonl")
	clitest.Shows(t, got, []cli.Line{{Kind: "think", Text: "The user wants me to read a.txt"},
		{Kind: "tool", Text: "Read", Arg: "/work/a.txt"}, {Kind: "out", Text: "1\thello from agent-watch"},
		{Kind: "text", Text: "The file contains a short two-line message"}})
	if sum.Session != "sess_7aefbf2a-9035-4e2c-90b5-7019cfbab9f2" {
		t.Errorf("session %q, so its next turn couldn't resume", sum.Session)
	}
	if sum.In != 34276 || sum.Out != 169 {
		t.Errorf("usage %d in, %d out; want 34276, 169", sum.In, sum.Out)
	}
	for _, l := range got {
		if l.Kind == "info" {
			t.Errorf("an event shown raw: %.80s", l.Text)
		}
	}
}

func TestCommand(t *testing.T) {
	prog, args, _ := CLI{}.Command(cli.Turn{Model: "account:zai-start-plan/GLM-5.3", Effort: "high", Prompt: "go", System: "lead"})
	if prog != "zcode" || !slices.Contains(args, "--disallowed-tools") || args[len(args)-1] != "lead\n\n---\n\ngo" || slices.Contains(args, "--resume") {
		t.Errorf("lead's first turn: %v", args)
	}
	_, args, _ = CLI{}.Command(cli.Turn{Prompt: "next", Session: "sess_1", Effort: `x" -c y`})
	if !slices.Contains(args, "--resume") || slices.Contains(args, "--disallowed-tools") || slices.Contains(args, "--effort") || args[len(args)-1] != "next" {
		t.Errorf("an agent's later turn: %v", args)
	}
}
