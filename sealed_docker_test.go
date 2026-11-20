package kritrim

import (
	"context"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"
)

// TestSealedDocker runs a sealed agent in a real container: the box must
// survive New returning, run a risky tool after a host approval, and answer.
func TestSealedDocker(t *testing.T) { testSealedDocker(t, DockerConfig{Image: "alpine:3.20"}) }
func TestSealedDockerStrict(t *testing.T) {
	testSealedDocker(t, DockerConfig{Image: "alpine:3.20", Strict: true})
}

func testSealedDocker(t *testing.T, dc DockerConfig) {
	if testing.Short() {
		t.Skip("short mode")
	}
	if _, err := exec.LookPath("docker"); err != nil {
		t.Skip("docker not installed")
	}
	if err := exec.Command("docker", "info").Run(); err != nil {
		t.Skip("docker daemon not running")
	}
	arch := "amd64"
	if runtime.GOARCH == "arm64" {
		arch = "arm64"
	}
	bin := filepath.Join(t.TempDir(), "box")
	build := exec.Command("go", "build", "-o", bin, "./internal/boxtest")
	build.Env = append(build.Environ(), "GOOS=linux", "GOARCH="+arch, "CGO_ENABLED=0")
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build box: %v: %s", err, out)
	}
	refund := Risky(NewTool("refund", "refund", func(ctx context.Context, in struct {
		Order string `json:"order"`
	}) (string, error) {
		return "refunded " + in.Order, nil
	}))
	var approved int
	a, err := New(Config{
		Model: Mock(TurnTool("refund", map[string]any{"order": "A-17"}), Turn{Text: "done"}),
		Tools: []Tool{refund},
		Approve: func(ctx context.Context, req Approval) (Decision, error) {
			approved++
			return Approve(), nil
		},
		Sealed: &Sealed{Sandbox: Docker(dc), Binary: bin},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	res, err := a.Run(context.Background(), "refund A-17")
	if err != nil {
		t.Fatal(err)
	}
	if res.Text != "done" || approved != 1 || res.Steps[0].ToolResults[0].Content != "refunded A-17" {
		t.Fatalf("approved=%d res=%+v", approved, res)
	}
}
