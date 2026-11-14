package kritrim

import (
	"context"
	"fmt"
	"html"
	"net/url"
	"regexp"
	"strings"
	"time"
)

// WebFetchConfig configures the web_fetch tool.
type WebFetchConfig struct {
	// AllowHosts is required. Exact hosts may be private; "*." suffixes
	// resolve and must be public.
	AllowHosts []string
	// MaxChars caps the text handed to the model. Default 50,000.
	MaxChars int
	Headers  map[string]string
	Timeout  time.Duration
}

// WebFetch returns a tool that reads one page as text.
//
// It is http_request with the parts a model actually wants: one GET, and
// HTML turned into readable text instead of markup. A page of HTML is
// mostly tags, scripts and navigation, and sending that raw burns the
// context window on nothing. Use HTTP when the model needs methods,
// headers, bodies or the exact response; use WebFetch when it needs to
// read something.
//
//	kritrim.WebFetch(kritrim.WebFetchConfig{AllowHosts: []string{"*.wikipedia.org"}})
//
// The page is data, not instructions. Text fetched from the web reaches
// the model as a tool result, and a page that says "ignore your
// instructions" is a page saying so, not a user asking. Keep the allowlist
// as narrow as the task and give an agent that reads arbitrary pages no
// tool it should not be tricked into using.
func WebFetch(cfg WebFetchConfig) Tool {
	t := Tool{
		Name:        "web_fetch",
		Description: "Fetch a web page or file and return its readable text.",
		Schema: objectSchema(map[string]any{
			"url": map[string]any{"type": "string", "description": "absolute http(s) URL"},
		}, "url"),
		meta: map[string]any{"readonly": true},
	}
	if len(cfg.AllowHosts) == 0 {
		t.err = fmt.Errorf("kritrim: WebFetch needs at least one allowed host")
		return t
	}
	maxChars := cfg.MaxChars
	if maxChars <= 0 {
		maxChars = 50_000
	}
	t.run = func(ctx context.Context, args map[string]any) (string, error) {
		raw, _ := args["url"].(string)
		if raw == "" {
			return "", fmt.Errorf("url is required")
		}
		u, err := url.Parse(raw)
		if err != nil {
			return "", fmt.Errorf("invalid url: %w", err)
		}
		if u.Scheme != "http" && u.Scheme != "https" {
			return "", fmt.Errorf("url must be http or https")
		}
		resp, err := envOf(ctx).HTTP(ctx, httpRequest{Method: "GET", URL: raw, Headers: cfg.Headers,
			AllowHosts: cfg.AllowHosts, Timeout: cfg.Timeout})
		if err != nil {
			return "", err
		}
		if resp.Status/100 != 2 {
			return "", fmt.Errorf("%d %s", resp.Status, httpStatusText(resp.Status))
		}
		ctype := ""
		if resp.Header != nil {
			if v := resp.Header["Content-Type"]; len(v) > 0 {
				ctype = strings.ToLower(v[0])
			}
		}
		body := string(resp.Body)
		text := body
		if strings.Contains(ctype, "html") || (ctype == "" && looksLikeHTML(body)) {
			text = htmlToText(body)
		}
		return truncate(strings.TrimSpace(text), maxChars), nil
	}
	return t
}

func looksLikeHTML(s string) bool {
	head := strings.ToLower(s)
	if len(head) > 1024 {
		head = head[:1024]
	}
	return strings.Contains(head, "<html") || strings.Contains(head, "<!doctype html")
}

var (
	// RE2 has no backreferences, so each container is spelled out. (?is)
	// so . spans newlines: scripts and styles are multi-line.
	dropRE = regexp.MustCompile(`(?is)` +
		`<script\b[^>]*>.*?</script\s*>|<style\b[^>]*>.*?</style\s*>|` +
		`<noscript\b[^>]*>.*?</noscript\s*>|<svg\b[^>]*>.*?</svg\s*>|` +
		`<template\b[^>]*>.*?</template\s*>|<head\b[^>]*>.*?</head\s*>`)
	commentRE = regexp.MustCompile(`(?s)<!--.*?-->`)
	titleRE   = regexp.MustCompile(`(?is)<title[^>]*>(.*?)</title>`)
	breakRE   = regexp.MustCompile(`(?i)<(br|/p|/div|/li|/h[1-6]|/tr|/table|/section|/article|/blockquote)\s*/?>`)
	// Only containers whose closing tag is often missing open a line of
	// their own; <li> and <tr> get their break from the closing tag, so
	// listing them here would double-space every list.
	blockRE = regexp.MustCompile(`(?i)<(p|div|h[1-6]|section|article|blockquote)\b[^>]*>`)
	blankRE = regexp.MustCompile(`\n{3,}`)
	spaceRE = regexp.MustCompile(`[ \t]{2,}`)
)

