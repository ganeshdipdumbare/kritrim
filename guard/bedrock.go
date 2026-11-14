package guard

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/ganeshdipdumbare/kritrim"
	"github.com/ganeshdipdumbare/kritrim/internal/awssig"
)

// ErrBlocked is returned (wrapped) when a guardrail blocks a prompt. The
// error text carries the guardrail's message.
var ErrBlocked = errors.New("guard: blocked by guardrail")

// BedrockConfig configures Bedrock.
type BedrockConfig struct {
	ID      string // guardrail identifier or ARN
	Version string // a version number or "DRAFT"

	// Region and credentials; empty fields come from the AWS_*
	// environment variables.
	Region       string
	AccessKey    string
	SecretKey    string
	SessionToken string

	// BaseURL overrides https://bedrock-runtime.<region>.amazonaws.com.
	BaseURL string
	Client  *http.Client

	// Tools also checks tool results, where indirect prompt injection
	// arrives. Each checked result is one more ApplyGuardrail call.
	Tools bool
}

// Bedrock checks traffic with an Amazon Bedrock guardrail
// (ApplyGuardrail), for any model:
//
//   - the user's prompt, before the first model call: a block fails the
//     run with ErrBlocked, a mask rewrites the prompt;
//   - every model reply: a block replaces the reply with the guardrail's
//     message, drops its tool calls and ends the run with
//     Result.Stop == kritrim.StopGuardrail, a mask rewrites the reply;
//   - with Tools, every tool result: a block replaces the result with an
//     error, a mask rewrites it.
//
// A failed guardrail call fails closed: the run returns the error. For
// Bedrock models, ModelConfig.Guardrail applies the guardrail inside the
// Converse call instead, with no extra request.
func Bedrock(cfg BedrockConfig) (kritrim.Hooks, error) {
	if cfg.ID == "" || cfg.Version == "" {
		return kritrim.Hooks{}, errors.New("guard: Bedrock needs ID and Version")
	}
	creds := awssig.Credentials{Region: cfg.Region, AccessKey: cfg.AccessKey, SecretKey: cfg.SecretKey, SessionToken: cfg.SessionToken}.FromEnv()
	base := cfg.BaseURL
	if base == "" {
		base = "https://bedrock-runtime." + creds.Region + ".amazonaws.com"
	}
	client := cfg.Client
	if client == nil {
		client = &http.Client{Timeout: 30 * time.Second}
	}
	g := &guardrail{
		url:    strings.TrimSuffix(base, "/") + "/guardrail/" + url.PathEscape(cfg.ID) + "/version/" + url.PathEscape(cfg.Version) + "/apply",
		creds:  creds,
		client: client,
	}
	h := kritrim.Hooks{
		BeforeModel: func(ctx context.Context, e *kritrim.ModelEvent) error {
			msgs := e.Request.Messages
			if len(msgs) == 0 || msgs[len(msgs)-1].Role != kritrim.RoleUser || msgs[len(msgs)-1].Content == "" {
				return nil
			}
			last := &msgs[len(msgs)-1]
			v, err := g.apply(ctx, "INPUT", last.Content)
			if err != nil {
				return err
			}
			if v.blocked {
				return fmt.Errorf("%w: %s", ErrBlocked, v.text)
			}
			if v.text != "" {
				last.Content = v.text
			}
			return nil
		},
		AfterModel: func(ctx context.Context, e *kritrim.ModelEvent) {
			if e.Response == nil || e.Response.Text == "" {
				return
			}
			v, err := g.apply(ctx, "OUTPUT", e.Response.Text)
			switch {
			case err != nil:
				e.Response.Text = "guardrail check failed: " + err.Error()
				e.Response.ToolCalls = nil
				e.Response.Stop = kritrim.StopGuardrail
			case v.blocked:
				e.Response.Text = v.text
				e.Response.ToolCalls = nil
				e.Response.Stop = kritrim.StopGuardrail
			case v.text != "":
				e.Response.Text = v.text
			}
		},
	}
	if cfg.Tools {
		h.AfterTool = func(ctx context.Context, e *kritrim.ToolEvent) {
			if e.Result == nil || e.Result.Content == "" {
				return
			}
			v, err := g.apply(ctx, "INPUT", e.Result.Content)
			switch {
			case err != nil:
				e.Result.Content, e.Result.IsError = "blocked: guardrail check failed", true
			case v.blocked:
				e.Result.Content, e.Result.IsError = "blocked by guardrail: "+v.text, true
			case v.text != "":
				e.Result.Content = v.text
			}
		}
	}
	return h, nil
}

type guardrail struct {
	url    string
	creds  awssig.Credentials
	client *http.Client
}

// verdict is what a guardrail decided: blocked, or masked into text, or
// nothing (text empty).
type verdict struct {
	blocked bool
	text    string
}

func (g *guardrail) apply(ctx context.Context, source, text string) (verdict, error) {
	body, _ := json.Marshal(map[string]any{
		"source":  source,
		"content": []any{map[string]any{"text": map[string]any{"text": text}}},
	})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, g.url, bytes.NewReader(body))
	if err != nil {
		return verdict{}, err
	}
	req.Header.Set("Content-Type", "application/json")
	if err := awssig.Sign(req, body, "bedrock", g.creds); err != nil {
		return verdict{}, err
	}
	resp, err := g.client.Do(req)
	if err != nil {
		return verdict{}, err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode/100 != 2 {
		return verdict{}, fmt.Errorf("guard: ApplyGuardrail: %s: %s", resp.Status, strings.TrimSpace(string(raw)))
	}
	var out struct {
		Action  string `json:"action"`
		Outputs []struct {
			Text string `json:"text"`
		} `json:"outputs"`
		Assessments []json.RawMessage `json:"assessments"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return verdict{}, err
	}
	if out.Action != "GUARDRAIL_INTERVENED" {
		return verdict{}, nil
	}
	var texts []string
	for _, o := range out.Outputs {
		texts = append(texts, o.Text)
	}
	v := verdict{text: strings.Join(texts, "\n")}
	// A policy that blocked reports action BLOCKED; one that only masked
	// reports ANONYMIZED, and the outputs are the masked content.
	for _, a := range out.Assessments {
		if bytes.Contains(a, []byte(`"BLOCKED"`)) {
			v.blocked = true
		}
	}
	if !v.blocked && v.text == "" {
		v.blocked = true
	}
	if v.blocked && v.text == "" {
		v.text = "the guardrail blocked this content"
	}
	return v, nil
}
