// Semantic memory: recall by meaning, not by keyword. Offline: a tiny
// embedder stands in for a real one such as memory.OpenAIEmbedder.
package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"strings"

	"github.com/ganeshdipdumbare/kritrim"
	"github.com/ganeshdipdumbare/kritrim/memory"
)

// topics embeds text onto three axes: food, travel, work. A real embedder
// learns thousands of these.
type topics struct{}

func (topics) Embed(ctx context.Context, texts []string) ([][]float32, error) {
	axes := [][]string{
		{"vegetarian", "pasta", "dinner", "eat", "lunch", "restaurant", "hungry"},
		{"flight", "lisbon", "hotel", "trip", "airport", "travel"},
		{"deadline", "report", "meeting", "project", "review"},
	}
	out := make([][]float32, len(texts))
	for i, t := range texts {
		v := make([]float32, len(axes))
		for j, words := range axes {
			for _, w := range words {
				if strings.Contains(strings.ToLower(t), w) {
					v[j]++
				}
			}
		}
		out[i] = v
	}
	return out, nil
}

func main() {
	ctx := context.Background()
	dir, err := os.MkdirTemp("", "kritrim-memory-")
	if err != nil {
		log.Fatal(err)
	}
	defer os.RemoveAll(dir)
	base, err := memory.NewFile(dir)
	if err != nil {
		log.Fatal(err)
	}
	store, err := memory.NewSemantic(base, topics{}, memory.SemanticConfig{Dir: dir})
	if err != nil {
		log.Fatal(err)
	}
	_ = store.Put(ctx, "diet", "Ada is vegetarian and loves pasta")
	_ = store.Put(ctx, "trip", "Flight to Lisbon on Friday, hotel near the airport")
	_ = store.Put(ctx, "work", "Quarterly report due before the Monday meeting")

	// "Where should we go for lunch?" shares no word with the diet memory.
	items, err := store.Search(ctx, "Where should we go for lunch?", 1)
	if err != nil {
		log.Fatal(err)
	}
	fmt.Println("recalled:", items[0].Key, "-", items[0].Value)

	a, err := kritrim.New(kritrim.Config{
		Model: kritrim.Mock(kritrim.Turn{Text: "A vegetarian pasta place, since Ada loves pasta."}),
		Hooks: memory.Inject(store, 1),
	})
	if err != nil {
		log.Fatal(err)
	}
	defer a.Close()
	res, err := a.Run(ctx, "Where should we go for lunch?")
	if err != nil {
		log.Fatal(err)
	}
	fmt.Println("agent:", res.Text)
}
