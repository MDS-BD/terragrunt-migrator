//go:build darwin || linux

package terragrunt

import (
	"errors"
	"os/exec"
	"syscall"
)

func configureProcessGroup(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
}

func signalProcessGroup(processGroupID int, signal syscall.Signal) error {
	err := syscall.Kill(-processGroupID, signal)
	if errors.Is(err, syscall.ESRCH) {
		return nil
	}
	return err
}

func processGroupExists(processGroupID int) (bool, error) {
	err := syscall.Kill(-processGroupID, 0)
	if errors.Is(err, syscall.ESRCH) {
		return false, nil
	}
	if errors.Is(err, syscall.EPERM) {
		return true, nil
	}
	return err == nil, err
}
