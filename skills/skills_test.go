package skills

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ganeshdipdumbare/kritrim"
)

func TestLoadAndUse(t *testing.T) {
	dir := t.TempDir()
	os.MkdirAll(filepath.Join(dir, "pdf"), 0o755)
	os.WriteFile(filepath.Join(dir, "pdf", "SKILL.md"),
		[]byte("---\nname: pdf\ndescription: Work with PDFs\n---\n# PDF\nUse pdftotext."), 0o644)
	os.MkdirAll(filepath.Join(dir, "web"), 0o755)
	os.WriteFile(filepath.Join(dir, "web", "SKILL.md"),
		[]byte("---\nname: web\ndescription: Browse the web\n---\nUse curl."), 0o644)
	skills, err := Load(dir)
	if err != nil || len(skills) != 2 {
		t.Fatalf("%+v %v", skills, err)
	}
	if !strings.Contains(Prompt(skills), "pdf: Work with PDFs") {
		t.Fatalf("prompt: %q", Prompt(skills))
	}
	a, err := kritrim.New(kritrim.Config{Model: kritrim.Mock(), Tools: Tools(skills)})
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	body, err := a.CallTool(context.Background(), "use_skill", map[string]any{"name": "pdf"})
	if err != nil || !strings.Contains(body, "pdftotext") {
		t.Fatalf("%q %v", body, err)
	}
	if _, err := a.CallTool(context.Background(), "use_skill", map[string]any{"name": "missing"}); err == nil {
		t.Fatal("unknown skill should fail")
	}
}
