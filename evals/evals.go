// Package evals measures kritrim agents: cases, evaluators, detectors and a
// report you can gate CI on. It is library-first; the CLI renders reports and
// diagnoses failures.
package evals

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/ganeshdipdumbare/kritrim"
)

// Case is one input and what a good answer looks like.
type Case struct {
	Name     string         `json:"name"`
	Input    string         `json:"input"`
	Expected string         `json:"expected,omitempty"`
	Expect   map[string]any `json:"expect,omitempty"`
	Meta     map[string]any `json:"meta,omitempty"`
}

// Result is one evaluated case.
type Result struct {
	Case     Case            `json:"case"`
	Output   string          `json:"output"`
	Run      *kritrim.Result `json:"run,omitempty"`
	Err      string          `json:"error,omitempty"`
	Duration time.Duration   `json:"duration"`
	Tokens   int             `json:"tokens"`
	Scores   []Score         `json:"scores,omitempty"`
	Findings []Finding       `json:"findings,omitempty"`
	Pass     bool            `json:"pass"`
}

// Score is one evaluator result.
type Score struct {
	Evaluator string  `json:"evaluator"`
	Value     float64 `json:"value"`
	Pass      bool    `json:"pass"`
	Reason    string  `json:"reason,omitempty"`
}

// Evaluator scores one result.
type Evaluator interface {
	Name() string
	Evaluate(ctx context.Context, r Result) (Score, error)
}

// Experiment runs cases and evaluators.
type Experiment struct {
	Cases       []Case
	Evaluators  []Evaluator
	Concurrency int
	Threshold   float64 // minimum score per evaluator; default 1.0
	Cache       Cache
	CacheKey    string // agent/version discriminator for the cache
	Detect      bool   // include detectors in the report
}

// Cache stores model responses so reruns do not spend tokens.
type Cache interface {
	Load(key string) (*kritrim.Result, bool)
	Save(key string, res *kritrim.Result)
}

// Report is the outcome of an experiment.
type Report struct {
	Name     string        `json:"name"`
	Results  []Result      `json:"results"`
	Passed   int           `json:"passed"`
	Failed   int           `json:"failed"`
	Duration time.Duration `json:"duration"`
}

// OK reports whether every case passed.
func (r *Report) OK() bool { return r.Failed == 0 }

// Run executes the experiment. The run function builds or reuses an agent and
// returns its result for one case.
func (e Experiment) Run(ctx context.Context, run func(context.Context, Case) (*kritrim.Result, error)) (*Report, error) {
	if run == nil {
		return nil, fmt.Errorf("evals: run function is required")
	}
	if e.Threshold <= 0 {
		e.Threshold = 1
	}
	conc := e.Concurrency
	if conc <= 0 {
		conc = 1
	}
	started := time.Now()
	results := make([]Result, len(e.Cases))
	var wg sync.WaitGroup
	sem := make(chan struct{}, conc)
	for i, c := range e.Cases {
		wg.Add(1)
		go func(i int, c Case) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			results[i] = e.one(ctx, c, run)
		}(i, c)
	}
	wg.Wait()
	rep := &Report{Results: results, Duration: time.Since(started)}
	for _, r := range results {
		if r.Pass {
			rep.Passed++
		} else {
			rep.Failed++
		}
	}
	return rep, nil
}

