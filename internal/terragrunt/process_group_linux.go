//go:build linux

package terragrunt

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

func processGroupHasLiveProcesses(processGroupID int) (bool, error) {
	processes, err := processGroupProcesses(processGroupID)
	return len(processes) > 0, err
}

func processGroupHasLiveDescendants(processGroupID, leaderPID int) (bool, error) {
	processes, err := processGroupProcesses(processGroupID)
	if err != nil {
		return false, err
	}
	for _, process := range processes {
		if process.pid != leaderPID {
			return true, nil
		}
	}
	return false, nil
}

type processGroupMember struct {
	pid   int
	state byte
}

func processGroupProcesses(processGroupID int) ([]processGroupMember, error) {
	entries, err := os.ReadDir("/proc")
	if err != nil {
		return nil, fmt.Errorf("read process table: %w", err)
	}
	processes := make([]processGroupMember, 0, 4)
	for _, entry := range entries {
		pid, err := strconv.Atoi(entry.Name())
		if err != nil {
			continue
		}
		stat, err := os.ReadFile(filepath.Join("/proc", entry.Name(), "stat"))
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return nil, fmt.Errorf("read process %s status: %w", entry.Name(), err)
		}
		commandEnd := strings.LastIndexByte(string(stat), ')')
		if commandEnd < 0 {
			return nil, fmt.Errorf("parse process %s status", entry.Name())
		}
		fields := strings.Fields(string(stat[commandEnd+1:]))
		if len(fields) < 3 {
			return nil, fmt.Errorf("parse process %s status", entry.Name())
		}
		groupID, err := strconv.Atoi(fields[2])
		if err != nil {
			return nil, fmt.Errorf("parse process %s group: %w", entry.Name(), err)
		}
		if groupID == processGroupID && fields[0] != "Z" && fields[0] != "X" {
			processes = append(processes, processGroupMember{pid: pid, state: fields[0][0]})
		}
	}
	return processes, nil
}
