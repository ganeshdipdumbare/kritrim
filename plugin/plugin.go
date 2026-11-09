// Package plugin provides small, composable hooks and tools that behave like
// plugins without a plugin runtime.
package plugin

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/ganeshdipdumbare/kritrim"
)

// Inject appends text from fn to the system prompt before every model call.
// Use it for a clock, environment facts or a live lookup; the text is not
// stored in history.
func Inject(fn func(ctx context.Context) string) kritrim.Hooks {
	return kritrim.Hooks{
		BeforeModel: func(ctx context.Context, e *kritrim.ModelEvent) error {
			if s := fn(ctx); s != "" {
				e.Request.System = strings.TrimSpace(e.Request.System + "\n\n" + s)
			}
			return nil
		},
	}
}

// Steer appends a user message to the next model call without storing it in
// history. Use it for just-in-time guidance.
func Steer(fn func(ctx context.Context) string) kritrim.Hooks {
	return kritrim.Hooks{
		BeforeModel: func(ctx context.Context, e *kritrim.ModelEvent) error {
			if s := fn(ctx); s != "" {
				e.Request.Messages = append(e.Request.Messages, kritrim.Message{Role: kritrim.RoleUser, Content: s})
			}
			return nil
		},
	}
}

// Offload moves large tool results to files and returns a read_offloaded
// tool. Add the tool to Config.Tools; the hook rewrites results above
// threshold bytes.
func Offload(dir string, threshold int) (kritrim.Hooks, kritrim.Tool) {
	if threshold <= 0 {
		threshold = 16_000
	}
	hook := kritrim.Hooks{
		AfterTool: func(ctx context.Context, e *kritrim.ToolEvent) {
			if e.Result == nil || e.Call == nil || e.Call.Name == "read_offloaded" {
				return
			}
			if len(e.Result.Content) <= threshold {
				return
			}
			if err := os.MkdirAll(dir, 0o700); err != nil {
				return
			}
			name := "offload-" + randID() + ".txt"
			if err := os.WriteFile(filepath.Join(dir, name), []byte(e.Result.Content), 0o600); err != nil {
				return
			}
			e.Result.Content = fmt.Sprintf("[offloaded to %s; use read_offloaded]", name)
		},
	}
	tool := kritrim.NewTool("read_offloaded", "Read a tool result that was offloaded to a file.",
		func(ctx context.Context, in struct {
			Path string `json:"path" desc:"file name returned by the offload marker"`
		}) (string, error) {
			name := filepath.Base(in.Path)
			if name != in.Path || strings.Contains(name, "..") {
				return "", fmt.Errorf("invalid path")
			}
			b, err := os.ReadFile(filepath.Join(dir, name))
			if err != nil {
				return "", err
			}
			return string(b), nil
		})
	return hook, tool
}

func randID() string {
	var b [8]byte
	_, _ = rand.Read(b[:])
	return hex.EncodeToString(b[:])
}
