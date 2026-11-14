// A2A files: send a text file and an image to a remote agent, then use the
// remote agent as a step in a graph. Offline: the server runs in-process.
package main

import (
	"context"
	"fmt"
	"log"
	"net/http/httptest"
	"strings"

	"github.com/ganeshdipdumbare/kritrim"
	"github.com/ganeshdipdumbare/kritrim/a2a"
	"github.com/ganeshdipdumbare/kritrim/multiagent"
)

// echo reports what reached the model: the prompt with inlined files, and
// how many images came along.
type echo struct{}

func (echo) Generate(ctx context.Context, req kritrim.Request) (*kritrim.Response, error) {
	last := req.Messages[len(req.Messages)-1]
	text := fmt.Sprintf("I got %d characters of text and %d image(s).", len(last.Content), len(last.Images))
	if strings.Contains(last.Content, "<file") {
		text += " A file was inlined."
	}
	return &kritrim.Response{Text: text, Stop: kritrim.StopDone}, nil
}

func (m echo) Stream(ctx context.Context, req kritrim.Request, emit func(kritrim.Delta)) (*kritrim.Response, error) {
	return m.Generate(ctx, req)
}

func main() {
	srv, err := a2a.NewServer(a2a.ServerConfig{
		Card:  a2a.Card{Name: "Reader", Description: "reads documents"},
		Auth:  a2a.Bearer("demo-token"),
		Agent: func(string) (*kritrim.Agent, error) { return kritrim.New(kritrim.Config{Model: echo{}}) },
	})
	if err != nil {
		log.Fatal(err)
	}
	defer srv.Close()
	ts := httptest.NewServer(srv)
	defer ts.Close()

	client, err := a2a.NewClient(a2a.ClientConfig{Endpoint: ts.URL,
		Headers: map[string]string{"Authorization": "Bearer demo-token"}})
	if err != nil {
		log.Fatal(err)
	}
	ctx := context.Background()
	res, err := client.Run(ctx, "Summarize the notes and describe the chart.",
		a2a.Files(
			a2a.File{Name: "notes.md", MimeType: "text/markdown", Bytes: []byte("# Q3\nRevenue up 12%.")},
			a2a.File{Name: "chart.png", MimeType: "image/png", Bytes: []byte("\x89PNG...")},
		))
	if err != nil {
		log.Fatal(err)
	}
	fmt.Println("remote:", res.Text)

	// The same remote agent as the second step of a pipeline.
	g, err := multiagent.Sequence(
		multiagent.Step{Name: "prepare", Node: func(ctx context.Context, p string, _ *multiagent.State) (*kritrim.Result, error) {
			return &kritrim.Result{Text: "Context: Q3 board meeting. " + p, Stop: kritrim.StopDone}, nil
		}},
		multiagent.Step{Name: "read", Node: client.Node()},
	)
	if err != nil {
		log.Fatal(err)
	}
	res, err = g.Run(ctx, "What changed?")
	if err != nil {
		log.Fatal(err)
	}
	fmt.Println("graph:", res.Text)
}
