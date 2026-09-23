package engine

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"recoverylab/internal/config"
)

type tailBuffer struct {
	mu sync.Mutex
	b  []byte
}

func (t *tailBuffer) Write(p []byte) (int, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.b = append(t.b, p...)
	if len(t.b) > 32*1024 {
		t.b = append([]byte(nil), t.b[len(t.b)-32*1024:]...)
	}
	return len(p), nil
}

func (t *tailBuffer) String() string {
	t.mu.Lock()
	defer t.mu.Unlock()
	return strings.TrimSpace(string(t.b))
}

type process struct {
	config    config.Service
	sourceDir string
	cmd       *exec.Cmd
	done      chan struct{}
	logs      *tailBuffer
}

func newProcess(c config.Service, sourceDir string) *process {
	return &process{config: c, sourceDir: sourceDir}
}

func (p *process) Start(parent context.Context) error {
	if p.cmd != nil {
		return fmt.Errorf("service is already started")
	}
	// A pre-existing service must not be mistaken for the child we are starting.
	preflight := &http.Client{Timeout: 300 * time.Millisecond, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	if req, err := http.NewRequestWithContext(parent, http.MethodGet, p.config.ReadyURL, nil); err == nil {
		if resp, err := preflight.Do(req); err == nil {
			_ = resp.Body.Close()
			if resp.StatusCode >= 200 && resp.StatusCode < 300 {
				return fmt.Errorf("readiness URL was already responding before the managed service started")
			}
		}
	}
	workingDir := p.sourceDir
	if workingDir == "" {
		workingDir = "."
	}
	if p.config.WorkingDir != "" {
		if filepath.IsAbs(p.config.WorkingDir) {
			workingDir = p.config.WorkingDir
		} else {
			workingDir = filepath.Join(workingDir, p.config.WorkingDir)
		}
	}
	command := p.config.Command[0]
	if !filepath.IsAbs(command) && strings.ContainsAny(command, `/\`) {
		absDir, err := filepath.Abs(workingDir)
		if err != nil {
			return err
		}
		command = filepath.Join(absDir, command)
	}
	cmd := exec.Command(command, p.config.Command[1:]...)
	cmd.Dir = workingDir
	logs := &tailBuffer{}
	cmd.Stdout = logs
	cmd.Stderr = logs
	if err := cmd.Start(); err != nil {
		return err
	}
	done := make(chan struct{})
	p.cmd, p.logs, p.done = cmd, logs, done
	go func() { _ = cmd.Wait(); close(done) }()
	readyCtx, cancel := context.WithTimeout(parent, p.config.StartupDuration())
	defer cancel()
	client := &http.Client{Timeout: 500 * time.Millisecond, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	for {
		req, err := http.NewRequestWithContext(readyCtx, http.MethodGet, p.config.ReadyURL, nil)
		if err != nil {
			_ = p.Stop()
			return err
		}
		resp, err := client.Do(req)
		if err == nil {
			_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 1024))
			_ = resp.Body.Close()
			if resp.StatusCode >= 200 && resp.StatusCode < 300 {
				return nil
			}
		}
		select {
		case <-p.done:
			p.cmd = nil
			return fmt.Errorf("service exited before readiness; logs: %s", logs.String())
		case <-readyCtx.Done():
			_ = p.Stop()
			return fmt.Errorf("readiness timeout: %w; logs: %s", readyCtx.Err(), logs.String())
		case <-ticker.C:
		}
	}
}

// Crash requires the managed process to still be alive, then forcibly kills it.
func (p *process) Crash() error {
	if p.cmd == nil {
		return fmt.Errorf("managed service is not running")
	}
	cmd, done := p.cmd, p.done
	select {
	case <-done:
		p.cmd = nil
		return fmt.Errorf("managed service exited before the fault could be injected")
	default:
	}
	if err := cmd.Process.Kill(); err != nil {
		return fmt.Errorf("kill managed service: %w", err)
	}
	p.cmd = nil
	select {
	case <-done:
		return nil
	case <-time.After(5 * time.Second):
		return fmt.Errorf("managed service did not exit after kill")
	}
}

func (p *process) Stop() error {
	if p.cmd == nil {
		return nil
	}
	cmd, done := p.cmd, p.done
	p.cmd = nil
	select {
	case <-done:
		return nil
	default:
	}
	if err := cmd.Process.Kill(); err != nil {
		select {
		case <-done:
			return nil
		default:
			return err
		}
	}
	select {
	case <-done:
		return nil
	case <-time.After(5 * time.Second):
		return fmt.Errorf("service process did not exit after kill")
	}
}