func (e Experiment) one(ctx context.Context, c Case, run func(context.Context, Case) (*kritrim.Result, error)) Result {
	r := Result{Case: c}
	start := time.Now()
	var res *kritrim.Result
	var err error
	if e.Cache != nil {
		if cached, ok := e.Cache.Load(cacheKey(e.CacheKey, c)); ok {
			res = cached
		}
	}
	if res == nil {
		res, err = run(ctx, c)
		if err == nil && e.Cache != nil {
			e.Cache.Save(cacheKey(e.CacheKey, c), res)
		}
	}
	r.Duration = time.Since(start)
	if err != nil {
		r.Err = err.Error()
	} else {
		r.Run = res
		r.Output = res.Text
		r.Tokens = res.Usage.Total()
	}
	for _, ev := range e.Evaluators {
		sc, serr := ev.Evaluate(ctx, r)
		if serr != nil {
			sc = Score{Evaluator: ev.Name(), Value: 0, Pass: false, Reason: serr.Error()}
		}
		if sc.Evaluator == "" {
			sc.Evaluator = ev.Name()
		}
		if !sc.Pass && sc.Value >= e.Threshold {
			sc.Pass = true
		}
		r.Scores = append(r.Scores, sc)
	}
	if e.Detect {
		r.Findings = Detect(r)
	}
	r.Pass = r.Err == "" && allPass(r.Scores) && len(r.Findings) == 0
	return r
}

func allPass(scores []Score) bool {
	for _, s := range scores {
		if !s.Pass {
			return false
		}
	}
	return true
}

func cacheKey(ns string, c Case) string {
	b, _ := json.Marshal(c)
	sum := sha256.Sum256(append([]byte(ns+"\x00"), b...))
	return hex.EncodeToString(sum[:])
}

// DirCache stores run results as JSON files under dir.
func DirCache(dir string) Cache { return &dirCache{dir: dir} }

type dirCache struct{ dir string }

func (c *dirCache) path(key string) string { return filepath.Join(c.dir, key+".json") }

func (c *dirCache) Load(key string) (*kritrim.Result, bool) {
	b, err := os.ReadFile(c.path(key))
	if err != nil {
		return nil, false
	}
	var r kritrim.Result
	if json.Unmarshal(b, &r) != nil {
		return nil, false
	}
	return &r, true
}

func (c *dirCache) Save(key string, r *kritrim.Result) {
	if err := os.MkdirAll(c.dir, 0o700); err != nil {
		return
	}
	b, err := json.Marshal(r)
	if err != nil {
		return
	}
	_ = os.WriteFile(c.path(key), b, 0o600)
}

// Summary renders one line per case.
func (r *Report) Summary() string {
	var b strings.Builder
	for _, x := range r.Results {
		status := "PASS"
		if !x.Pass {
			status = "FAIL"
		}
		fmt.Fprintf(&b, "%s  %-30s %dms", status, x.Case.Name, x.Duration.Milliseconds())
		if x.Err != "" {
			fmt.Fprintf(&b, "  error=%s", x.Err)
		}
		for _, s := range x.Scores {
			fmt.Fprintf(&b, "  %s=%.2f", s.Evaluator, s.Value)
		}
		fmt.Fprintln(&b)
	}
	fmt.Fprintf(&b, "\n%d passed, %d failed in %s\n", r.Passed, r.Failed, r.Duration.Round(time.Millisecond))
	return b.String()
}

// WriteJSON writes the report as indented JSON.
func (r *Report) WriteJSON(w io.Writer) error {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(r)
}

// ReadReport reads a JSON report.
func ReadReport(r io.Reader) (*Report, error) {
	var rep Report
	if err := json.NewDecoder(r).Decode(&rep); err != nil {
		return nil, err
	}
	return &rep, nil
}

// WriteMarkdown writes a compact Markdown table.
func (r *Report) WriteMarkdown(w io.Writer) error {
	fmt.Fprintln(w, "| Case | Pass | Duration | Scores | Findings |")
	fmt.Fprintln(w, "|---|---|---|---|---|")
	for _, x := range r.Results {
		var scores []string
		for _, s := range x.Scores {
			scores = append(scores, fmt.Sprintf("%s %.2f", s.Evaluator, s.Value))
		}
		var findings []string
		for _, f := range x.Findings {
			findings = append(findings, f.Kind)
		}
		sort.Strings(findings)
		fmt.Fprintf(w, "| %s | %v | %dms | %s | %s |\n", x.Case.Name, x.Pass,
			x.Duration.Milliseconds(), strings.Join(scores, "; "), strings.Join(findings, ", "))
	}
	return nil
}
