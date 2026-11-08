package kritrim

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"regexp"
	"strings"
	"time"
)

// Time returns a read-only tool that reports the current time.
func Time() Tool {
	return Tool{
		Name:        "current_time",
		Description: "Returns the current date and time in RFC 3339.",
		Schema:      objectSchema(map[string]any{}),
		meta:        map[string]any{"readonly": true},
		run: func(ctx context.Context, args map[string]any) (string, error) {
			return time.Now().UTC().Format(time.RFC3339), nil
		},
	}
}

// HTTPConfig configures the http_request tool.
type HTTPConfig struct {
	// AllowHosts is required. Exact hosts may be private; "*." suffixes
	// resolve and must be public.
	AllowHosts []string
	Headers    map[string]string
	Timeout    time.Duration
}

// HTTP returns a tool that makes an HTTP request to an allowlisted host.
func HTTP(cfg HTTPConfig) Tool {
	t := Tool{
		Name:        "http_request",
		Description: "Make an HTTP request and return the status and body.",
		Schema: objectSchema(map[string]any{
			"method": map[string]any{"type": "string", "enum": []any{"GET", "POST", "PUT", "PATCH", "DELETE"}},
			"url":    map[string]any{"type": "string", "description": "absolute URL"},
			"body":   map[string]any{"type": "string", "description": "request body"},
		}, "url"),
	}
	if len(cfg.AllowHosts) == 0 {
		t.err = fmt.Errorf("kritrim: HTTP needs at least one allowed host")
		return t
	}
	t.run = func(ctx context.Context, args map[string]any) (string, error) {
		method, _ := args["method"].(string)
		u, _ := args["url"].(string)
		body, _ := args["body"].(string)
		if u == "" {
			return "", fmt.Errorf("url is required")
		}
		resp, err := envOf(ctx).HTTP(ctx, httpRequest{Method: method, URL: u, Headers: cfg.Headers,
			Body: []byte(body), AllowHosts: cfg.AllowHosts, Timeout: cfg.Timeout})
		if err != nil {
			return "", err
		}
		return fmt.Sprintf("%d %s\n%s", resp.Status, httpStatusText(resp.Status), truncate(string(resp.Body), 100_000)), nil
	}
	return t
}

func httpStatusText(code int) string {
	if t := map[int]string{200: "OK", 201: "Created", 204: "No Content", 400: "Bad Request",
		401: "Unauthorized", 403: "Forbidden", 404: "Not Found", 429: "Too Many Requests", 500: "Internal Server Error"}[code]; t != "" {
		return t
	}
	return ""
}

// WebSearchConfig configures the web_search tool.
type WebSearchConfig struct {
	// Backend is "duckduckgo" (default, no key), "tavily" or "brave".
	Backend    string
	APIKey     string // literal or "secret://NAME"
	BaseURL    string
	MaxResults int
	Timeout    time.Duration
}

// WebSearch returns a tool that searches the web.
func WebSearch(cfg WebSearchConfig) Tool {
	backend := strings.ToLower(cfg.Backend)
	if backend == "" {
		backend = "duckduckgo"
	}
	if cfg.MaxResults == 0 {
		cfg.MaxResults = 5
	}
	t := Tool{
		Name:        "web_search",
		Description: "Search the web and return titles, URLs and snippets.",
		Schema: objectSchema(map[string]any{
			"query": map[string]any{"type": "string", "description": "what to search for"},
		}, "query"),
	}
	base := cfg.BaseURL
	switch backend {
	case "duckduckgo":
		if base == "" {
			base = "https://html.duckduckgo.com/html/"
		}
	case "tavily":
		if base == "" {
			base = "https://api.tavily.com/search"
		}
	case "brave":
		if base == "" {
			base = "https://api.search.brave.com/res/v1/web/search"
		}
	default:
		t.err = fmt.Errorf("kritrim: unknown web search backend %q", backend)
		return t
	}
	u, err := url.Parse(base)
	if err != nil {
		t.err = err
		return t
	}
	host := u.Hostname()
	t.run = func(ctx context.Context, args map[string]any) (string, error) {
		query, _ := args["query"].(string)
		if query == "" {
			return "", fmt.Errorf("query is required")
		}
		key := cfg.APIKey
		if strings.HasPrefix(key, "secret://") {
			v, err := envOf(ctx).Secret(ctx, strings.TrimPrefix(key, "secret://"))
			if err != nil {
				return "", err
			}
			key = v
		}
		req := httpRequest{AllowHosts: []string{host}, Timeout: cfg.Timeout}
		switch backend {
		case "duckduckgo":
			req.Method = "GET"
			req.URL = base + "?q=" + url.QueryEscape(query)
		case "tavily":
			b, _ := json.Marshal(map[string]any{"api_key": key, "query": query, "max_results": cfg.MaxResults})
			req.Method, req.URL, req.Body = "POST", base, b
			req.Headers = map[string]string{"Content-Type": "application/json"}
		case "brave":
			req.Method = "GET"
			req.URL = base + "?q=" + url.QueryEscape(query) + fmt.Sprintf("&count=%d", cfg.MaxResults)
			req.Headers = map[string]string{"X-Subscription-Token": key, "Accept": "application/json"}
		}
		resp, err := envOf(ctx).HTTP(ctx, req)
		if err != nil {
			return "", err
		}
		if resp.Status/100 != 2 {
			return "", fmt.Errorf("search returned %d: %s", resp.Status, truncate(string(resp.Body), 500))
		}
		return formatSearch(backend, resp.Body, cfg.MaxResults)
	}
	return t
}

