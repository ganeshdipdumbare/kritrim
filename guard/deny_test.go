package guard

import (
	"context"
	"testing"

	"github.com/ganeshdipdumbare/kritrim"
)

func TestDeny(t *testing.T) {
	h, err := Deny(`(?i)secret`, "no secrets")
	if err != nil {
		t.Fatal(err)
	}
	req := &kritrim.Request{Messages: []kritrim.Message{{Role: kritrim.RoleUser, Content: "my SECRET plan"}}}
	if err := h.BeforeModel(context.Background(), &kritrim.ModelEvent{Request: req}); err == nil {
		t.Fatal("want denial")
	}
	ok := &kritrim.Request{Messages: []kritrim.Message{{Role: kritrim.RoleUser, Content: "hello"}}}
	if err := h.BeforeModel(context.Background(), &kritrim.ModelEvent{Request: ok}); err != nil {
		t.Fatal(err)
	}
	if _, err := Deny("(", ""); err == nil {
		t.Fatal("invalid regex should fail")
	}
}
