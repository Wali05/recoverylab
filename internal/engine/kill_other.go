//go:build !unix && !windows

package engine

import "os/exec"

func ownGroup(*exec.Cmd) {}

func killTree(cmd *exec.Cmd) error { return cmd.Process.Kill() }
