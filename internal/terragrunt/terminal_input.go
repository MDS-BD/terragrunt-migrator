//go:build darwin || linux

package terragrunt

import (
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"sync"
	"syscall"

	"github.com/creack/pty"
	"golang.org/x/sys/unix"
	"golang.org/x/term"
)

// prepareTerminalInput gives a terminal-backed command a controlling pseudo-
// terminal. The real terminal stays in the parent's foreground process group;
// input is relayed through a PTY so child programs retain terminal detection
// and password prompts can disable echo on the PTY slave.
//
// start must be called only after cmd.Start succeeds. stop is safe to call
// whether or not start was called, and stops and joins all relay goroutines,
// closes PTY resources, and restores the real terminal's original settings.
// The interrupted channel is closed when Ctrl+C is read from the real
// terminal; the caller is responsible for interrupting the child process group.
func prepareTerminalInput(cmd *exec.Cmd) (start func(), stop func(), interrupted <-chan struct{}, err error) {
	terminalIn, ok := cmd.Stdin.(*os.File)
	if !ok {
		return noopTerminalInput()
	}
	terminalInFD := int(terminalIn.Fd())
	if !term.IsTerminal(terminalInFD) {
		return noopTerminalInput()
	}

	stdoutOnTerminal := isSameTerminal(cmd.Stdout, terminalIn)
	stderrOnTerminal := isSameTerminal(cmd.Stderr, terminalIn)
	// Use a fresh file description so O_NONBLOCK applies only to the relay,
	// not the shell's stdin/stdout descriptors. Poll on Darwin treats /dev/tty
	// as invalid, so tty readiness uses Select below instead.
	ttyFD, err := unix.Open("/dev/tty", unix.O_RDWR|unix.O_CLOEXEC|unix.O_NONBLOCK, 0)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("open controlling terminal for relay: %w", err)
	}
	tty := os.NewFile(uintptr(ttyFD), "/dev/tty")
	if tty == nil {
		_ = unix.Close(ttyFD)
		return nil, nil, nil, fmt.Errorf("open controlling terminal for relay: invalid descriptor")
	}
	if !term.IsTerminal(ttyFD) {
		_ = tty.Close()
		return noopTerminalInput()
	}
	inputForeground, err := unix.IoctlGetInt(terminalInFD, unix.TIOCGPGRP)
	if err != nil {
		_ = tty.Close()
		return nil, nil, nil, fmt.Errorf("check terminal input foreground group: %w", err)
	}
	controlForeground, err := unix.IoctlGetInt(ttyFD, unix.TIOCGPGRP)
	if err != nil {
		_ = tty.Close()
		return nil, nil, nil, fmt.Errorf("check controlling terminal foreground group: %w", err)
	}
	if inputForeground != controlForeground || inputForeground != unix.Getpgrp() {
		_ = tty.Close()
		return noopTerminalInput()
	}

	master, slave, err := pty.Open()
	if err != nil {
		_ = tty.Close()
		return nil, nil, nil, fmt.Errorf("open interactive PTY: %w", err)
	}
	closePTY := func() {
		_ = master.Close()
		_ = slave.Close()
	}
	masterFD := int(master.Fd())
	if err := copyTerminalSize(ttyFD, masterFD); err != nil {
		closePTY()
		_ = tty.Close()
		return nil, nil, nil, fmt.Errorf("copy terminal size to PTY: %w", err)
	}
	if err := unix.SetNonblock(masterFD, true); err != nil {
		closePTY()
		_ = tty.Close()
		return nil, nil, nil, fmt.Errorf("configure interactive PTY: %w", err)
	}

	terminalState, err := term.MakeRaw(ttyFD)
	if err != nil {
		closePTY()
		_ = tty.Close()
		return nil, nil, nil, fmt.Errorf("prepare terminal input relay: %w", err)
	}

	inputWakeRead, inputWakeWrite, inputWakeReadFD, inputWakeWriteFD, err := newWakePipe()
	if err != nil {
		_ = term.Restore(ttyFD, terminalState)
		closePTY()
		_ = tty.Close()
		return nil, nil, nil, err
	}
	outputWakeRead, outputWakeWrite, outputWakeReadFD, outputWakeWriteFD, err := newWakePipe()
	if err != nil {
		closeWakePipe(inputWakeRead, inputWakeWrite)
		_ = term.Restore(ttyFD, terminalState)
		closePTY()
		_ = tty.Close()
		return nil, nil, nil, err
	}

	originalStdin, originalStdout, originalStderr := cmd.Stdin, cmd.Stdout, cmd.Stderr
	originalSysProcAttr := cmd.SysProcAttr
	cmd.Stdin = slave
	if stdoutOnTerminal {
		cmd.Stdout = slave
	}
	if stderrOnTerminal {
		cmd.Stderr = slave
	}
	attrs := new(syscall.SysProcAttr)
	if originalSysProcAttr != nil {
		*attrs = *originalSysProcAttr
	}
	// The command gets its own session and controlling PTY; the real terminal
	// remains foregrounded with the parent, which receives Ctrl+C from relay.
	attrs.Setpgid = false
	attrs.Setsid = true
	attrs.Setctty = true
	attrs.Ctty = 0
	cmd.SysProcAttr = attrs

	interruptedChan := make(chan struct{})
	inputDone, outputDone, resizeDone := make(chan struct{}), make(chan struct{}), make(chan struct{})
	stopResize := make(chan struct{})
	resizeSignals := make(chan os.Signal, 1)
	var mu sync.Mutex
	started := false
	stopped := false
	var stopOnce sync.Once

	start = func() {
		mu.Lock()
		defer mu.Unlock()
		if started || stopped {
			return
		}
		started = true
		// The child inherited its own slave descriptor in Start.
		_ = slave.Close()
		go func() {
			defer close(inputDone)
			relayTerminalInput(ttyFD, masterFD, inputWakeReadFD, interruptedChan, &mu, &stopped)
		}()
		go func() {
			defer close(outputDone)
			relayTerminalOutput(masterFD, outputWakeReadFD, ttyFD)
		}()
		signal.Notify(resizeSignals, syscall.SIGWINCH)
		go func() {
			defer close(resizeDone)
			for {
				select {
				case <-stopResize:
					return
				case <-resizeSignals:
					_ = copyTerminalSize(ttyFD, masterFD)
				}
			}
		}()
	}

	stop = func() {
		stopOnce.Do(func() {
			mu.Lock()
			stopped = true
			wasStarted := started
			if wasStarted {
				wakeRelay(inputWakeWriteFD)
				wakeRelay(outputWakeWriteFD)
				close(stopResize)
			}
			mu.Unlock()

			if wasStarted {
				<-inputDone
				signal.Stop(resizeSignals)
				<-resizeDone
				<-outputDone
			}
			closeWakePipe(inputWakeRead, inputWakeWrite)
			closeWakePipe(outputWakeRead, outputWakeWrite)
			closePTY()
			cmd.Stdin, cmd.Stdout, cmd.Stderr = originalStdin, originalStdout, originalStderr
			cmd.SysProcAttr = originalSysProcAttr
			_ = term.Restore(ttyFD, terminalState)
			_ = tty.Close()
		})
	}

	return start, stop, interruptedChan, nil
}

