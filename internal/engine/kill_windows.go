//go:build windows

package engine

import (
	"os/exec"
	"strconv"
	"syscall"
)

func ownGroup(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{CreationFlags: syscall.CREATE_NEW_PROCESS_GROUP}
}

// killTree asks taskkill to end the service and its children, then kills the
// process directly in case taskkill is missing or was too late.
func killTree(cmd *exec.Cmd) error {
	tree := exec.Command("taskkill", "/T", "/F", "/PID", strconv.Itoa(cmd.Process.Pid)).Run()
	if err := cmd.Process.Kill(); err != nil && tree != nil {
		return err
	}
	return nil
}
