// Cron: named schedules, a time zone, and ticks shared by several
// schedulers so each runs once. Offline; it runs for about three seconds.
package main

import (
	"context"
	"fmt"
	"log"
	"sync/atomic"
	"time"

	"github.com/ganeshdipdumbare/kritrim"
	"github.com/ganeshdipdumbare/kritrim/trigger"
)

func main() {
	berlin, err := time.LoadLocation("Europe/Berlin")
	if err != nil {
		berlin = time.UTC
	}
	digest, _ := trigger.ParseCron("0 9 * * mon-fri")
	fmt.Println("next weekday digest:", digest.Next(time.Now().In(berlin)).Format(time.RFC1123))

	// Two instances of the same service share one dedupe store. In
	// production that is trigger.SQLDedupe over your database.
	store := trigger.MemoryDedupe(0)
	var runs atomic.Int32
	var scheds []*trigger.Scheduler
	for i := range 2 {
		a, err := kritrim.New(kritrim.Config{
			Model: kritrim.Mock(kritrim.Turn{Text: "queue is empty"}),
			Hooks: kritrim.Hooks{AfterRun: func(ctx context.Context, e *kritrim.RunEvent) {
				runs.Add(1)
				fmt.Printf("instance %d ran %s\n", i, e.Thread)
			}},
		})
		if err != nil {
			log.Fatal(err)
		}
		defer a.Close()
		s, err := trigger.NewScheduler(a,
			trigger.Schedule{Name: "digest", Cron: "0 9 * * mon-fri", Location: berlin, Thread: "digest", Prompt: "summarize yesterday"},
			trigger.Schedule{Name: "poll", Every: time.Second, Thread: "poll", Prompt: "check the queue"},
		)
		if err != nil {
			log.Fatal(err)
		}
		s.SetStore(store)
		s.Start(context.Background())
		scheds = append(scheds, s)
	}
	time.Sleep(3200 * time.Millisecond)
	for _, s := range scheds {
		s.Close()
	}
	fmt.Printf("%d poll runs: one per tick, though 2 instances were scheduling\n", runs.Load())
}