func noopTerminalInput() (func(), func(), <-chan struct{}, error) {
	return func() {}, func() {}, nil, nil
}

func isSameTerminal(candidate any, terminal *os.File) bool {
	file, ok := candidate.(*os.File)
	return ok && term.IsTerminal(int(file.Fd())) && sameTerminal(file, terminal)
}

func sameTerminal(first, second *os.File) bool {
	firstInfo, firstErr := first.Stat()
	secondInfo, secondErr := second.Stat()
	return firstErr == nil && secondErr == nil && os.SameFile(firstInfo, secondInfo)
}

func copyTerminalSize(terminalFD, masterFD int) error {
	winsize, err := unix.IoctlGetWinsize(terminalFD, unix.TIOCGWINSZ)
	if err != nil {
		return err
	}
	return unix.IoctlSetWinsize(masterFD, unix.TIOCSWINSZ, winsize)
}

func newWakePipe() (*os.File, *os.File, int, int, error) {
	readEnd, writeEnd, err := os.Pipe()
	if err != nil {
		return nil, nil, -1, -1, fmt.Errorf("create terminal relay wake pipe: %w", err)
	}
	readFD := int(readEnd.Fd())
	writeFD := int(writeEnd.Fd())
	if err := unix.SetNonblock(writeFD, true); err != nil {
		_ = readEnd.Close()
		_ = writeEnd.Close()
		return nil, nil, -1, -1, fmt.Errorf("configure terminal relay wake pipe: %w", err)
	}
	return readEnd, writeEnd, readFD, writeFD, nil
}

func closeWakePipe(readEnd, writeEnd *os.File) {
	_ = readEnd.Close()
	_ = writeEnd.Close()
}

func wakeRelay(fd int) {
	_, _ = unix.Write(fd, []byte{1})
}

func relayTerminalInput(terminalFD, masterFD, wakeFD int, interrupted chan struct{}, mu *sync.Mutex, stopped *bool) {
	var one [1]byte
	for {
		ready, err := selectTerminal(terminalFD, wakeFD, false)
		if err == unix.EINTR {
			continue
		}
		if err != nil || ready == selectWake {
			return
		}
		mu.Lock()
		if *stopped {
			mu.Unlock()
			return
		}
		count, err := unix.Read(terminalFD, one[:])
		mu.Unlock()
		if err == unix.EINTR || err == unix.EAGAIN || err == unix.EWOULDBLOCK {
			continue
		}
		if err != nil || count == 0 {
			return
		}
		if one[0] == 3 {
			close(interrupted)
			return
		}
		if !writePTYInput(masterFD, wakeFD, one[:count]) {
			return
		}
	}
}

