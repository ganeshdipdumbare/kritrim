package guard

import (
	"context"
	"strings"
	"testing"

	"github.com/ganeshdipdumbare/kritrim"
)

func TestPIIRedaction(t *testing.T) {
	h := PII()
	req := &kritrim.Request{System: "email a@b.com", Messages: []kritrim.Message{
		{Role: kritrim.RoleUser, Content: "my card is 4111 1111 1111 1111 and email a@b.com"}}}
	if err := h.BeforeModel(context.Background(), &kritrim.ModelEvent{Request: req}); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(req.Messages[0].Content, "a@b.com") || strings.Contains(req.Messages[0].Content, "4111") {
		t.Fatalf("not redacted: %q", req.Messages[0].Content)
	}
	res := &kritrim.ToolResult{Content: "ssn 123-45-6789"}
	h.AfterTool(context.Background(), &kritrim.ToolEvent{Result: res})
	if strings.Contains(res.Content, "123-45-6789") {
		t.Fatalf("tool result not redacted: %q", res.Content)
	}
}
