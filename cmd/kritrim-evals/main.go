// Command kritrim-evals generates cases, runs them against any agent
// program, scores recorded traces, and renders and diagnoses reports.
//
//	kritrim-evals generate -model anthropic:claude-sonnet-5-5 -context agent.md -n 10 > cases.jsonl
//	kritrim-evals run -cases cases.jsonl -contains -o report.json -- go run ./myagent
//	kritrim-evals traces -judge faithful -judge-model openai:gpt-4.1 trace.jsonl
//	kritrim-evals report report.json
//	kritrim-evals diagnose report.json
//
// run starts the command once per case with the case input on stdin and
// KRITRIM_CASE set to the case name. The command prints either plain text
// (the answer) or a kritrim.Result as JSON, which keeps tool calls for
// trajectory checks. Experiments with custom evaluators are Go code; see
// package evals.
package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/ganeshdipdumbare/kritrim"
	"github.com/ganeshdipdumbare/kritrim/evals"
)

func main() {
	os.Exit(run(context.Background(), os.Args[1:], os.Stdin, os.Stdout, os.Stderr))
}

const usageText = `usage:
  kritrim-evals generate -model provider:model [-context file] [-n 5] [-o cases.jsonl]
  kritrim-evals run -cases cases.jsonl [evaluators] [-o report.json] -- command [args...]
  kritrim-evals traces [evaluators] [-o report.json] trace.jsonl
  kritrim-evals report [--json] report.json
  kritrim-evals diagnose report.json

evaluators: -contains -exact -json -no-error -regex PATTERN -max-steps
            -max-tokens N -judge criterion[,criterion] -judge-model provider:model -detect
providers:  openai, responses, anthropic, gemini, bedrock, ollama, deepseek,
            groq, mistral, openrouter
`

// exit codes: 0 pass, 1 an evaluation failed, 2 usage or I/O error.
func run(ctx context.Context, args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	if len(args) < 1 {
		fmt.Fprint(stderr, usageText)
		return 2
	}
	var err error
	code := 0
	switch args[0] {
	case "generate":
		err = generate(ctx, args[1:], stdin, stdout)
	case "run":
		code, err = runCases(ctx, args[1:], stdout)
	case "traces":
		code, err = traces(ctx, args[1:], stdin, stdout)
	case "report":
		code, err = report(args[1:], stdout)
	case "diagnose":
		err = diagnose(args[1:], stdout)
	case "-h", "--help", "help":
		fmt.Fprint(stdout, usageText)
		return 0
	default:
		fmt.Fprint(stderr, usageText)
		return 2
	}
	if err != nil {
		fmt.Fprintln(stderr, "kritrim-evals:", err)
		return 2
	}
	return code
}

// parseModel builds a model from "provider:model".
func parseModel(spec string) (kritrim.Model, error) {
	provider, name, ok := strings.Cut(spec, ":")
	if !ok || name == "" {
		return nil, fmt.Errorf("model %q: want provider:model, e.g. openai:gpt-4.1", spec)
	}
	switch provider {
	case "openai":
		return kritrim.OpenAI(name), nil
	case "responses":
		return kritrim.OpenAIResponses(name), nil
	case "anthropic":
		return kritrim.Anthropic(name), nil
	case "gemini":
		return kritrim.Gemini(name), nil
	case "bedrock":
		return kritrim.Bedrock(name), nil
	case "ollama":
		return kritrim.Ollama(name), nil
	case "deepseek":
		return kritrim.DeepSeek(name), nil
	case "groq":
		return kritrim.Groq(name), nil
	case "mistral":
		return kritrim.Mistral(name), nil
	case "openrouter":
		return kritrim.OpenRouter(name), nil
	}
	return nil, fmt.Errorf("unknown provider %q", provider)
}

func generate(ctx context.Context, args []string, stdin io.Reader, stdout io.Writer) error {
	fs := flag.NewFlagSet("generate", flag.ContinueOnError)
	modelSpec := fs.String("model", "", "provider:model that writes the cases (required)")
	contextFile := fs.String("context", "-", "file describing the agent; - reads stdin")
	n := fs.Int("n", 5, "number of cases")
	out := fs.String("o", "", "write cases here instead of stdout")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *modelSpec == "" {
		return errors.New("generate: -model is required")
	}
	model, err := parseModel(*modelSpec)
	if err != nil {
		return err
	}
	var desc []byte
	if *contextFile == "-" {
		desc, err = io.ReadAll(stdin)
	} else {
		desc, err = os.ReadFile(*contextFile)
	}
	if err != nil {
		return err
	}
	cases, err := evals.GenerateCases(ctx, model, string(desc), *n)
	if err != nil {
		return err
	}
	return withOutput(*out, stdout, func(w io.Writer) error {
		enc := json.NewEncoder(w)
		for _, c := range cases {
			if err := enc.Encode(c); err != nil {
				return err
			}
		}
		return nil
	})
}

