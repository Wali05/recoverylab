//go:build unix

package engine

import (
	"context"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/Wali05/recoverylab/internal/config"
)

// "npm start", "go run", and shell scripts serve from a child process. A
// crash has to take that child down too, or the restart finds the port busy
// and the old server outlives the run.
func TestCrashKillsAServiceStartedThroughAWrapper(t *testing.T) {
	sh, err := exec.LookPath("sh")
	if err != nil {
		t.Skip("no sh on this machine")
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := listener.Addr().String()
	_ = listener.Close()
	t.Setenv("RECOVERYLAB_HELPER", "1")
	t.Setenv("RECOVERYLAB_MODE", "correct")
	t.Setenv("RECOVERYLAB_ADDR", addr)
	t.Setenv("RECOVERYLAB_JOURNAL", filepath.Join(t.TempDir(), "journal.jsonl"))
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	base := "http://" + addr
	// The trailing "true" stops the shell from exec'ing the server in place.
	service := &config.Service{Command: []string{sh, "-c", `"$0" -test.run='^TestHelperService$'; true`, exe}, ReadyURL: base + "/health", StartupTimeout: "4s"}
	r := Run(context.Background(), scenario(base, "crash-after-ack", service))
	if r.Status != "PASS" || !r.FaultInjected {
		t.Fatalf("unexpected report: %+v", r)
	}
	client := &http.Client{Timeout: 300 * time.Millisecond}
	if resp, err := client.Get(base + "/health"); err == nil {
		_ = resp.Body.Close()
		t.Fatal("the wrapped server is still running after the experiment")
	}
}