// htmlToText renders HTML as the text a reader would see. It is a
// deliberate approximation: no DOM, no CSS, no JavaScript, so a page that
// builds its content in the browser comes back nearly empty.
func htmlToText(s string) string {
	title := ""
	if m := titleRE.FindStringSubmatch(s); len(m) == 2 {
		title = strings.TrimSpace(html.UnescapeString(tagRE.ReplaceAllString(m[1], "")))
	}
	s = commentRE.ReplaceAllString(s, "")
	s = dropRE.ReplaceAllString(s, "")
	s = breakRE.ReplaceAllString(s, "\n")
	s = blockRE.ReplaceAllString(s, "\n")
	s = tagRE.ReplaceAllString(s, "")
	s = html.UnescapeString(s)
	// A non-breaking space is a space to a reader.
	s = strings.ReplaceAll(s, " ", " ")
	var lines []string
	for _, line := range strings.Split(s, "\n") {
		lines = append(lines, strings.TrimSpace(spaceRE.ReplaceAllString(line, " ")))
	}
	s = blankRE.ReplaceAllString(strings.Join(lines, "\n"), "\n\n")
	s = strings.TrimSpace(s)
	if title != "" {
		return title + "\n\n" + s
	}
	return s
}

// Todos returns a tool that keeps the agent's plan in session state.
//
// A long task drifts: the model finishes step two, forgets steps four and
// five, and declares victory. A todo list it writes and rereads is the
// cheapest fix, and keeping it in session state means it survives a pause,
// a resume and a restart along with the conversation.
//
//	Tools: []kritrim.Tool{kritrim.Todos(), /* ... */}
//
// Put "Keep the todo list current" in Instructions; the tool stores a
// plan, it does not enforce one. Exactly one item may be in_progress, so
// the model has to finish what it started before it moves on.
func Todos() Tool {
	return Tool{
		Name: "todos",
		Description: "Track the plan for this task. Use write with the full list every time " +
			"(it replaces the previous list), and list to reread it.",
		Schema: objectSchema(map[string]any{
			"op": map[string]any{"type": "string", "enum": []any{"write", "list"},
				"description": "write replaces the list, list returns it"},
			"items": map[string]any{"type": "array", "description": "the full list, in order",
				"items": objectSchema(map[string]any{
					"task": map[string]any{"type": "string", "description": "what to do"},
					"status": map[string]any{"type": "string", "enum": []any{"pending", "in_progress", "done"},
						"description": "at most one item may be in_progress"},
				}, "task", "status")},
		}, "op"),
		run: func(ctx context.Context, args map[string]any) (string, error) {
			st := StateFrom(ctx)
			if st == nil {
				return "", fmt.Errorf("todos needs a run state")
			}
			op, _ := args["op"].(string)
			switch op {
			case "list":
				return renderTodos(todosFrom(st)), nil
			case "write":
				items, err := parseTodos(args["items"])
				if err != nil {
					return "", err
				}
				st.Set("todos", todosToState(items))
				return renderTodos(items), nil
			default:
				return "", fmt.Errorf("op must be write or list")
			}
		},
	}
}

type todo struct {
	Task   string
	Status string
}

func parseTodos(v any) ([]todo, error) {
	list, ok := v.([]any)
	if !ok {
		return nil, fmt.Errorf("items must be an array")
	}
	var out []todo
	running := 0
	for i, raw := range list {
		m, ok := raw.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("item %d must be an object", i+1)
		}
		task, _ := m["task"].(string)
		status, _ := m["status"].(string)
		if strings.TrimSpace(task) == "" {
			return nil, fmt.Errorf("item %d needs a task", i+1)
		}
		switch status {
		case "pending", "done":
		case "in_progress":
			running++
		default:
			return nil, fmt.Errorf("item %d: status must be pending, in_progress or done", i+1)
		}
		out = append(out, todo{Task: task, Status: status})
	}
	if running > 1 {
		return nil, fmt.Errorf("%d items are in_progress; finish one before starting another", running)
	}
	return out, nil
}

