// Package zcode runs Z.ai's ZCode agent (`zcode -p`, from the zcode-app-cli npm package) as an agent.
// It has one login only: no Accounts.
package zcode

import (
	"cmp"
	"encoding/json"
	"slices"
	"strings"

	"github.com/n-delic/agent-watch/cli"
)

type CLI struct{ cli.Base }

func (CLI) Name() string { return "zcode" }

var efforts = []string{"", "low", "high", "max"}

func (CLI) Efforts() []string { return efforts }

// shortcut: a fixed list of Z.ai's plans' models; read the provider registry if other plans need them.
var models = []cli.Model{
	{ID: "account:zai-start-plan/GLM-5.3", Price: "Start Plan", Variants: efforts[1:]},
	{ID: "account:zai-start-plan/GLM-5.3-Flash", Price: "Start Plan", Variants: efforts[1:]},
	{ID: "account:zai-individual-coding-plan/GLM-5.3", Price: "Coding Plan", Variants: efforts[1:]},
	{ID: "account:zai-individual-coding-plan/GLM-5.3-Flash", Price: "Coding Plan", Variants: efforts[1:]},
}

func (CLI) Models(string) ([]cli.Model, error) { return models, nil }

// Command starts a session zcode names itself (in every event), and resumes it by that id after.
func (CLI) Command(t cli.Turn) (string, []string, []string) {
	args := []string{"--output-format", "stream-json", "--mode", "yolo"}
	if t.System != "" && !t.Edits {
		// the lead must not fix its agents' code
		args = append(args, "--disallowed-tools", "Write Edit ApplyPatch")
	}
	if t.Model != "" {
		args = append(args, "--model", t.Model)
	}
	if t.Effort != "" && slices.Contains(efforts, t.Effort) {
		args = append(args, "--effort", t.Effort)
	}
	prompt := t.Prompt
	if t.Session != "" {
		args = append(args, "--resume", t.Session)
	} else {
		prompt = cli.WithSystem(t.System, prompt) // zcode has no system-prompt flag
	}
	for _, img := range t.Images {
		args = append(args, "--attach", img)
	}
	return "zcode", append(args, "-p", prompt), nil
}

// NewParser reads `zcode -p --output-format stream-json`: session events, with text and reasoning arriving
// in deltas (gathered until their _end), tool calls and results, and a final result with the turn's usage.
func (CLI) NewParser() cli.Parser {
	var text, think strings.Builder
	session := ""
	return func(raw []byte) ([]cli.Line, cli.Delta) {
		var e struct {
			Type      string
			SessionID string `json:"sessionId"`
			Usage     struct{ InputTokens, OutputTokens int64 }
			Payload   struct {
				Kind     string
				Delta    string
				ToolName string
				Input    any // a tool call's arguments, but turn.started's prompt
				Result   struct {
					Success bool
					Content any
					Error   string
				}
				Error struct{ Message string }
			}
		}
		if json.Unmarshal(raw, &e) != nil {
			return []cli.Line{{Kind: "info", Text: string(raw)}}, cli.Delta{}
		}
		var d cli.Delta
		if e.SessionID != "" && e.SessionID != session {
			session = e.SessionID
			d.Session = session
		}
		p := e.Payload
		switch e.Type + ":" + p.Kind {
		case "model.streaming:reasoning_delta":
			think.WriteString(p.Delta)
		case "model.streaming:reasoning_end":
			t := strings.TrimSpace(think.String())
			think.Reset()
			if t != "" {
				return []cli.Line{{Kind: "think", Text: t}}, d
			}
		case "model.streaming:text_delta":
			text.WriteString(p.Delta)
		case "model.streaming:text_end":
			t := strings.TrimSpace(text.String())
			text.Reset()
			if t != "" {
				return []cli.Line{{Kind: "text", Text: t}}, d
			}
		case "model.streaming:tool_call":
			in, _ := p.Input.(map[string]any)
			return []cli.Line{cli.ToolLine(p.ToolName, in)}, d
		case "tool.updated:result":
			out, _ := p.Result.Content.(string)
			if !p.Result.Success {
				return []cli.Line{{Kind: "err", Text: cli.Clip(cmp.Or(p.Result.Error, out), 300)}}, d
			}
			return cli.OutLines(out), d
		case "turn.failed:":
			return []cli.Line{{Kind: "err", Text: p.Error.Message}}, d
		case "result:":
			d.In, d.Out = e.Usage.InputTokens, e.Usage.OutputTokens // the cached ones included
		}
		return nil, d
	}
}