type searchHit struct {
	Title, URL, Snippet string
}

func formatSearch(backend string, body []byte, max int) (string, error) {
	var hits []searchHit
	switch backend {
	case "duckduckgo":
		hits = parseDuckDuckGo(string(body))
	case "tavily":
		var raw struct {
			Results []struct {
				Title   string `json:"title"`
				URL     string `json:"url"`
				Content string `json:"content"`
			} `json:"results"`
		}
		if err := json.Unmarshal(body, &raw); err != nil {
			return "", err
		}
		for _, r := range raw.Results {
			hits = append(hits, searchHit{r.Title, r.URL, r.Content})
		}
	case "brave":
		var raw struct {
			Web struct {
				Results []struct {
					Title       string `json:"title"`
					URL         string `json:"url"`
					Description string `json:"description"`
				} `json:"results"`
			} `json:"web"`
		}
		if err := json.Unmarshal(body, &raw); err != nil {
			return "", err
		}
		for _, r := range raw.Web.Results {
			hits = append(hits, searchHit{r.Title, r.URL, r.Description})
		}
	}
	if len(hits) == 0 {
		return "No results.", nil
	}
	if len(hits) > max {
		hits = hits[:max]
	}
	var b strings.Builder
	for i, h := range hits {
		fmt.Fprintf(&b, "%d. %s\n%s\n%s\n\n", i+1, h.Title, h.URL, stripTags(h.Snippet))
	}
	return strings.TrimSpace(b.String()), nil
}

var (
	ddgResult = regexp.MustCompile(`(?s)<a[^>]+class="result__a"[^>]+href="([^"]+)"[^>]*>(.*?)</a>`)
	ddgSnip   = regexp.MustCompile(`(?s)<a[^>]+class="result__snippet"[^>]*>(.*?)</a>`)
	tagRE     = regexp.MustCompile(`<[^>]+>`)
)

func parseDuckDuckGo(html string) []searchHit {
	links := ddgResult.FindAllStringSubmatch(html, -1)
	snips := ddgSnip.FindAllStringSubmatch(html, -1)
	var hits []searchHit
	for i, m := range links {
		u := m[1]
		if parsed, err := url.Parse(u); err == nil {
			if v := parsed.Query().Get("uddg"); v != "" {
				u = v
			}
		}
		s := ""
		if i < len(snips) {
			s = stripTags(snips[i][1])
		}
		hits = append(hits, searchHit{stripTags(m[2]), u, s})
	}
	return hits
}

func stripTags(s string) string {
	return strings.TrimSpace(tagRE.ReplaceAllString(s, ""))
}

// ShellConfig configures the shell tool.
type ShellConfig struct {
	// Allow lists the allowed command words, e.g. []string{"git", "go"}.
	// It is required on the host and is the admin's intent inside a
	// sandbox.
	Allow   []string
	Dir     string
	Timeout time.Duration
}

