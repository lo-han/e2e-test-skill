package harness

import (
	"context"
	"fmt"
	"net"
	"time"
)

// Until polls check until it returns true, and fails with what was being
// waited for rather than a bare timeout.
//
// This is the only correct shape of wait in the suite: every wait is on
// something the service actually did — a row appearing, a log line landing —
// never on a clock. A sleep is too short on a loaded machine, wasted time
// everywhere else, and it never says what it was waiting for.
func Until(ctx context.Context, timeout time.Duration, what string, check func(context.Context) (bool, error)) error {
	deadline := time.Now().Add(timeout)
	var last error
	for {
		ok, err := check(ctx)
		if err != nil {
			last = err
		}
		if ok {
			return nil
		}
		if time.Now().After(deadline) {
			if last != nil {
				return fmt.Errorf("timed out after %s waiting for %s: %w", timeout, what, last)
			}
			return fmt.Errorf("timed out after %s waiting for %s", timeout, what)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(20 * time.Millisecond):
		}
	}
}

// Stays asserts a condition holds for the whole window — the shape behind
// "nothing was stored". Only meaningful after a marker proving the service
// already processed the input.
func Stays(ctx context.Context, window time.Duration, what string, check func(context.Context) (bool, error)) error {
	end := time.Now().Add(window)
	for time.Now().Before(end) {
		ok, err := check(ctx)
		if err != nil {
			return err
		}
		if !ok {
			return fmt.Errorf("%s stopped holding before %s elapsed", what, window)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(20 * time.Millisecond):
		}
	}
	return nil
}

// PortFree reports whether a TCP port can be bound. Preflight uses it: a
// leftover process will answer for the service and every log assertion will
// then fail bizarrely.
func PortFree(hostPort string) bool {
	ln, err := net.Listen("tcp", hostPort)
	if err != nil {
		return false
	}
	ln.Close()
	return true
}

// HoldPort binds a port and returns a release func, so "it must refuse to
// start when its port is taken" can be provoked from the suite.
func HoldPort(hostPort string) (func(), error) {
	ln, err := net.Listen("tcp", hostPort)
	if err != nil {
		return nil, fmt.Errorf("cannot hold %s, something is already listening: %w", hostPort, err)
	}
	return func() { ln.Close() }, nil
}

// WaitPort waits until something accepts connections on the address.
func WaitPort(ctx context.Context, hostPort string, timeout time.Duration) error {
	return Until(ctx, timeout, "a listener on "+hostPort, func(context.Context) (bool, error) {
		conn, err := net.DialTimeout("tcp", hostPort, 500*time.Millisecond)
		if err != nil {
			return false, err
		}
		conn.Close()
		return true, nil
	})
}
