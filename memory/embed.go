package memory

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/ganeshdipdumbare/kritrim"
	"github.com/ganeshdipdumbare/kritrim/internal/awssig"
)

// OpenAIEmbedder embeds with the OpenAI embeddings API, or any
// OpenAI-compatible one through cfg.BaseURL. The key comes from cfg.APIKey,
// then OPENAI_API_KEY.
//
//	memory.OpenAIEmbedder("text-embedding-3-small")
func OpenAIEmbedder(model string, cfg ...kritrim.ModelConfig) Embedder {
	c := firstConfig(cfg)
	if c.BaseURL == "" {
		c.BaseURL = "https://api.openai.com/v1"
	}
	if c.APIKey == "" {
		c.APIKey = os.Getenv("OPENAI_API_KEY")
	}
	return &openaiEmbedder{model: model, cfg: c}
}

// OllamaEmbedder embeds with a local Ollama server.
//
//	memory.OllamaEmbedder("nomic-embed-text")
func OllamaEmbedder(model string, cfg ...kritrim.ModelConfig) Embedder {
	c := firstConfig(cfg)
	if c.BaseURL == "" {
		c.BaseURL = "http://localhost:11434/v1"
	}
	if c.APIKey == "" {
		c.APIKey = "ollama"
	}
	return &openaiEmbedder{model: model, cfg: c}
}

// GeminiEmbedder embeds with the Gemini API. The key comes from
// cfg.APIKey, then GEMINI_API_KEY.
//
//	memory.GeminiEmbedder("gemini-embedding-001")
func GeminiEmbedder(model string, cfg ...kritrim.ModelConfig) Embedder {
	c := firstConfig(cfg)
	if c.BaseURL == "" {
		c.BaseURL = "https://generativelanguage.googleapis.com"
	}
	if c.APIKey == "" {
		c.APIKey = os.Getenv("GEMINI_API_KEY")
	}
	return &geminiEmbedder{model: model, cfg: c}
}

// BedrockEmbedder embeds with an Amazon Bedrock model: Titan text
// embeddings ("amazon.titan-embed-text-v2:0") or Cohere embed
// ("cohere.embed-english-v3"). Region and credentials come from cfg, then
// the AWS_* environment variables.
func BedrockEmbedder(model string, cfg ...kritrim.ModelConfig) Embedder {
	c := firstConfig(cfg)
	creds := awssig.Credentials{Region: c.Region, AccessKey: c.AccessKey, SecretKey: c.SecretKey, SessionToken: c.SessionToken}.FromEnv()
	if c.BaseURL == "" {
		c.BaseURL = "https://bedrock-runtime." + creds.Region + ".amazonaws.com"
	}
	return &bedrockEmbedder{model: model, cfg: c, creds: creds}
}

func firstConfig(cfg []kritrim.ModelConfig) kritrim.ModelConfig {
	if len(cfg) > 0 {
		return cfg[0]
	}
	return kritrim.ModelConfig{}
}

func httpClient(c kritrim.ModelConfig) *http.Client {
	if c.Client != nil {
		return c.Client
	}
	t := c.Timeout
	if t <= 0 {
		t = time.Minute
	}
	return &http.Client{Timeout: t}
}

// postJSON sends body and decodes a 2xx reply into out.
func postJSON(ctx context.Context, c kritrim.ModelConfig, u string, body any, out any, prepare func(*http.Request, []byte) error) error {
	b, err := json.Marshal(body)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, u, bytes.NewReader(b))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	for k, v := range c.Headers {
		req.Header.Set(k, v)
	}
	if prepare != nil {
		if err := prepare(req, b); err != nil {
			return err
		}
	}
	resp, err := httpClient(c).Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 64<<20))
	if err != nil {
		return err
	}
	if resp.StatusCode/100 != 2 {
		return &kritrim.StatusError{Provider: "embeddings", Code: resp.StatusCode, Status: resp.Status, Body: strings.TrimSpace(string(raw))}
	}
	return json.Unmarshal(raw, out)
}

