// Package guard provides ready-made hooks for common safety checks.
package guard

import (
	"context"
	"regexp"

	"github.com/ganeshdipdumbare/kritrim"
)

var patterns = map[string]*regexp.Regexp{
	"email":       regexp.MustCompile(`[A-Za-z0-9._%+\-]+@[A-Za-z0-9.\-]+\.[A-Za-z]{2,}`),
	"phone":       regexp.MustCompile(`\+?\d[\d\s().\-]{7,}\d`),
	"credit_card": regexp.MustCompile(`\b(?:\d[ -]*?){13,19}\b`),
	"ssn":         regexp.MustCompile(`\b\d{3}-\d{2}-\d{4}\b`),
	"ipv4":        regexp.MustCompile(`\b(?:\d{1,3}\.){3}\d{1,3}\b`),
}

// PII redacts personal data in prompts, model replies and tool results.
// With no types it redacts every supported type.
func PII(types ...string) kritrim.Hooks {
	var chosen []*regexp.Regexp
	if len(types) == 0 {
		for _, re := range patterns {
			chosen = append(chosen, re)
		}
	} else {
		for _, t := range types {
			if re := patterns[t]; re != nil {
				chosen = append(chosen, re)
			}
		}
	}
	redact := func(s string) string {
		for _, re := range chosen {
			s = re.ReplaceAllString(s, "[redacted]")
		}
		return s
	}
	return kritrim.Hooks{
		BeforeModel: func(ctx context.Context, e *kritrim.ModelEvent) error {
			for i := range e.Request.Messages {
				e.Request.Messages[i].Content = redact(e.Request.Messages[i].Content)
			}
			e.Request.System = redact(e.Request.System)
			return nil
		},
		AfterModel: func(ctx context.Context, e *kritrim.ModelEvent) {
			if e.Response != nil {
				e.Response.Text = redact(e.Response.Text)
				e.Response.Reasoning = redact(e.Response.Reasoning)
			}
		},
		AfterTool: func(ctx context.Context, e *kritrim.ToolEvent) {
			if e.Result != nil {
				e.Result.Content = redact(e.Result.Content)
			}
		},
	}
}
