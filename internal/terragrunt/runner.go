package terragrunt

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"slices"
	"strings"
	"syscall"
	"time"

	"github.com/hashicorp/terraform-json"
	"github.com/rs/zerolog"
)

type Runner struct {
	Stdin  io.Reader
	Stdout io.Writer
	Stderr io.Writer
}

func (runner *Runner) streams() (io.Reader, io.Writer, io.Writer) {
	in, out, errOut := runner.Stdin, runner.Stdout, runner.Stderr
	if in == nil {
		in = os.Stdin
	}
	if out == nil {
		out = os.Stdout
	}
	if errOut == nil {
		errOut = os.Stderr
	}
	return in, out, errOut
}

func (runner *Runner) Init(ctx context.Context, cwd string, dryRun bool) error {
	commandLine := "terragrunt init -upgrade"
	if dryRun {
		zerolog.Ctx(ctx).Info().Str("command", commandLine).Msg("Dry run: would initialize with Terragrunt")
		return nil
	}
	zerolog.Ctx(ctx).Info().Str("command", commandLine).Msg("Initializing with Terragrunt")
	cmd := command(ctx, "terragrunt", "init", "-upgrade")
	cmd.Dir = cwd
	cmd.Env = setEnv(os.Environ(), "TF_IN_AUTOMATION", "1")
	cmd.Stdin, cmd.Stdout, cmd.Stderr = runner.streamsInput(), runner.streamsError(), runner.streamsError()
	if err := runCommand(ctx, cmd); err != nil {
		if !isCancellation(err) {
			zerolog.Ctx(ctx).Error().Stack().Err(err).Msg("Terragrunt init failed")
		}
		return fmt.Errorf("Terragrunt init failed: %w", err)
	}
	return nil
}

func (runner *Runner) Apply(ctx context.Context, cwd string, variables map[string]string, autoApply, dryRun bool) error {
	args := []string{"apply"}
	if autoApply {
		args = append(args, "-auto-approve")
	}
	keys := make([]string, 0, len(variables))
	for key := range variables {
		keys = append(keys, key)
	}
	slices.Sort(keys)
	for _, key := range keys {
		args = append(args, "-var", key+"="+variables[key])
	}
	commandLine := "terragrunt " + strings.Join(args, " ")
	if dryRun {
		zerolog.Ctx(ctx).Info().Str("command", commandLine).Msg("Dry run: would apply with Terragrunt")
		return nil
	}
	zerolog.Ctx(ctx).Info().Str("command", commandLine).Msg("Applying with Terragrunt")
	cmd := command(ctx, "terragrunt", args...)
	cmd.Dir = cwd
	cmd.Env = os.Environ()
	cmd.Stdin, cmd.Stdout, cmd.Stderr = runner.streams()
	if err := runCommand(ctx, cmd); err != nil {
		if !isCancellation(err) {
			zerolog.Ctx(ctx).Error().Stack().Err(err).Msg("Terragrunt apply failed")
		}
		return fmt.Errorf("terragrunt apply failed: %w", err)
	}
	return nil
}

func (runner *Runner) Show(ctx context.Context, cwd string) (*tfjson.State, error) {
	cmd := command(ctx, "terragrunt", "show", "-json")
	cmd.Dir = cwd
	cmd.Stdin = nil
	cmd.Stderr = runner.streamsError()
	var stdout bytes.Buffer
	cmd.Stdout = &stdout
	if err := runCommand(ctx, cmd); err != nil {
		return nil, fmt.Errorf("terragrunt show failed: %w", err)
	}
	state := new(tfjson.State)
	if err := json.Unmarshal(stdout.Bytes(), state); err != nil {
		return nil, fmt.Errorf("decode terragrunt show -json: %w", err)
	}
	return state, nil
}

func (runner *Runner) streamsInput() io.Reader { in, _, _ := runner.streams(); return in }
func (runner *Runner) streamsError() io.Writer { _, _, err := runner.streams(); return err }

func setEnv(env []string, key, value string) []string {
	out := make([]string, 0, len(env)+1)
	prefix := key + "="
	for _, item := range env {
		if len(item) >= len(prefix) && item[:len(prefix)] == prefix {
			continue
		}
		out = append(out, item)
	}
	return append(out, prefix+value)
}

const processShutdownGrace = 30 * time.Second

const commandWaitDelay = 10 * time.Second

func command(_ context.Context, name string, args ...string) *exec.Cmd {
	cmd := exec.Command(name, args...)
	configureProcessGroup(cmd)
	cmd.WaitDelay = commandWaitDelay
	return cmd
}

func isCancellation(err error) bool {
	return errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded)
}

func runCommand(ctx context.Context, cmd *exec.Cmd) error {
	return runCommandWithGrace(ctx, cmd, processShutdownGrace)
}