const (
	selectWake = iota
	selectTTY
)

func selectTerminal(terminalFD, wakeFD int, writable bool) (int, error) {
	var readSet, writeSet unix.FdSet
	readSet.Zero()
	writeSet.Zero()
	readSet.Set(wakeFD)
	if writable {
		writeSet.Set(terminalFD)
	} else {
		readSet.Set(terminalFD)
	}
	nfds := max(terminalFD, wakeFD) + 1
	for {
		_, err := unix.Select(nfds, &readSet, func() *unix.FdSet {
			if writable {
				return &writeSet
			}
			return nil
		}(), nil, nil)
		if err == unix.EINTR {
			readSet.Zero()
			writeSet.Zero()
			readSet.Set(wakeFD)
			if writable {
				writeSet.Set(terminalFD)
			} else {
				readSet.Set(terminalFD)
			}
			continue
		}
		if err != nil {
			return 0, err
		}
		if readSet.IsSet(wakeFD) {
			return selectWake, nil
		}
		if (writable && writeSet.IsSet(terminalFD)) || (!writable && readSet.IsSet(terminalFD)) {
			return selectTTY, nil
		}
	}
}

func writeTerminal(terminalFD, wakeFD int, data []byte) bool {
	for len(data) > 0 {
		ready, err := selectTerminal(terminalFD, wakeFD, true)
		if err == unix.EINTR {
			continue
		}
		if err != nil || ready == selectWake {
			return false
		}
		count, err := unix.Write(terminalFD, data)
		if err == unix.EINTR || err == unix.EAGAIN || err == unix.EWOULDBLOCK {
			continue
		}
		if err != nil || count == 0 {
			return false
		}
		data = data[count:]
	}
	return true
}

func writePTYInput(masterFD, wakeFD int, input []byte) bool {
	for len(input) > 0 {
		fds := []unix.PollFd{
			{Fd: int32(wakeFD), Events: unix.POLLIN},
			{Fd: int32(masterFD), Events: unix.POLLOUT},
		}
		if _, err := unix.Poll(fds, -1); err != nil {
			if err == unix.EINTR {
				continue
			}
			return false
		}
		if fds[0].Revents != 0 || fds[1].Revents&(unix.POLLHUP|unix.POLLERR|unix.POLLNVAL) != 0 {
			return false
		}
		if fds[1].Revents&unix.POLLOUT == 0 {
			continue
		}
		count, err := unix.Write(masterFD, input)
		if err == unix.EINTR || err == unix.EAGAIN || err == unix.EWOULDBLOCK {
			continue
		}
		if err != nil || count == 0 {
			return false
		}
		input = input[count:]
	}
	return true
}

func relayTerminalOutput(masterFD, wakeFD, terminalFD int) {
	var buffer [4096]byte
	for {
		fds := []unix.PollFd{
			{Fd: int32(wakeFD), Events: unix.POLLIN},
			{Fd: int32(masterFD), Events: unix.POLLIN},
		}
		if _, err := unix.Poll(fds, -1); err != nil {
			if err == unix.EINTR {
				continue
			}
			return
		}
		if fds[0].Revents != 0 {
			drainPTYOutput(masterFD, terminalFD, buffer[:])
			return
		}
		if fds[1].Revents&(unix.POLLIN|unix.POLLHUP|unix.POLLERR|unix.POLLNVAL) == 0 {
			continue
		}
		count, err := unix.Read(masterFD, buffer[:])
		if err == unix.EINTR || err == unix.EAGAIN || err == unix.EWOULDBLOCK {
			continue
		}
		if err != nil || count == 0 {
			return
		}
		if !writeTerminal(terminalFD, wakeFD, buffer[:count]) {
			return
		}
	}
}

func drainPTYOutput(masterFD, terminalFD int, buffer []byte) {
	for {
		count, err := unix.Read(masterFD, buffer)
		if err == unix.EINTR {
			continue
		}
		if err == unix.EAGAIN || err == unix.EWOULDBLOCK || err != nil || count == 0 {
			return
		}
		data := buffer[:count]
		for len(data) > 0 {
			written, err := unix.Write(terminalFD, data)
			if err == unix.EINTR {
				continue
			}
			if err == unix.EAGAIN || err == unix.EWOULDBLOCK || err != nil || written == 0 {
				return
			}
			data = data[written:]
		}
	}
}