type openaiEmbedder struct {
	model string
	cfg   kritrim.ModelConfig
}

func (e *openaiEmbedder) Embed(ctx context.Context, texts []string) ([][]float32, error) {
	var out struct {
		Data []struct {
			Index     int       `json:"index"`
			Embedding []float32 `json:"embedding"`
		} `json:"data"`
	}
	err := postJSON(ctx, e.cfg, strings.TrimSuffix(e.cfg.BaseURL, "/")+"/embeddings",
		map[string]any{"model": e.model, "input": texts}, &out,
		func(r *http.Request, _ []byte) error {
			if e.cfg.APIKey != "" {
				r.Header.Set("Authorization", "Bearer "+e.cfg.APIKey)
			}
			return nil
		})
	if err != nil {
		return nil, err
	}
	vs := make([][]float32, len(texts))
	for _, d := range out.Data {
		if d.Index < 0 || d.Index >= len(vs) {
			return nil, fmt.Errorf("embeddings: index %d out of range", d.Index)
		}
		vs[d.Index] = d.Embedding
	}
	for i, v := range vs {
		if v == nil {
			return nil, fmt.Errorf("embeddings: no vector for input %d", i)
		}
	}
	return vs, nil
}

type geminiEmbedder struct {
	model string
	cfg   kritrim.ModelConfig
}

func (e *geminiEmbedder) Embed(ctx context.Context, texts []string) ([][]float32, error) {
	reqs := make([]any, len(texts))
	for i, t := range texts {
		reqs[i] = map[string]any{"model": "models/" + e.model, "content": map[string]any{"parts": []any{map[string]any{"text": t}}}}
	}
	var out struct {
		Embeddings []struct {
			Values []float32 `json:"values"`
		} `json:"embeddings"`
	}
	u := strings.TrimSuffix(e.cfg.BaseURL, "/") + "/v1beta/models/" + url.PathEscape(e.model) + ":batchEmbedContents"
	err := postJSON(ctx, e.cfg, u, map[string]any{"requests": reqs}, &out, func(r *http.Request, _ []byte) error {
		r.Header.Set("x-goog-api-key", e.cfg.APIKey)
		return nil
	})
	if err != nil {
		return nil, err
	}
	if len(out.Embeddings) != len(texts) {
		return nil, fmt.Errorf("embeddings: got %d vectors for %d inputs", len(out.Embeddings), len(texts))
	}
	vs := make([][]float32, len(texts))
	for i, em := range out.Embeddings {
		vs[i] = em.Values
	}
	return vs, nil
}

type bedrockEmbedder struct {
	model string
	cfg   kritrim.ModelConfig
	creds awssig.Credentials
}

func (e *bedrockEmbedder) Embed(ctx context.Context, texts []string) ([][]float32, error) {
	u := strings.TrimSuffix(e.cfg.BaseURL, "/") + "/model/" + url.PathEscape(e.model) + "/invoke"
	sign := func(r *http.Request, body []byte) error { return awssig.Sign(r, body, "bedrock", e.creds) }
	if strings.HasPrefix(e.model, "cohere.") {
		var out struct {
			Embeddings [][]float32 `json:"embeddings"`
		}
		if err := postJSON(ctx, e.cfg, u, map[string]any{"texts": texts, "input_type": "search_document"}, &out, sign); err != nil {
			return nil, err
		}
		if len(out.Embeddings) != len(texts) {
			return nil, fmt.Errorf("embeddings: got %d vectors for %d inputs", len(out.Embeddings), len(texts))
		}
		return out.Embeddings, nil
	}
	// Titan embeds one text per request.
	vs := make([][]float32, len(texts))
	for i, t := range texts {
		var out struct {
			Embedding []float32 `json:"embedding"`
		}
		if err := postJSON(ctx, e.cfg, u, map[string]any{"inputText": t}, &out, sign); err != nil {
			return nil, err
		}
		vs[i] = out.Embedding
	}
	return vs, nil
}
