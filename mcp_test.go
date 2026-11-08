package kritrim

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
)

func TestMain(m *testing.M) {
	if os.Getenv("KRITRIM_MCP_HELPER") == "1" {
		mcpHelper()
		return
	}
	os.Exit(m.Run())
}

func mcpHelper() {
	dec := json.NewDecoder(os.Stdin)
	enc := json.NewEncoder(os.Stdout)
	for {
		var req struct {
			ID     int64           `json:"id"`
			Method string          `json:"method"`
			Params json.RawMessage `json:"params"`
		}
		if err := dec.Decode(&req); err != nil {
			return
		}
		if req.ID == 0 {
			continue
		}
		reply := func(result any) {
			_ = enc.Encode(map[string]any{"jsonrpc": "2.0", "id": req.ID, "result": result})
		}
		switch req.Method {
		case "initialize":
			reply(map[string]any{"protocolVersion": "2024-11-05", "capabilities": map[string]any{}})
		case "tools/list":
			reply(map[string]any{"tools": []any{map[string]any{
				"name": "echo", "description": "echo text",
				"inputSchema": map[string]any{"type": "object", "properties": map[string]any{
					"text": map[string]any{"type": "string"}}, "required": []any{"text"}},
			}}})
		case "tools/call":
			var p struct {
				Name      string         `json:"name"`
				Arguments map[string]any `json:"arguments"`
			}
			_ = json.Unmarshal(req.Params, &p)
			reply(map[string]any{"content": []any{map[string]any{"type": "text",
				"text": fmt.Sprintf("echo:%v", p.Arguments["text"])}}})
		default:
			_ = enc.Encode(map[string]any{"jsonrpc": "2.0", "id": req.ID,
				"error": map[string]any{"code": -32601, "message": "unknown"}})
		}
	}
}

func TestMCPStdio(t *testing.T) {
	a := newAgent(t, Config{
		Model: &spyModel{turns: []Turn{TurnTool("srv__echo", map[string]any{"text": "hi"}), {Text: "done"}}},
		MCP: []MCPConfig{{
			Name:    "srv",
			Command: os.Args[0],
			Env:     []string{"KRITRIM_MCP_HELPER=1"},
		}},
	})
	res, err := a.Run(context.Background(), "x")
	if err != nil {
		t.Fatal(err)
	}
	if got := res.Steps[0].ToolResults[0].Content; got != "echo:hi" {
		t.Fatalf("result %q", got)
	}
}

func TestMCPHTTP(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			ID     int64           `json:"id"`
			Method string          `json:"method"`
			Params json.RawMessage `json:"params"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Fatal(err)
		}
		w.Header().Set("Content-Type", "application/json")
		switch req.Method {
		case "initialize":
			json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": req.ID, "result": map[string]any{}})
		case "tools/list":
			json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": req.ID, "result": map[string]any{
				"tools": []any{map[string]any{"name": "ping", "description": "ping",
					"inputSchema": map[string]any{"type": "object", "properties": map[string]any{}}}}}})
		case "tools/call":
			json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": req.ID, "result": map[string]any{
				"content": []any{map[string]any{"type": "text", "text": "pong"}}}})
		default:
			json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": req.ID, "error": map[string]any{"code": -32601, "message": "unknown"}})
		}
	}))
	defer srv.Close()
	a := newAgent(t, Config{
		Model: &spyModel{turns: []Turn{TurnTool("web__ping", nil), {Text: "done"}}},
		MCP:   []MCPConfig{{Name: "web", URL: srv.URL}},
	})
	res, err := a.Run(context.Background(), "x")
	if err != nil {
		t.Fatal(err)
	}
	if got := res.Steps[0].ToolResults[0].Content; got != "pong" {
		t.Fatalf("result %q", got)
	}
}