// todosToState stores plain types, so the list round-trips through any
// session store's JSON.
func todosToState(items []todo) []any {
	out := make([]any, 0, len(items))
	for _, it := range items {
		out = append(out, map[string]any{"task": it.Task, "status": it.Status})
	}
	return out
}

func todosFrom(st *State) []todo {
	list, _ := st.Get("todos").([]any)
	var out []todo
	for _, raw := range list {
		if m, ok := raw.(map[string]any); ok {
			task, _ := m["task"].(string)
			status, _ := m["status"].(string)
			out = append(out, todo{Task: task, Status: status})
		}
	}
	return out
}

func renderTodos(items []todo) string {
	if len(items) == 0 {
		return "no todos yet"
	}
	mark := map[string]string{"pending": "[ ]", "in_progress": "[~]", "done": "[x]"}
	var b strings.Builder
	done := 0
	for _, it := range items {
		m := mark[it.Status]
		if m == "" {
			m = "[ ]"
		}
		if it.Status == "done" {
			done++
		}
		fmt.Fprintf(&b, "%s %s\n", m, it.Task)
	}
	fmt.Fprintf(&b, "\n%d/%d done", done, len(items))
	return b.String()
}

// PythonConfig configures the python tool.
type PythonConfig struct {
	// Interpreter is the command to run. Default "python3".
	Interpreter string
	// Dir is the working directory.
	Dir     string
	Timeout time.Duration
	// Env adds KEY=VALUE variables. The interpreter does not inherit the
	// host environment beyond PATH, HOME, locale and temp-dir variables.
	Env []string
	// AllowUnsafeHost runs the interpreter on the host instead of
	// refusing. Development only: model-written Python on the host can
	// read your files, your keys and the network, and no allowlist can
	// narrow that down, because the whole point of the tool is code you
	// have not seen.
	AllowUnsafeHost bool
}

// Python returns a risky tool that runs model-written Python and returns
// its output.
//
// It needs Config.Sandbox (or sealed mode) and refuses to run without one
// unless AllowUnsafeHost is set:
//
//	a, _ := kritrim.New(kritrim.Config{
//		Model:   model,
//		Tools:   []kritrim.Tool{kritrim.Python(kritrim.PythonConfig{})},
//		Sandbox: kritrim.Docker(kritrim.DockerConfig{Image: "python:3.13-slim"}),
//		Approve: approve,
//	})
//
// This is the one built-in tool whose argument cannot be checked: an
// allowlist constrains a command, and Python is a language. Containers
// kritrim creates have no network, drop every capability and see no host
// files, which is what makes the tool safe to offer at all. The code goes
// to the interpreter on stdin, so there is no shell and nothing to quote.
func Python(cfg PythonConfig) Tool {
	interp := cfg.Interpreter
	if interp == "" {
		interp = "python3"
	}
	t := Risky(Tool{
		Name:        "python",
		Description: "Run Python code and return anything it prints.",
		Schema: objectSchema(map[string]any{
			"code": map[string]any{"type": "string", "description": "the program; print what you want back"},
		}, "code"),
	})
	t.run = func(ctx context.Context, args map[string]any) (string, error) {
		code, _ := args["code"].(string)
		if strings.TrimSpace(code) == "" {
			return "", fmt.Errorf("code is required")
		}
		e := envOf(ctx)
		if !e.sandboxed() && !cfg.AllowUnsafeHost {
			return "", fmt.Errorf("kritrim: python needs a sandbox; set Config.Sandbox or PythonConfig.AllowUnsafeHost")
		}
		// "-" reads the program from stdin: the code is never an argument,
		// so there is no shell and nothing to quote.
		res, err := e.Exec(ctx, execRequest{Command: []string{interp, "-"}, Stdin: []byte(code),
			Dir: cfg.Dir, Env: cfg.Env, Timeout: cfg.Timeout, Allow: []string{interp}})
		if err != nil {
			return "", err
		}
		out := strings.TrimSpace(res.Stdout)
		if res.Stderr != "" {
			out = strings.TrimSpace(out + "\n" + res.Stderr)
		}
		if res.Exit != 0 {
			return out, fmt.Errorf("python exited %d", res.Exit)
		}
		if out == "" {
			return "(no output; use print() to return a value)", nil
		}
		return out, nil
	}
	return t
}
