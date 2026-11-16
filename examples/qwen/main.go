// Qwen: a live run against Qwen's OpenAI-compatible endpoint. Needs
// QWEN_API_KEY in the environment.
//
//	QWEN_API_KEY=... go run ./examples/qwen
package main

import (
	"context"
	"fmt"
	"log"

	"github.com/ganeshdipdumbare/kritrim"
)

// qwenBaseURL is Qwen's OpenAI-compatible endpoint. Override the model name
// in model below to try another Qwen model listed by the endpoint.
const qwenBaseURL = "https://token-plan.maas.qwencloudapi.com/compatible-mode/v1"

func main() {
	a, err := kritrim.New(kritrim.Config{
		Model:        kritrim.Compatible("qwen", qwenBaseURL, "qwen3.8-max"),
		Instructions: "You are concise.",
	})
	if err != nil {
		log.Fatal(err)
	}
	defer a.Close()

	res, err := a.Run(context.Background(), "Say hello and name the model you are.")
	if err != nil {
		log.Fatal(err)
	}
	fmt.Println(res.Text)
}