func runCommandWithGrace(ctx context.Context, cmd *exec.Cmd, grace time.Duration) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	startInput, stopInput, interrupted, err := prepareTerminalInput(cmd)
	if err != nil {
		return fmt.Errorf("prepare Terragrunt input: %w", err)
	}
	defer stopInput()
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := cmd.Start(); err != nil {
		return err
	}
	startInput()
	wait := make(chan error, 1)
	go func() { wait <- cmd.Wait() }()

	select {
	case waitErr := <-wait:
		select {
		case <-interrupted:
			cleanupErr := shutdownProcessGroup(cmd.Process, grace)
			return errors.Join(context.Canceled, waitErr, cleanupErr)
		default:
		}
		if ctx.Err() != nil {
			cleanupErr := shutdownProcessGroup(cmd.Process, grace)
			return errors.Join(ctx.Err(), waitErr, cleanupErr)
		}
		live, probeErr := processGroupHasLiveProcesses(cmd.Process.Pid)
		if probeErr == nil && !live {
			return waitErr
		}
		cleanupErr := shutdownProcessGroup(cmd.Process, grace)
		return errors.Join(waitErr, probeErr, cleanupErr)
	case <-ctx.Done():
		cleanupErr := shutdownProcessGroup(cmd.Process, grace)
		waitErr := <-wait
		return errors.Join(ctx.Err(), waitErr, cleanupErr)
	case <-interrupted:
		cleanupErr := shutdownProcessGroup(cmd.Process, grace)
		waitErr := <-wait
		return errors.Join(context.Canceled, waitErr, cleanupErr)
	}
}

const processGroupKillWait = 2 * time.Second

func shutdownProcessGroup(process *os.Process, grace time.Duration) error {
	processGroupID := process.Pid
	exists, err := processGroupExists(processGroupID)
	if err != nil || !exists {
		return err
	}
	descendants, err := processGroupHasLiveDescendants(processGroupID, processGroupID)
	if err != nil {
		return errors.Join(err, killProcessGroupAndWait(processGroupID))
	}
	deadline := time.Now().Add(grace)
	if descendants {
		// A PTY session leader exiting first sends SIGHUP to the session's
		// foreground group. Hold Terragrunt until its child tree has handled
		// the one group-wide interrupt, then let the leader exit.
		if err := signalProcessGroup(processGroupID, syscall.SIGCONT); err != nil {
			return errors.Join(err, killProcessGroupAndWait(processGroupID))
		}
		if err := process.Signal(syscall.SIGSTOP); err != nil {
			if !errors.Is(err, os.ErrProcessDone) {
				return errors.Join(err, killProcessGroupAndWait(processGroupID))
			}
			descendants = false
		}
	}
	if err := signalProcessGroup(processGroupID, syscall.SIGINT); err != nil {
		return errors.Join(err, killProcessGroupAndWait(processGroupID))
	}
	if descendants {
		remaining := time.Until(deadline)
		if remaining < 0 {
			remaining = 0
		}
		drained, err := waitForProcessGroupDescendants(processGroupID, processGroupID, remaining)
		if err != nil || !drained {
			return errors.Join(err, killProcessGroupAndWait(processGroupID))
		}
		if err := process.Signal(syscall.SIGCONT); err != nil && !errors.Is(err, os.ErrProcessDone) {
			return errors.Join(err, killProcessGroupAndWait(processGroupID))
		}
	}
	remaining := time.Until(deadline)
	if remaining < 0 {
		remaining = 0
	}
	drained, err := waitForProcessGroup(processGroupID, remaining)
	if err != nil || !drained {
		return errors.Join(err, killProcessGroupAndWait(processGroupID))
	}
	return nil
}

func killProcessGroupAndWait(processGroupID int) error {
	if err := signalProcessGroup(processGroupID, syscall.SIGKILL); err != nil {
		return err
	}
	if drained, err := waitForProcessGroup(processGroupID, processGroupKillWait); err != nil {
		return err
	} else if !drained {
		return fmt.Errorf("process group %d remained after SIGKILL", processGroupID)
	}
	return nil
}

func waitForProcessGroupDescendants(processGroupID, leaderPID int, timeout time.Duration) (bool, error) {
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	ticker := time.NewTicker(20 * time.Millisecond)
	defer ticker.Stop()
	for {
		descendants, err := processGroupHasLiveDescendants(processGroupID, leaderPID)
		if err != nil || !descendants {
			return !descendants, err
		}
		select {
		case <-timer.C:
			return false, nil
		case <-ticker.C:
		}
	}
}

func waitForProcessGroup(processGroupID int, timeout time.Duration) (bool, error) {
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	ticker := time.NewTicker(20 * time.Millisecond)
	defer ticker.Stop()
	for {
		exists, err := processGroupExists(processGroupID)
		if err != nil || !exists {
			return !exists, err
		}
		live, err := processGroupHasLiveProcesses(processGroupID)
		if err != nil || !live {
			return !live, err
		}
		select {
		case <-timer.C:
			return false, nil
		case <-ticker.C:
		}
	}
}
