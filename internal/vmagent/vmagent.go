// Package vmagent is the protocol between the Firecracker sandbox on the
// host and kritrim-vmagent inside the microVM. Every operation uses its own
// connection: a JSON request line, then either a JSON reply line (exec,
// read, write, ping) or, for start, a stream of frames in both directions.
package vmagent

import (
	"bufio"
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"time"
)

// Port is the vsock port the agent listens on.
const Port = 52

// Request is the first line of every connection.
type Request struct {
	Op      string   `json:"op"` // ping | exec | read | write | start
	Argv    []string `json:"argv,omitempty"`
	Dir     string   `json:"dir,omitempty"`
	Env     []string `json:"env,omitempty"`
	Stdin   []byte   `json:"stdin,omitempty"`
	HasIn   bool     `json:"has_stdin,omitempty"`
	Timeout int64    `json:"timeout_ms,omitempty"`
	Path    string   `json:"path,omitempty"`
	Data    []byte   `json:"data,omitempty"`
	Mode    uint32   `json:"mode,omitempty"`
}

// Reply answers ping, exec, read and write.
type Reply struct {
	Error  string `json:"error,omitempty"`
	Stdout []byte `json:"stdout,omitempty"`
	Stderr []byte `json:"stderr,omitempty"`
	Exit   int    `json:"exit"`
	Data   []byte `json:"data,omitempty"`
}

// Frame types for start.
const (
	FrameStdin    byte = 0 // host → agent
	FrameClose    byte = 1 // host → agent: close stdin
	FrameKill     byte = 2 // host → agent
	FrameStdout   byte = 3 // agent → host
	FrameStderr   byte = 4 // agent → host
	FrameExit     byte = 5 // agent → host: 4-byte big-endian exit code
	FrameStartErr byte = 6 // agent → host: the process did not start
)

// maxFrame bounds one frame, and a request line.
const maxFrame = 64 << 20

// WriteFrame writes one frame.
func WriteFrame(w io.Writer, typ byte, payload []byte) error {
	var h [5]byte
	h[0] = typ
	binary.BigEndian.PutUint32(h[1:], uint32(len(payload)))
	if _, err := w.Write(h[:]); err != nil {
		return err
	}
	_, err := w.Write(payload)
	return err
}

// ReadFrame reads one frame.
func ReadFrame(r io.Reader) (byte, []byte, error) {
	var h [5]byte
	if _, err := io.ReadFull(r, h[:]); err != nil {
		return 0, nil, err
	}
	n := binary.BigEndian.Uint32(h[1:])
	if n > maxFrame {
		return 0, nil, fmt.Errorf("vmagent: frame of %d bytes is too large", n)
	}
	b := make([]byte, n)
	if _, err := io.ReadFull(r, b); err != nil {
		return 0, nil, err
	}
	return h[0], b, nil
}

// Serve handles one connection. env is the environment every command
// starts from (the agent passes a minimal one).
func Serve(conn io.ReadWriteCloser, env []string) {
	defer conn.Close()
	br := bufio.NewReaderSize(conn, 64*1024)
	line, err := readLine(br)
	if err != nil {
		return
	}
	var req Request
	if err := json.Unmarshal(line, &req); err != nil {
		writeReply(conn, Reply{Error: "bad request: " + err.Error()})
		return
	}
	switch req.Op {
	case "ping":
		writeReply(conn, Reply{})
	case "exec":
		writeReply(conn, execute(req, env))
	case "read":
		b, err := os.ReadFile(req.Path)
		if err != nil {
			writeReply(conn, Reply{Error: err.Error()})
			return
		}
		writeReply(conn, Reply{Data: b})
	case "write":
		writeReply(conn, write(req))
	case "start":
		start(req, env, br, conn)
	default:
		writeReply(conn, Reply{Error: "unknown op " + req.Op})
	}
}

func readLine(br *bufio.Reader) ([]byte, error) {
	var buf bytes.Buffer
	for {
		chunk, isPrefix, err := br.ReadLine()
		if err != nil {
			return nil, err
		}
		buf.Write(chunk)
		if buf.Len() > maxFrame {
			return nil, errors.New("vmagent: request too large")
		}
		if !isPrefix {
			return buf.Bytes(), nil
		}
	}
}

