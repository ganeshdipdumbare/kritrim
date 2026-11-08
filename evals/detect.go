package evals

import (
	"fmt"
	"strings"
)

// Finding is one diagnosed problem.
type Finding struct {
	Kind   string `json:"kind"`
	Detail string `json:"detail,omitempty"`
	Step   int    `json:"step,omitempty"`
}

// Detect looks for the failures that are worth reporting verbatim.
func Detect(r Result) []Finding {
	var out []Finding
	if r.Err != "" {
		out = append(out, Finding{Kind: "run_error", Detail: r.Err})
	}
	if strings.TrimSpace(r.Output) == "" {
		out = append(out, Finding{Kind: "empty_output"})
	}
	if r.Run == nil {
		return out
	}
	switch r.Run.Stop {
	case "max_steps":
		out = append(out, Finding{Kind: "max_steps"})
	case "interrupt":
		out = append(out, Finding{Kind: "approval_pending", Detail: "thread paused on interrupts"})
	}
	seen := map[string]bool{}
	for i, s := range r.Run.Steps {
		for _, tr := range s.ToolResults {
			if tr.IsError {
				out = append(out, Finding{Kind: "tool_error", Detail: tr.Name + ": " + tr.Content, Step: i})
			}
			if strings.Contains(tr.Content, "rejected:") {
				out = append(out, Finding{Kind: "approval_rejected", Detail: tr.Name, Step: i})
			}
		}
		for _, c := range s.ToolCalls {
			key := c.Name + ":" + fmt.Sprint(c.Args)
			if seen[key] {
				out = append(out, Finding{Kind: "loop", Detail: "repeated " + c.Name, Step: i})
			}
			seen[key] = true
		}
	}
	return out
}
