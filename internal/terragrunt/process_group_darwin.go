//go:build darwin

package terragrunt

import (
	"fmt"

	"golang.org/x/sys/unix"
)

func processGroupHasLiveProcesses(processGroupID int) (bool, error) {
	processes, err := unix.SysctlKinfoProcSlice("kern.proc.pgrp", processGroupID)
	if err != nil {
		return false, fmt.Errorf("list process group %d: %w", processGroupID, err)
	}
	for _, process := range processes {
		if process.Proc.P_stat != 5 { // SZOMB
			return true, nil
		}
	}
	return false, nil
}

func processGroupHasLiveDescendants(processGroupID, leaderPID int) (bool, error) {
	processes, err := unix.SysctlKinfoProcSlice("kern.proc.pgrp", processGroupID)
	if err != nil {
		return false, fmt.Errorf("list process group %d: %w", processGroupID, err)
	}
	for _, process := range processes {
		if process.Proc.P_stat != 5 && int(process.Proc.P_pid) != leaderPID {
			return true, nil
		}
	}
	return false, nil
}