func writeReply(w io.Writer, r Reply) {
	b, _ := json.Marshal(r)
	_, _ = w.Write(append(b, '\n'))
}

func command(ctx context.Context, req Request, env []string) (*exec.Cmd, error) {
	if len(req.Argv) == 0 {
		return nil, errors.New("empty command")
	}
	cmd := exec.CommandContext(ctx, req.Argv[0], req.Argv[1:]...)
	cmd.Dir = req.Dir
	cmd.Env = append(append([]string(nil), env...), req.Env...)
	return cmd, nil
}

func execute(req Request, env []string) Reply {
	ctx := context.Background()
	if req.Timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, time.Duration(req.Timeout)*time.Millisecond)
		defer cancel()
	}
	cmd, err := command(ctx, req, env)
	if err != nil {
		return Reply{Error: err.Error()}
	}
	if req.HasIn {
		cmd.Stdin = bytes.NewReader(req.Stdin)
	}
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	err = cmd.Run()
	r := Reply{Stdout: out.Bytes(), Stderr: errb.Bytes()}
	var ee *exec.ExitError
	switch {
	case errors.As(err, &ee):
		r.Exit = ee.ExitCode()
	case err != nil:
		r.Error = err.Error()
	}
	return r
}

func write(req Request) Reply {
	mode := fs.FileMode(req.Mode).Perm()
	if mode == 0 {
		mode = 0o644
	}
	if err := os.MkdirAll(filepath.Dir(req.Path), 0o755); err != nil {
		return Reply{Error: err.Error()}
	}
	if err := os.WriteFile(req.Path, req.Data, mode); err != nil {
		return Reply{Error: err.Error()}
	}
	if err := os.Chmod(req.Path, mode); err != nil {
		return Reply{Error: err.Error()}
	}
	return Reply{}
}

// start runs a process and relays its stdio as frames until it exits.
func start(req Request, env []string, in io.Reader, conn io.Writer) {
	var wmu sync.Mutex
	send := func(typ byte, b []byte) {
		wmu.Lock()
		defer wmu.Unlock()
		_ = WriteFrame(conn, typ, b)
	}
	cmd, err := command(context.Background(), req, env)
	if err != nil {
		send(FrameStartErr, []byte(err.Error()))
		return
	}
	stdin, _ := cmd.StdinPipe()
	stdout, _ := cmd.StdoutPipe()
	stderr, _ := cmd.StderrPipe()
	if err := cmd.Start(); err != nil {
		send(FrameStartErr, []byte(err.Error()))
		return
	}
	go func() {
		for {
			typ, b, err := ReadFrame(in)
			if err != nil {
				// The host went away: nothing will read the output.
				_ = cmd.Process.Kill()
				return
			}
			switch typ {
			case FrameStdin:
				_, _ = stdin.Write(b)
			case FrameClose:
				_ = stdin.Close()
			case FrameKill:
				_ = cmd.Process.Kill()
			}
		}
	}()
	var wg sync.WaitGroup
	pump := func(r io.Reader, typ byte) {
		defer wg.Done()
		buf := make([]byte, 32*1024)
		for {
			n, err := r.Read(buf)
			if n > 0 {
				send(typ, buf[:n])
			}
			if err != nil {
				return
			}
		}
	}
	wg.Add(2)
	go pump(stdout, FrameStdout)
	go pump(stderr, FrameStderr)
	wg.Wait()
	code := 0
	var ee *exec.ExitError
	if err := cmd.Wait(); errors.As(err, &ee) {
		code = ee.ExitCode()
	}
	var b [4]byte
	binary.BigEndian.PutUint32(b[:], uint32(int32(code)))
	send(FrameExit, b[:])
}

// Call sends one request on conn and reads the reply.
func Call(conn io.ReadWriter, req Request) (Reply, error) {
	b, err := json.Marshal(req)
	if err != nil {
		return Reply{}, err
	}
	if _, err := conn.Write(append(b, '\n')); err != nil {
		return Reply{}, err
	}
	line, err := readLine(bufio.NewReaderSize(conn, 64*1024))
	if err != nil {
		return Reply{}, err
	}
	var r Reply
	if err := json.Unmarshal(line, &r); err != nil {
		return Reply{}, err
	}
	if r.Error != "" {
		return r, errors.New(r.Error)
	}
	return r, nil
}
