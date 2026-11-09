// Expose an agent over A2A and call it with the client.
package main

import (
	"context"
	"fmt"
	"log"
	"net/http/httptest"

	"github.com/ganeshdipdumbare/kritrim"
	"github.com/ganeshdipdumbare/kritrim/a2a"
)

func main() {
	srv, err := a2a.NewServer(a2a.ServerConfig{
		Card: a2a.Card{Name: "greeter", Description: "says hello"},
		Auth: a2a.AllowAll(), // development only
		Agent: func(contextID string) (*kritrim.Agent, error) {
			return kritrim.New(kritrim.Config{
				Model: kritrim.Mock(kritrim.Turn{Text: "hello from A2A"}),
			})
		},
	})
	if err != nil {
		log.Fatal(err)
	}
	defer srv.Close()
	ts := httptest.NewServer(srv)
	defer ts.Close()

	client, err := a2a.NewClient(a2a.ClientConfig{Endpoint: ts.URL})
	if err != nil {
		log.Fatal(err)
	}
	res, err := client.Run(context.Background(), "hi")
	if err != nil {
		log.Fatal(err)
	}
	fmt.Println(res.Text)
}
