package memory

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/ganeshdipdumbare/kritrim"
)

// topicEmbedder maps text onto three topics so similarity is predictable
// without a model: food, travel, code.
type topicEmbedder struct{ calls atomic.Int32 }

func (e *topicEmbedder) Embed(ctx context.Context, texts []string) ([][]float32, error) {
	e.calls.Add(1)
	topics := [][]string{
		{"pizza", "pasta", "dinner", "eat", "vegetarian", "food", "hungry"},
		{"flight", "lisbon", "hotel", "trip", "travel", "airport"},
		{"golang", "compiler", "bug", "code", "test"},
	}
	out := make([][]float32, len(texts))
	for i, t := range texts {
		v := make([]float32, len(topics))
		lt := strings.ToLower(t)
		for j, words := range topics {
			for _, w := range words {
				if strings.Contains(lt, w) {
					v[j]++
				}
			}
		}
		out[i] = v
	}
	return out, nil
}

func TestSemanticRanksByMeaning(t *testing.T) {
	ctx := context.Background()
	emb := &topicEmbedder{}
	s, err := NewSemantic(NewMemory(), emb, SemanticConfig{})
	if err != nil {
		t.Fatal(err)
	}
	_ = s.Put(ctx, "diet", "the user is vegetarian and loves pasta")
	_ = s.Put(ctx, "trip", "flight to Lisbon on Friday, hotel near the airport")
	_ = s.Put(ctx, "work", "debugging a golang compiler bug")
	items, err := s.Search(ctx, "what should I eat for dinner", 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 || items[0].Key != "diet" {
		t.Fatalf("got %+v", items)
	}
	// No shared keyword with "trip", still found by meaning.
	items, _ = s.Search(ctx, "travel plans", 1)
	if len(items) != 1 || items[0].Key != "trip" {
		t.Fatalf("got %+v", items)
	}
}

func TestSemanticPersistsAndEmbedsExisting(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	base, _ := NewFile(dir)
	_ = base.Put(ctx, "old", "pizza on fridays") // stored before semantic search existed
	emb := &topicEmbedder{}
	s, err := NewSemantic(base, emb, SemanticConfig{Dir: dir})
	if err != nil {
		t.Fatal(err)
	}
	items, err := s.Search(ctx, "food", 5)
	if err != nil || len(items) != 1 || items[0].Key != "old" {
		t.Fatalf("items=%+v err=%v", items, err)
	}
	// A restart reuses the stored vectors: only the query is embedded.
	emb2 := &topicEmbedder{}
	s2, _ := NewSemantic(base, emb2, SemanticConfig{Dir: dir})
	if _, err := s2.Search(ctx, "hungry", 5); err != nil {
		t.Fatal(err)
	}
	if emb2.calls.Load() != 1 {
		t.Fatalf("embed calls after restart = %d, want 1 (query only)", emb2.calls.Load())
	}
	_ = s2.Delete(ctx, "old")
	if items, _ := s2.Search(ctx, "food", 5); len(items) != 0 {
		t.Fatalf("deleted memory found: %+v", items)
	}
}

func TestSemanticWorksWithTools(t *testing.T) {
	s, _ := NewSemantic(NewMemory(), &topicEmbedder{}, SemanticConfig{})
	a, _ := kritrim.New(kritrim.Config{
		Model: kritrim.Mock(
			kritrim.TurnTool("remember", map[string]any{"key": "diet", "value": "vegetarian"}),
			kritrim.TurnTool("recall", map[string]any{"query": "food preferences"}),
			kritrim.Turn{Text: "ok"},
		),
		Tools: Tools(s),
	})
	res, err := a.Run(context.Background(), "x")
	if err != nil {
		t.Fatal(err)
	}
	if got := res.Steps[1].ToolResults[0].Content; got != "diet: vegetarian" {
		t.Fatalf("recall = %q", got)
	}
}

func TestOpenAIEmbedder(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/embeddings" || r.Header.Get("Authorization") != "Bearer k" {
			t.Errorf("%s %s", r.URL.Path, r.Header.Get("Authorization"))
		}
		var in struct {
			Model string   `json:"model"`
			Input []string `json:"input"`
		}
		_ = json.NewDecoder(r.Body).Decode(&in)
		if in.Model != "m" || len(in.Input) != 2 {
			t.Errorf("%+v", in)
		}
		// Out of order on purpose: index decides placement.
		w.Write([]byte(`{"data":[{"index":1,"embedding":[0,1]},{"index":0,"embedding":[1,0]}]}`))
	}))
	defer srv.Close()
	vs, err := OpenAIEmbedder("m", kritrim.ModelConfig{BaseURL: srv.URL + "/v1", APIKey: "k"}).Embed(context.Background(), []string{"a", "b"})
	if err != nil || vs[0][0] != 1 || vs[1][1] != 1 {
		t.Fatalf("%v %v", vs, err)
	}
}