// evalFlags are the evaluator flags shared by run and traces.
type evalFlags struct {
	contains, exact, jsonFields, noError, maxSteps, detect bool
	regex, judge, judgeModel                               string
	maxTokens                                              int
}

func (f *evalFlags) register(fs *flag.FlagSet) {
	fs.BoolVar(&f.contains, "contains", false, "output contains the expected text")
	fs.BoolVar(&f.exact, "exact", false, "output equals the expected text")
	fs.BoolVar(&f.jsonFields, "json", false, "output JSON has the expected fields")
	fs.BoolVar(&f.noError, "no-error", false, "the run did not fail")
	fs.BoolVar(&f.maxSteps, "max-steps", false, "the run did not hit its step limit")
	fs.BoolVar(&f.detect, "detect", false, "run the failure detectors")
	fs.StringVar(&f.regex, "regex", "", "output matches this pattern")
	fs.StringVar(&f.judge, "judge", "", "built-in criteria to judge, comma separated (faithful, correct, ...)")
	fs.StringVar(&f.judgeModel, "judge-model", "", "provider:model for -judge")
	fs.IntVar(&f.maxTokens, "max-tokens", 0, "the run used at most this many tokens")
}

func (f *evalFlags) evaluators() ([]evals.Evaluator, error) {
	var out []evals.Evaluator
	if f.contains {
		out = append(out, evals.Contains())
	}
	if f.exact {
		out = append(out, evals.Exact())
	}
	if f.jsonFields {
		out = append(out, evals.JSONFields())
	}
	if f.noError {
		out = append(out, evals.NoError())
	}
	if f.maxSteps {
		out = append(out, evals.MaxSteps())
	}
	if f.maxTokens > 0 {
		out = append(out, evals.MaxTokens(f.maxTokens))
	}
	if f.regex != "" {
		ev, err := evals.Regex(f.regex)
		if err != nil {
			return nil, err
		}
		out = append(out, ev)
	}
	if f.judge != "" {
		if f.judgeModel == "" {
			return nil, errors.New("-judge needs -judge-model")
		}
		model, err := parseModel(f.judgeModel)
		if err != nil {
			return nil, err
		}
		for _, name := range strings.Split(f.judge, ",") {
			c, ok := evals.CriterionByName(strings.TrimSpace(name))
			if !ok {
				return nil, fmt.Errorf("unknown criterion %q", name)
			}
			out = append(out, evals.Judge(c, model))
		}
	}
	if len(out) == 0 && !f.detect {
		out = append(out, evals.NoError())
	}
	return out, nil
}

func runCases(ctx context.Context, args []string, stdout io.Writer) (int, error) {
	fs := flag.NewFlagSet("run", flag.ContinueOnError)
	var ef evalFlags
	ef.register(fs)
	casesFile := fs.String("cases", "", "cases as JSON lines or a JSON array (required)")
	concurrency := fs.Int("concurrency", 1, "cases run at once")
	timeout := fs.Duration("timeout", 5*time.Minute, "per-case time limit")
	out := fs.String("o", "", "also write the JSON report here")
	if err := fs.Parse(args); err != nil {
		return 0, err
	}
	command := fs.Args()
	if *casesFile == "" || len(command) == 0 {
		return 0, errors.New("run: need -cases and a command after --")
	}
	cases, err := readCases(*casesFile)
	if err != nil {
		return 0, err
	}
	evs, err := ef.evaluators()
	if err != nil {
		return 0, err
	}
	exp := evals.Experiment{Cases: cases, Evaluators: evs, Concurrency: *concurrency, Detect: ef.detect}
	rep, err := exp.Run(ctx, func(ctx context.Context, c evals.Case) (*kritrim.Result, error) {
		return runCommand(ctx, command, c, *timeout)
	})
	if err != nil {
		return 0, err
	}
	rep.Name = "run"
	return finish(rep, *out, stdout)
}

// runCommand runs one case through the agent program.
func runCommand(ctx context.Context, command []string, c evals.Case, timeout time.Duration) (*kritrim.Result, error) {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, command[0], command[1:]...)
	cmd.Stdin = strings.NewReader(c.Input)
	cmd.Env = append(os.Environ(), "KRITRIM_CASE="+c.Name)
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		msg := strings.TrimSpace(stderr.String())
		if len(msg) > 500 {
			msg = msg[len(msg)-500:]
		}
		return nil, fmt.Errorf("%v: %s", err, msg)
	}
	raw := bytes.TrimSpace(stdout.Bytes())
	if len(raw) > 0 && raw[0] == '{' {
		var res kritrim.Result
		if err := json.Unmarshal(raw, &res); err == nil && (res.Stop != "" || res.Steps != nil || res.Messages != nil) {
			return &res, nil
		}
	}
	return &kritrim.Result{Text: string(raw), Stop: kritrim.StopDone}, nil
}

