package terragrunt

import (
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestRunnerDryRunNeedsNoExecutableAndDoesNotMutate(t *testing.T) {
	marker := filepath.Join(t.TempDir(), "invoked")
	bin := t.TempDir()
	writeTerragrunt(t, bin, "#!/bin/sh\nprintf invoked > \"$MARKER\"\n")
	t.Setenv("PATH", bin)
	t.Setenv("MARKER", marker)
	runner := &Runner{}
	if err := runner.Init(context.Background(), t.TempDir(), true); err != nil {
		t.Errorf("Runner.Init() error = %v", err)
	}
	if err := runner.Apply(context.Background(), t.TempDir(), nil, false, true); err != nil {
		t.Errorf("Runner.Apply() error = %v", err)
	}
	if _, err := os.Stat(marker); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("dry-run invoked executable: stat marker error = %v", err)
	}
	t.Setenv("PATH", t.TempDir())
	if err := runner.Init(context.Background(), t.TempDir(), true); err != nil {
		t.Errorf("Runner.Init() without executable error = %v", err)
	}
	if err := runner.Apply(context.Background(), t.TempDir(), nil, false, true); err != nil {
		t.Errorf("Runner.Apply() without executable error = %v", err)
	}
}

func TestRunnerApplyExposesSubprocessExitStatus(t *testing.T) {
	bin := t.TempDir()
	writeTerragrunt(t, bin, "#!/bin/sh\nexit 7\n")
	t.Setenv("PATH", bin)
	err := (&Runner{}).Apply(context.Background(), t.TempDir(), nil, false, false)
	var exitErr *exec.ExitError
	if !errors.As(err, &exitErr) || exitErr.ExitCode() != 7 {
		t.Errorf("Apply error = %v, want subprocess exit status 7", err)
	}
}

func TestRunnerShowRejectsInvalidJSON(t *testing.T) {
	bin := t.TempDir()
	writeTerragrunt(t, bin, "#!/bin/sh\nprintf 'not json'\n")
	t.Setenv("PATH", bin)
	_, err := (&Runner{}).Show(context.Background(), t.TempDir())
	if err == nil || !strings.Contains(err.Error(), "decode terragrunt show -json") {
		t.Errorf("Show error = %v, want invalid JSON error", err)
	}
}

func TestRunnerCancelledContextPreventsInvocation(t *testing.T) {
	marker := filepath.Join(t.TempDir(), "invoked")
	bin := t.TempDir()
	writeTerragrunt(t, bin, "#!/bin/sh\nprintf invoked > \"$MARKER\"\n")
	t.Setenv("PATH", bin)
	t.Setenv("MARKER", marker)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	err := (&Runner{}).Apply(ctx, t.TempDir(), nil, false, false)
	if !errors.Is(err, context.Canceled) {
		t.Errorf("Apply error = %v, want context.Canceled", err)
	}
	if _, err := os.Stat(marker); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("cancelled context invoked executable: stat marker error = %v", err)
	}
}

func TestRunnerCancellationShutsDownProcessGroup(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	marker := filepath.Join(t.TempDir(), "child-terminated")
	ready := filepath.Join(t.TempDir(), "ready")
	testProcessTreeTerragrunt(t, "graceful-parent")
	t.Setenv("PROCESS_TREE_READY", ready)
	t.Setenv("PROCESS_TREE_MARKER", marker)
	cmd := command(ctx, "terragrunt", "apply")
	cmd.Stdout, cmd.Stderr = io.Discard, io.Discard
	go func() { done <- runCommandWithGrace(ctx, cmd, 500*time.Millisecond) }()
	finished := false
	defer func() {
		if !finished {
			cancel()
			<-done
		}
	}()
	waitForFile(t, ready)
	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Errorf("command error = %v, want context.Canceled", err)
	}
	finished = true
	if _, err := os.Stat(marker); err != nil {
		t.Errorf("child termination marker missing: %v", err)
	}
}

func TestRunnerCleansDescendantAfterLeaderExits(t *testing.T) {
	tempDir := t.TempDir()
	ready := filepath.Join(tempDir, "ready")
	marker := filepath.Join(tempDir, "child-terminated")
	testProcessTreeTerragrunt(t, "early-parent")
	t.Setenv("PROCESS_TREE_READY", ready)
	t.Setenv("PROCESS_TREE_MARKER", marker)
	cmd := command(context.Background(), "terragrunt", "apply")
	cmd.Dir = tempDir
	cmd.WaitDelay = 50 * time.Millisecond
	cmd.Stdout, cmd.Stderr = io.Discard, io.Discard
	start := time.Now()
	if err := runCommandWithGrace(context.Background(), cmd, 500*time.Millisecond); err == nil {
		t.Error("command error = nil, want bounded inherited-stream wait error")
	}
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Errorf("command returned after %s, want bounded process-tree cleanup", elapsed)
	}
	if _, err := os.Stat(marker); err != nil {
		t.Errorf("descendant termination marker missing: %v", err)
	}
}

