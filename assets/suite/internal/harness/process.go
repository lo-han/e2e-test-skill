package harness

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"time"
)

// Service owns the process under test: build it, start it, know when it is
// ready, restart it, signal it, and know how it exited.
//
// Launch and WaitExit are deliberately separate from Start. That separation is
// what makes "it must refuse to start when its port is taken" testable at all.
type Service struct {
	// Repo is the checkout to build. The suite lives outside it.
	Repo string
	// Package is the main package within the repo ("." or "./cmd/server").
	Package string
	// BinPath is where the built binary goes.
	BinPath string
	// Args are passed to the service on every start.
	Args []string
	// Env is added to the service's environment as KEY=VALUE.
	Env []string
	// StdioPath collects the process's own stdout/stderr. A panic goes here,
	// not to the service's log, and it is the first thing you will want when
	// a start fails.
	StdioPath string
	// Ready reports whether the service is actually serving. Return nil only
	// when a real call succeeded — never after a bare connection or a sleep.
	Ready func(ctx context.Context) error

	cmd    *exec.Cmd
	stdio  *os.File
	exited chan struct{}
	status int
}

// Build compiles the checkout. Returns a build error verbatim: a suite that
// cannot build the service must say why in the first line of its output.
func (s *Service) Build(ctx context.Context) error {
	if s.Package == "" {
		s.Package = "."
	}
	if s.BinPath == "" {
		s.BinPath = filepath.Join(os.TempDir(), "service-under-test")
	}
	cmd := exec.CommandContext(ctx, "go", "build", "-o", s.BinPath, s.Package)
	cmd.Dir = s.Repo
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("building %s in %s: %w\n%s", s.Package, s.Repo, err, out)
	}
	return nil
}

// Launch starts the process without waiting for readiness.
func (s *Service) Launch() error {
	if s.cmd != nil && s.exited != nil {
		select {
		case <-s.exited:
		default:
			return errors.New("service is already running")
		}
	}
	stdio, err := os.OpenFile(s.StdioPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return fmt.Errorf("opening stdio file %s: %w", s.StdioPath, err)
	}

	cmd := exec.Command(s.BinPath, s.Args...)
	cmd.Env = append(os.Environ(), s.Env...)
	cmd.Stdout, cmd.Stderr = stdio, stdio
	// Its own process group, so a signal aimed at the service is not also
	// delivered to the suite.
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}

	if err := cmd.Start(); err != nil {
		stdio.Close()
		return fmt.Errorf("starting %s: %w", s.BinPath, err)
	}
	s.cmd, s.stdio = cmd, stdio
	s.exited = make(chan struct{})
	go func(done chan struct{}) {
		_ = cmd.Wait()
		s.status = cmd.ProcessState.ExitCode()
		close(done)
	}(s.exited)
	return nil
}

// Start launches and waits until the service is ready, or the process dies.
func (s *Service) Start(ctx context.Context, timeout time.Duration) error {
	if err := s.Launch(); err != nil {
		return err
	}
	return s.WaitReady(ctx, timeout)
}

// WaitReady polls Ready until it succeeds, the process exits, or time runs out.
// A process that died is reported as such rather than as a readiness timeout —
// the two have entirely different causes.
func (s *Service) WaitReady(ctx context.Context, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	var last error
	for {
		select {
		case <-s.exited:
			return fmt.Errorf("service exited with status %d before becoming ready (see %s)", s.status, s.StdioPath)
		default:
		}
		if s.Ready == nil {
			return nil
		}
		if last = s.Ready(ctx); last == nil {
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("service not ready within %s: %w (see %s)", timeout, last, s.StdioPath)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(50 * time.Millisecond):
		}
	}
}

// WaitExit waits for the process to end on its own and returns its exit code.
// This is the call behind "it must refuse to start when its port is taken".
func (s *Service) WaitExit(timeout time.Duration) (int, error) {
	if s.exited == nil {
		return 0, errors.New("service was never launched")
	}
	select {
	case <-s.exited:
		return s.status, nil
	case <-time.After(timeout):
		return 0, fmt.Errorf("service was still running after %s; it was expected to exit", timeout)
	}
}

// Stop sends a signal, waits, and returns the exit code. Use the signal the
// service documents for graceful shutdown.
func (s *Service) Stop(sig os.Signal, timeout time.Duration) (int, error) {
	if s.cmd == nil || s.cmd.Process == nil {
		return 0, nil
	}
	select {
	case <-s.exited:
		return s.status, nil
	default:
	}
	if err := s.cmd.Process.Signal(sig); err != nil {
		return 0, fmt.Errorf("signalling service: %w", err)
	}
	select {
	case <-s.exited:
		s.closeStdio()
		return s.status, nil
	case <-time.After(timeout):
		s.Kill()
		return 0, fmt.Errorf("service did not exit within %s of %v", timeout, sig)
	}
}

// Restart stops the service and starts it again, waiting for readiness.
func (s *Service) Restart(ctx context.Context, timeout time.Duration) error {
	if _, err := s.Stop(syscall.SIGTERM, timeout); err != nil {
		return err
	}
	return s.Start(ctx, timeout)
}

// Kill terminates the whole process group, for teardown and for the paths
// where a graceful stop is not what is being tested.
func (s *Service) Kill() {
	if s.cmd == nil || s.cmd.Process == nil {
		return
	}
	_ = syscall.Kill(-s.cmd.Process.Pid, syscall.SIGKILL)
	if s.exited != nil {
		select {
		case <-s.exited:
		case <-time.After(2 * time.Second):
		}
	}
	s.closeStdio()
}

// Running reports whether the process is still alive.
func (s *Service) Running() bool {
	if s.exited == nil {
		return false
	}
	select {
	case <-s.exited:
		return false
	default:
		return true
	}
}

func (s *Service) closeStdio() {
	if s.stdio != nil {
		s.stdio.Close()
		s.stdio = nil
	}
}
