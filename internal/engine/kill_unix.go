//go:build unix

package engine

import (
	"os/exec"
	"syscall"
)

// ownGroup starts the service in its own process group, so killing the group
// also reaches anything it spawned: "npm start", "go run", and shell scripts
// all serve from a child process.
func ownGroup(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
}

// killTree kills the service and every process it started. A SIGKILL can't
// be caught, which is the point: this stands in for a crash.
func killTree(cmd *exec.Cmd) error {
	if err := syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL); err == nil {
		return nil
	}
	return cmd.Process.Kill()
}