func readCases(path string) ([]evals.Case, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	b = bytes.TrimSpace(b)
	var cases []evals.Case
	if len(b) > 0 && b[0] == '[' {
		if err := json.Unmarshal(b, &cases); err != nil {
			return nil, fmt.Errorf("%s: %w", path, err)
		}
	} else {
		sc := bufio.NewScanner(bytes.NewReader(b))
		sc.Buffer(make([]byte, 0, 64*1024), 16<<20)
		for n := 1; sc.Scan(); n++ {
			line := bytes.TrimSpace(sc.Bytes())
			if len(line) == 0 {
				continue
			}
			var c evals.Case
			if err := json.Unmarshal(line, &c); err != nil {
				return nil, fmt.Errorf("%s:%d: %w", path, n, err)
			}
			cases = append(cases, c)
		}
		if err := sc.Err(); err != nil {
			return nil, err
		}
	}
	for i := range cases {
		if cases[i].Name == "" {
			cases[i].Name = fmt.Sprintf("case-%d", i+1)
		}
	}
	if len(cases) == 0 {
		return nil, fmt.Errorf("%s: no cases", path)
	}
	return cases, nil
}

func traces(ctx context.Context, args []string, stdin io.Reader, stdout io.Writer) (int, error) {
	fs := flag.NewFlagSet("traces", flag.ContinueOnError)
	var ef evalFlags
	ef.register(fs)
	out := fs.String("o", "", "also write the JSON report here")
	if err := fs.Parse(args); err != nil {
		return 0, err
	}
	if fs.NArg() != 1 {
		return 0, errors.New("traces: need one trace file (- for stdin)")
	}
	var r io.Reader = stdin
	if fs.Arg(0) != "-" {
		f, err := os.Open(fs.Arg(0))
		if err != nil {
			return 0, err
		}
		defer f.Close()
		r = f
	}
	results, err := evals.FromTrace(r)
	if err != nil {
		return 0, err
	}
	evs, err := ef.evaluators()
	if err != nil {
		return 0, err
	}
	rep := evals.Experiment{Evaluators: evs, Detect: ef.detect}.Score(ctx, results)
	rep.Name = "traces"
	return finish(rep, *out, stdout)
}

func finish(rep *evals.Report, out string, stdout io.Writer) (int, error) {
	if out != "" {
		if err := withOutput(out, nil, rep.WriteJSON); err != nil {
			return 0, err
		}
	}
	fmt.Fprint(stdout, rep.Summary())
	if !rep.OK() {
		return 1, nil
	}
	return 0, nil
}

func withOutput(path string, stdout io.Writer, write func(io.Writer) error) error {
	if path == "" {
		return write(stdout)
	}
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	if err := write(f); err != nil {
		f.Close()
		return err
	}
	return f.Close()
}

func readReport(args []string, name string) (*evals.Report, bool, error) {
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	jsonOut := fs.Bool("json", false, "write JSON")
	if err := fs.Parse(args); err != nil {
		return nil, false, err
	}
	if fs.NArg() != 1 {
		return nil, false, fmt.Errorf("%s: need one report file", name)
	}
	f, err := os.Open(fs.Arg(0))
	if err != nil {
		return nil, false, err
	}
	defer f.Close()
	rep, err := evals.ReadReport(f)
	return rep, *jsonOut, err
}

func report(args []string, stdout io.Writer) (int, error) {
	rep, jsonOut, err := readReport(args, "report")
	if err != nil {
		return 0, err
	}
	if jsonOut {
		_ = rep.WriteJSON(stdout)
	} else {
		fmt.Fprint(stdout, rep.Summary())
	}
	if !rep.OK() {
		return 1, nil
	}
	return 0, nil
}

func diagnose(args []string, stdout io.Writer) error {
	rep, _, err := readReport(args, "diagnose")
	if err != nil {
		return err
	}
	for _, r := range rep.Results {
		if len(r.Findings) == 0 {
			continue
		}
		fmt.Fprintf(stdout, "%s\n", r.Case.Name)
		for _, f := range r.Findings {
			fmt.Fprintf(stdout, "  %s: %s (step %d)\n", f.Kind, f.Detail, f.Step)
		}
	}
	return nil
}