func TestGeminiEmbedder(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1beta/models/gemini-embedding-001:batchEmbedContents" || r.Header.Get("x-goog-api-key") != "k" {
			t.Errorf("%s", r.URL.Path)
		}
		w.Write([]byte(`{"embeddings":[{"values":[0.5,0.5]}]}`))
	}))
	defer srv.Close()
	vs, err := GeminiEmbedder("gemini-embedding-001", kritrim.ModelConfig{BaseURL: srv.URL, APIKey: "k"}).Embed(context.Background(), []string{"a"})
	if err != nil || len(vs) != 1 || vs[0][0] != 0.5 {
		t.Fatalf("%v %v", vs, err)
	}
}

func TestBedrockEmbedder(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasPrefix(r.Header.Get("Authorization"), "AWS4-HMAC-SHA256") {
			t.Error("unsigned")
		}
		if strings.Contains(r.URL.Path, "cohere") {
			w.Write([]byte(`{"embeddings":[[1,2],[3,4]]}`))
			return
		}
		w.Write([]byte(`{"embedding":[9,9]}`))
	}))
	defer srv.Close()
	cfg := kritrim.ModelConfig{BaseURL: srv.URL, AccessKey: "AK", SecretKey: "SK", Region: "us-east-1"}
	vs, err := BedrockEmbedder("amazon.titan-embed-text-v2:0", cfg).Embed(context.Background(), []string{"a", "b"})
	if err != nil || len(vs) != 2 || vs[1][0] != 9 {
		t.Fatalf("titan %v %v", vs, err)
	}
	vs, err = BedrockEmbedder("cohere.embed-english-v3", cfg).Embed(context.Background(), []string{"a", "b"})
	if err != nil || len(vs) != 2 || vs[1][1] != 4 {
		t.Fatalf("cohere %v %v", vs, err)
	}
}

func TestEmbedderError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "rate limited", http.StatusTooManyRequests)
	}))
	defer srv.Close()
	_, err := OpenAIEmbedder("m", kritrim.ModelConfig{BaseURL: srv.URL, APIKey: "k"}).Embed(context.Background(), []string{"a"})
	var se *kritrim.StatusError
	if !errors.As(err, &se) || se.Code != http.StatusTooManyRequests {
		t.Fatalf("err = %v", err)
	}
}

func BenchmarkSemanticSearch1000(b *testing.B) {
	ctx := context.Background()
	s, _ := NewSemantic(NewMemory(), &topicEmbedder{}, SemanticConfig{})
	words := []string{"pizza dinner", "flight lisbon", "golang bug", "hotel trip", "pasta food"}
	for i := range 1000 {
		_ = s.Put(ctx, fmt.Sprintf("k%d", i), words[i%len(words)])
	}
	b.ReportAllocs()
	for b.Loop() {
		if _, err := s.Search(ctx, "what to eat", 5); err != nil {
			b.Fatal(err)
		}
	}
}
