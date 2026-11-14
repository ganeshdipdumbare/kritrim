package main

import (
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/ganeshdipdumbare/kritrim/internal/vmagent"
)

func TestServeUnix(t *testing.T) {
	dir, err := os.MkdirTemp("/tmp", "kva")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)
	sock := filepath.Join(dir, "a.sock")
	go serve("unix:"+sock, []string{"PATH=/usr/bin:/bin"})
	var c net.Conn
	for i := 0; i < 100; i++ {
		if c, err = net.Dial("unix", sock); err == nil {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	r, err := vmagent.Call(c, vmagent.Request{Op: "exec", Argv: []string{"sh", "-c", "echo $PATH; exit 4"}})
	if err != nil || string(r.Stdout) != "/usr/bin:/bin\n" || r.Exit != 4 {
		t.Fatalf("%+v %v", r, err)
	}
	if err := serve("tcp:1", nil); err == nil {
		t.Fatal("unknown listener accepted")
	}
}