func TestRunnerEscalatesForStubbornDescendant(t *testing.T) {
	tempDir := t.TempDir()
	ready := filepath.Join(tempDir, "ready")
	output := filepath.Join(tempDir, "output")
	testProcessTreeTerragrunt(t, "stubborn-parent")
	t.Setenv("PROCESS_TREE_READY", ready)
	t.Setenv("PROCESS_TREE_OUTPUT", output)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	cmd := command(ctx, "terragrunt", "apply")
	cmd.Dir = tempDir
	cmd.Stdout, cmd.Stderr = io.Discard, io.Discard
	go func() { done <- runCommandWithGrace(ctx, cmd, 100*time.Millisecond) }()
	finished := false
	defer func() {
		if !finished {
			cancel()
			<-done
		}
	}()
	waitForFile(t, ready)
	start := time.Now()
	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Errorf("command error = %v, want context.Canceled", err)
	}
	finished = true
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Errorf("stubborn process cleanup took %s, want SIGKILL escalation", elapsed)
	}
	before, err := os.Stat(output)
	if err != nil {
		t.Fatalf("stat descendant output: %v", err)
	}
	time.Sleep(50 * time.Millisecond)
	after, err := os.Stat(output)
	if err != nil || after.Size() != before.Size() {
		t.Errorf("descendant continued writing after return: before=%d after=%v err=%v", before.Size(), after, err)
	}
}

func TestRunnerProcessTreeHelper(t *testing.T) {
	mode := os.Getenv("PROCESS_TREE_HELPER_MODE")
	if mode == "" {
		return
	}
	signals := make(chan os.Signal, 1)
	signal.Notify(signals, syscall.SIGINT)
	defer signal.Stop(signals)
	ready := os.Getenv("PROCESS_TREE_READY")
	marker := os.Getenv("PROCESS_TREE_MARKER")
	switch mode {
	case "graceful-parent", "early-parent", "stubborn-parent":
		childMode := "graceful-child"
		if mode == "stubborn-parent" {
			childMode = "stubborn-child"
		}
		child := exec.Command(os.Args[0], "-test.run=^TestRunnerProcessTreeHelper$")
		child.Env = helperEnvironment(childMode)
		child.Stdout, child.Stderr = os.Stdout, os.Stderr
		if err := child.Start(); err != nil {
			t.Fatalf("start helper descendant: %v", err)
		}
		if mode == "early-parent" {
			waitForFile(t, ready)
			return
		}
		if mode == "stubborn-parent" {
			waitForFile(t, ready)
		}
		<-signals
		return
	case "graceful-child":
		if err := os.WriteFile(ready, []byte("ready"), 0600); err != nil {
			t.Fatalf("write descendant readiness: %v", err)
		}
		<-signals
		if err := os.WriteFile(marker, []byte("terminated"), 0600); err != nil {
			t.Fatalf("write descendant termination: %v", err)
		}
		return
	case "stubborn-child":
		signal.Ignore(syscall.SIGINT, syscall.SIGTERM)
		if err := os.WriteFile(ready, []byte("ready"), 0600); err != nil {
			t.Fatalf("write descendant readiness: %v", err)
		}
		output, err := os.OpenFile(os.Getenv("PROCESS_TREE_OUTPUT"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
		if err != nil {
			t.Fatalf("open descendant output: %v", err)
		}
		defer output.Close()
		for {
			if _, err := output.Write([]byte("still running\n")); err != nil {
				return
			}
			_, _ = os.Stdout.Write([]byte("tick\n"))
			time.Sleep(10 * time.Millisecond)
		}
	default:
		t.Fatalf("unknown process helper mode %q", mode)
	}
}

func testProcessTreeTerragrunt(t *testing.T, mode string) {
	t.Helper()
	bin := t.TempDir()
	writeTerragrunt(t, bin, "#!/bin/sh\nexec \"$PROCESS_TREE_HELPER\" -test.run='^TestRunnerProcessTreeHelper$'\n")
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	helper, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("PROCESS_TREE_HELPER", helper)
	t.Setenv("PROCESS_TREE_HELPER_MODE", mode)
}

func helperEnvironment(mode string) []string {
	environment := slices.DeleteFunc(os.Environ(), func(value string) bool {
		return strings.HasPrefix(value, "PROCESS_TREE_HELPER_MODE=")
	})
	return append(environment, "PROCESS_TREE_HELPER_MODE="+mode)
}
func waitForFile(t *testing.T, path string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(path); err == nil {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", path)
}

func writeTerragrunt(t *testing.T, bin, script string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(bin, "terragrunt"), []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
}
