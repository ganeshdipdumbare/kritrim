// Package skills loads Agent Skills (folders with a SKILL.md) and exposes
// them through one use_skill tool, keeping the system prompt short.
package skills

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/ganeshdipdumbare/kritrim"
)

// Skill is one loaded SKILL.md.
type Skill struct {
	Name        string
	Description string
	Body        string
}

// Load reads a single SKILL.md or every child folder that contains one.
func Load(dir string) ([]Skill, error) {
	if _, err := os.Stat(filepath.Join(dir, "SKILL.md")); err == nil {
		s, err := parse(filepath.Join(dir, "SKILL.md"), filepath.Base(dir))
		if err != nil {
			return nil, err
		}
		return []Skill{s}, nil
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	var out []Skill
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		p := filepath.Join(dir, e.Name(), "SKILL.md")
		if _, err := os.Stat(p); err != nil {
			continue
		}
		s, err := parse(p, e.Name())
		if err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, nil
}

func parse(path, fallback string) (Skill, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return Skill{}, err
	}
	s := Skill{Name: fallback, Body: string(b)}
	text := string(b)
	if strings.HasPrefix(text, "---") {
		if end := strings.Index(text[3:], "---"); end >= 0 {
			front := text[3 : 3+end]
			s.Body = strings.TrimSpace(text[3+end+3:])
			for _, line := range strings.Split(front, "\n") {
				line = strings.TrimSpace(line)
				switch {
				case strings.HasPrefix(line, "name:"):
					if v := strings.TrimSpace(strings.TrimPrefix(line, "name:")); v != "" {
						s.Name = strings.Trim(v, `"'`)
					}
				case strings.HasPrefix(line, "description:"):
					s.Description = strings.Trim(strings.TrimSpace(strings.TrimPrefix(line, "description:")), `"'`)
				}
			}
		}
	}
	return s, nil
}

// Prompt lists the skills for the system prompt.
func Prompt(skills []Skill) string {
	if len(skills) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("Skills you can load with use_skill:\n")
	for _, s := range skills {
		fmt.Fprintf(&b, "- %s: %s\n", s.Name, s.Description)
	}
	return strings.TrimSpace(b.String())
}

// Tools returns the use_skill tool.
func Tools(skills []Skill) []kritrim.Tool {
	byName := map[string]Skill{}
	for _, s := range skills {
		byName[s.Name] = s
	}
	return []kritrim.Tool{kritrim.NewTool("use_skill", "Load the full instructions for a skill by name.",
		func(ctx context.Context, in struct {
			Name string `json:"name" desc:"skill name from the system prompt"`
		}) (string, error) {
			s, ok := byName[in.Name]
			if !ok {
				return "", fmt.Errorf("unknown skill %q", in.Name)
			}
			return s.Body, nil
		})}
}
