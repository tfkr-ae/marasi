//go:build unix

package main

import (
	"os/exec"
	"syscall"
)

// configureDetachedProcess puts the child in a new session so it survives parent exit.
func configureDetachedProcess(command *exec.Cmd) {
	command.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
}
