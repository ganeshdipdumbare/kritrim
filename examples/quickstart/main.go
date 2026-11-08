// Quickstart: the smallest useful agent. No API key, no network.
package main

import (
	"context"
	"fmt"
	"log"

	"github.com/ganeshdipdumbare/kritrim"
)

func main() {
	a, err := kritrim.New(kritrim.Config{
		Model:        kritrim.Mock(kritrim.Turn{Text: "Hello. I am a mock model."}),
		Instructions: "You are concise.",
	})
	if err != nil {
		log.Fatal(err)
	}
	defer a.Close()

	res, err := a.Run(context.Background(), "Say hello")
	if err != nil {
		log.Fatal(err)
	}
	fmt.Println(res.Text)
}
