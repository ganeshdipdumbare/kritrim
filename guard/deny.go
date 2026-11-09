package guard

import (
	"context"
	"fmt"
	"regexp"

	"github.com/ganeshdipdumbare/kritrim"
)

// Deny blocks a run when the latest user message matches pattern.
func Deny(pattern, reason string) (kritrim.Hooks, error) {
	re, err := regexp.Compile(pattern)
	if err != nil {
		return kritrim.Hooks{}, fmt.Errorf("guard: %w", err)
	}
	return kritrim.Hooks{
		BeforeModel: func(ctx context.Context, e *kritrim.ModelEvent) error {
			for i := len(e.Request.Messages) - 1; i >= 0; i-- {
				m := e.Request.Messages[i]
				if m.Role != kritrim.RoleUser {
					continue
				}
				if re.MatchString(m.Content) {
					if reason == "" {
						reason = "blocked by policy"
					}
					return fmt.Errorf("guard: %s", reason)
				}
				break
			}
			return nil
		},
	}, nil
}