// Shell returns a risky tool that runs a command. There is no shell
// interpretation: the model passes argv, and argv[0] must be allowed.
func Shell(cfg ShellConfig) Tool {
	t := Tool{
		Name:        "shell",
		Description: "Run a command and return its output.",
		Schema: objectSchema(map[string]any{
			"argv": map[string]any{"type": "array", "items": map[string]any{"type": "string"},
				"description": "command and arguments"},
		}, "argv"),
		meta: map[string]any{"risk": "high"},
	}
	if len(cfg.Allow) == 0 {
		t.err = fmt.Errorf("kritrim: Shell needs at least one allowed command")
		return t
	}
	t.run = func(ctx context.Context, args map[string]any) (string, error) {
		argv := toStrings(args["argv"])
		if len(argv) == 0 {
			return "", fmt.Errorf("argv is required")
		}
		res, err := envOf(ctx).Exec(ctx, execRequest{Command: argv, Dir: cfg.Dir, Timeout: cfg.Timeout, Allow: cfg.Allow})
		if err != nil {
			return "", err
		}
		out := res.Stdout
		if res.Stderr != "" {
			out += "\n" + res.Stderr
		}
		if res.Exit != 0 {
			return strings.TrimSpace(out), fmt.Errorf("command exited %d", res.Exit)
		}
		return strings.TrimSpace(out), nil
	}
	return t
}

// FilesConfig configures the files tool.
type FilesConfig struct {
	// Root is the directory the tool may touch. Default: the working
	// directory.
	Root string
	// Write enables the write operation. Default false.
	Write bool
}

// Files returns a tool that reads and (optionally) writes files under Root.
func Files(cfg FilesConfig) Tool {
	opts := []any{"read"}
	if cfg.Write {
		opts = append(opts, "write")
	}
	t := Tool{
		Name:        "files",
		Description: "Read or write a file under the configured root.",
		Schema: objectSchema(map[string]any{
			"op":      map[string]any{"type": "string", "enum": opts, "description": "read or write"},
			"path":    map[string]any{"type": "string", "description": "path relative to the root"},
			"content": map[string]any{"type": "string", "description": "content to write"},
		}, "op", "path"),
	}
	if cfg.Write {
		t.meta = map[string]any{"risk": "high"}
	}
	t.run = func(ctx context.Context, args map[string]any) (string, error) {
		op, _ := args["op"].(string)
		p, _ := args["path"].(string)
		if p == "" {
			return "", fmt.Errorf("path is required")
		}
		switch op {
		case "read":
			b, err := envOf(ctx).ReadFile(ctx, fsRequest{Path: p, Root: cfg.Root})
			if err != nil {
				return "", err
			}
			return string(b), nil
		case "write":
			if !cfg.Write {
				return "", fmt.Errorf("files tool is read-only")
			}
			content, _ := args["content"].(string)
			if err := envOf(ctx).WriteFile(ctx, fsRequest{Path: p, Root: cfg.Root, Data: []byte(content)}); err != nil {
				return "", err
			}
			return "wrote " + p, nil
		default:
			return "", fmt.Errorf("op must be read or write")
		}
	}
	return t
}

func toStrings(v any) []string {
	switch x := v.(type) {
	case []string:
		return x
	case []any:
		out := make([]string, 0, len(x))
		for _, e := range x {
			if s, ok := e.(string); ok {
				out = append(out, s)
			}
		}
		return out
	}
	return nil
}

// ReadConfig configures the line-numbered read tool.
type ReadConfig struct{ Root string }

// Read returns a tool that reads a file as numbered lines.
func Read(cfg ReadConfig) Tool {
	return Tool{
		Name:        "read",
		Description: "Read a file as numbered lines. Use offset and limit for large files.",
		Schema: objectSchema(map[string]any{
			"path":   map[string]any{"type": "string", "description": "file path"},
			"offset": map[string]any{"type": "integer", "description": "first line, 1-based"},
			"limit":  map[string]any{"type": "integer", "description": "max lines (default 200)"},
		}, "path"),
		meta: map[string]any{"readonly": true},
		run: func(ctx context.Context, args map[string]any) (string, error) {
			p, _ := args["path"].(string)
			if p == "" {
				return "", fmt.Errorf("path is required")
			}
			b, err := envOf(ctx).ReadFile(ctx, fsRequest{Path: p, Root: cfg.Root})
			if err != nil {
				return "", err
			}
			lines := strings.Split(string(b), "\n")
			offset := intArg(args, "offset", 1)
			if offset < 1 {
				offset = 1
			}
			limit := intArg(args, "limit", 200)
			if limit <= 0 {
				limit = 200
			}
			var out strings.Builder
			for i := offset - 1; i < len(lines) && i < offset-1+limit; i++ {
				fmt.Fprintf(&out, "%6d\t%s\n", i+1, lines[i])
			}
			return out.String(), nil
		},
	}
}

