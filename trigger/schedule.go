package trigger

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"sync"
	"time"

	"github.com/ganeshdipdumbare/kritrim"
)

// Schedule is one recurring run. Set exactly one of Every and Cron.
type Schedule struct {
	// Name identifies the schedule in logs and in the dedupe store.
	// Default: Thread, then "schedule-<index>". Names must be unique.
	Name string
	// Every runs at each multiple of the interval since the Unix epoch,
	// so every instance agrees on when a tick is due.
	Every time.Duration
	// Cron is a five-field cron expression or a descriptor such as
	// "@daily"; see Cron.
	Cron string
	// Location is the time zone Cron is read in. Default time.Local.
	Location *time.Location

	Thread string
	Prompt string
	User   string
	Roles  []string
}

// Scheduler runs schedules against one agent. A schedule skips a tick when
// its previous run is still going. With a shared DedupeStore (SetStore),
// several schedulers running the same schedules run each tick once.
type Scheduler struct {
	agent     *kritrim.Agent
	schedules []schedule
	cancel    context.CancelFunc
	wg        sync.WaitGroup
	log       func(msg string)
	store     DedupeStore
}

type schedule struct {
	Schedule
	cron Cron
}

// NewScheduler validates the schedules.
func NewScheduler(agent *kritrim.Agent, schedules ...Schedule) (*Scheduler, error) {
	if agent == nil {
		return nil, errors.New("trigger: agent is required")
	}
	names := map[string]bool{}
	out := make([]schedule, len(schedules))
	for i, s := range schedules {
		if s.Name == "" {
			s.Name = s.Thread
		}
		if s.Name == "" {
			s.Name = "schedule-" + strconv.Itoa(i)
		}
		if names[s.Name] {
			return nil, fmt.Errorf("trigger: two schedules are named %q", s.Name)
		}
		names[s.Name] = true
		sc := schedule{Schedule: s}
		switch {
		case s.Every > 0 && s.Cron != "":
			return nil, fmt.Errorf("trigger: schedule %q sets both Every and Cron", s.Name)
		case s.Cron != "":
			c, err := ParseCron(s.Cron)
			if err != nil {
				return nil, err
			}
			sc.cron = c
			if sc.Location == nil {
				sc.Location = time.Local
			}
		case s.Every <= 0:
			return nil, fmt.Errorf("trigger: schedule %q needs Every > 0 or Cron", s.Name)
		}
		out[i] = sc
	}
	return &Scheduler{agent: agent, schedules: out}, nil
}

// SetLogger receives run errors.
func (s *Scheduler) SetLogger(fn func(msg string)) { s.log = fn }

// SetStore shares ticks with other schedulers through store: an instance
// runs a tick only if it claims it first. Call before Start.
func (s *Scheduler) SetStore(store DedupeStore) { s.store = store }

// Start begins the schedules and returns immediately.
func (s *Scheduler) Start(ctx context.Context) {
	ctx, s.cancel = context.WithCancel(ctx)
	for i := range s.schedules {
		s.wg.Add(1)
		go s.loop(ctx, i)
	}
}

// next is the first tick of sc after t.
func (sc schedule) next(t time.Time) time.Time {
	if sc.Every > 0 {
		return t.Truncate(sc.Every).Add(sc.Every)
	}
	return sc.cron.Next(t.In(sc.Location))
}

func (s *Scheduler) loop(ctx context.Context, i int) {
	defer s.wg.Done()
	sc := s.schedules[i]
	for {
		at := sc.next(time.Now())
		if at.IsZero() {
			s.logf("schedule %s: cron %q never fires", sc.Name, sc.Cron)
			return
		}
		t := time.NewTimer(time.Until(at))
		select {
		case <-ctx.Done():
			t.Stop()
			return
		case <-t.C:
		}
		// Ticks run inline, so a tick that is due while the previous
		// run is still going is skipped.
		s.tick(ctx, sc, at)
	}
}

func (s *Scheduler) tick(ctx context.Context, sc schedule, at time.Time) {
	id := "tick:" + sc.Name + ":" + strconv.FormatInt(at.UnixMilli(), 10)
	if s.store != nil {
		keep := 24 * time.Hour
		if sc.Every > keep {
			keep = 2 * sc.Every
		}
		c, err := s.store.Claim(ctx, id, sc.Name, at.Add(keep))
		if err != nil {
			s.logf("schedule %s: claim: %v", sc.Name, err)
			return
		}
		if !c.Won {
			return
		}
	}
	_, err := s.agent.Run(ctx, sc.Prompt, kritrim.Thread(sc.Thread),
		kritrim.User(sc.User), kritrim.Roles(sc.Roles...))
	if err != nil && ctx.Err() == nil {
		s.logf("schedule %s: %v", sc.Name, err)
	}
	if s.store != nil {
		// A failed tick is still a tick: it is recorded, not retried by
		// another instance.
		result, _ := json.Marshal(map[string]any{"ok": err == nil})
		if cerr := s.store.Complete(context.WithoutCancel(ctx), id, result); cerr != nil {
			s.logf("schedule %s: complete: %v", sc.Name, cerr)
		}
	}
}

func (s *Scheduler) logf(format string, args ...any) {
	if s.log != nil {
		s.log(fmt.Sprintf(format, args...))
	}
}

// Close stops the schedules and waits for running ticks.
func (s *Scheduler) Close() {
	if s.cancel != nil {
		s.cancel()
	}
	s.wg.Wait()
}