// WriteConfig configures the write tool.
type WriteConfig struct{ Root string }

// Write returns a risky tool that creates or overwrites a file.
func Write(cfg WriteConfig) Tool {
	return Tool{
		Name:        "write",
		Description: "Create or overwrite a file with the full content.",
		Schema: objectSchema(map[string]any{
			"path":    map[string]any{"type": "string", "description": "file path"},
			"content": map[string]any{"type": "string", "description": "full content"},
		}, "path", "content"),
		meta: map[string]any{"risk": "high"},
		run: func(ctx context.Context, args map[string]any) (string, error) {
			p, _ := args["path"].(string)
			content, _ := args["content"].(string)
			if p == "" {
				return "", fmt.Errorf("path is required")
			}
			if err := envOf(ctx).WriteFile(ctx, fsRequest{Path: p, Root: cfg.Root, Data: []byte(content)}); err != nil {
				return "", err
			}
			return "wrote " + p, nil
		},
	}
}

// EditConfig configures the edit tool.
type EditConfig struct{ Root string }

// Edit returns a risky tool that replaces one exact, unique string in a
// file.
func EditFile(cfg EditConfig) Tool {
	return Tool{
		Name:        "edit",
		Description: "Replace one exact, unique string in a file.",
		Schema: objectSchema(map[string]any{
			"path": map[string]any{"type": "string", "description": "file path"},
			"old":  map[string]any{"type": "string", "description": "exact text to replace"},
			"new":  map[string]any{"type": "string", "description": "replacement"},
		}, "path", "old", "new"),
		meta: map[string]any{"risk": "high"},
		run: func(ctx context.Context, args map[string]any) (string, error) {
			p, _ := args["path"].(string)
			old, _ := args["old"].(string)
			next, _ := args["new"].(string)
			if p == "" || old == "" {
				return "", fmt.Errorf("path and old are required")
			}
			b, err := envOf(ctx).ReadFile(ctx, fsRequest{Path: p, Root: cfg.Root})
			if err != nil {
				return "", err
			}
			if n := strings.Count(string(b), old); n != 1 {
				return "", fmt.Errorf("old text occurs %d times; it must occur exactly once", n)
			}
			updated := strings.Replace(string(b), old, next, 1)
			if err := envOf(ctx).WriteFile(ctx, fsRequest{Path: p, Root: cfg.Root, Data: []byte(updated)}); err != nil {
				return "", err
			}
			return "edited " + p, nil
		},
	}
}

// Sleep returns a tool that pauses for a bounded number of seconds.
func Sleep(max time.Duration) Tool {
	if max <= 0 {
		max = 30 * time.Second
	}
	return Tool{
		Name:        "sleep",
		Description: fmt.Sprintf("Pause for a number of seconds (max %d).", int(max.Seconds())),
		Schema: objectSchema(map[string]any{
			"seconds": map[string]any{"type": "integer", "description": "seconds to sleep"},
		}, "seconds"),
		meta: map[string]any{"readonly": true},
		run: func(ctx context.Context, args map[string]any) (string, error) {
			secs := intArg(args, "seconds", 1)
			if secs < 0 {
				secs = 0
			}
			if time.Duration(secs)*time.Second > max {
				secs = int(max.Seconds())
			}
			t := time.NewTimer(time.Duration(secs) * time.Second)
			defer t.Stop()
			select {
			case <-ctx.Done():
				return "", ctx.Err()
			case <-t.C:
			}
			return fmt.Sprintf("slept %ds", secs), nil
		},
	}
}

// Stop returns a tool that ends the loop deliberately.
func Stop() Tool {
	return Tool{
		Name:        "stop",
		Description: "End the run. Use when the task is done and no more tools are needed.",
		Schema: objectSchema(map[string]any{
			"reason": map[string]any{"type": "string", "description": "why the run should stop"},
		}),
		meta: map[string]any{"readonly": true},
		run: func(ctx context.Context, args map[string]any) (string, error) {
			if st := StateFrom(ctx); st != nil {
				st.Set("stop", true)
			}
			reason, _ := args["reason"].(string)
			if reason == "" {
				reason = "stopped"
			}
			return reason, nil
		},
	}
}

func intArg(args map[string]any, key string, def int) int {
	switch v := args[key].(type) {
	case float64:
		return int(v)
	case int:
		return v
	case int64:
		return int(v)
	}
	return def
}
