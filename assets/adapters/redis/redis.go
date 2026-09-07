package harness

import (
	"bufio"
	"context"
	"fmt"
	"net"
	"strconv"
	"strings"
	"time"
)

// Redis speaks RESP directly over a socket, with no client library.
//
// A test suite needs Do, a few typed reads and a way to wait for a key — not a
// connection pool, pipelining or cluster support. Keeping it dependency-free
// means the suite still builds when the service's own Redis client is a
// version this module would otherwise have to agree with.
type Redis struct {
	Addr string
	DB   int

	conn net.Conn
	rw   *bufio.ReadWriter
}

// DialRedis connects and selects the database.
func DialRedis(addr string, db int, timeout time.Duration) (*Redis, error) {
	conn, err := net.DialTimeout("tcp", addr, timeout)
	if err != nil {
		return nil, fmt.Errorf("connecting to redis at %s: %w", addr, err)
	}
	r := &Redis{
		Addr: addr, DB: db, conn: conn,
		rw: bufio.NewReadWriter(bufio.NewReader(conn), bufio.NewWriter(conn)),
	}
	if db != 0 {
		if _, err := r.Do("SELECT", strconv.Itoa(db)); err != nil {
			conn.Close()
			return nil, err
		}
	}
	return r, nil
}

// Close releases the connection.
func (r *Redis) Close() error { return r.conn.Close() }

// Do sends one command and returns the decoded reply: nil, string, int64,
// or []any for arrays.
func (r *Redis) Do(args ...string) (any, error) {
	if err := r.write(args); err != nil {
		return nil, err
	}
	return r.readReply()
}

// Str runs a command expecting a bulk string; missing is ("", false, nil).
func (r *Redis) Str(args ...string) (string, bool, error) {
	reply, err := r.Do(args...)
	if err != nil {
		return "", false, err
	}
	if reply == nil {
		return "", false, nil
	}
	s, ok := reply.(string)
	if !ok {
		return "", false, fmt.Errorf("%s: expected a string reply, got %T", args[0], reply)
	}
	return s, true, nil
}

// Int runs a command expecting an integer reply.
func (r *Redis) Int(args ...string) (int64, error) {
	reply, err := r.Do(args...)
	if err != nil {
		return 0, err
	}
	switch v := reply.(type) {
	case int64:
		return v, nil
	case string:
		return strconv.ParseInt(v, 10, 64)
	default:
		return 0, fmt.Errorf("%s: expected an integer reply, got %T", args[0], reply)
	}
}

// List runs a command expecting an array of strings (LRANGE, SMEMBERS, KEYS).
func (r *Redis) List(args ...string) ([]string, error) {
	reply, err := r.Do(args...)
	if err != nil {
		return nil, err
	}
	items, ok := reply.([]any)
	if !ok {
		return nil, fmt.Errorf("%s: expected an array reply, got %T", args[0], reply)
	}
	out := make([]string, 0, len(items))
	for _, item := range items {
		out = append(out, fmt.Sprint(item))
	}
	return out, nil
}

// Get is the common case: read a key, reporting absence distinctly from "".
// The distinction matters — "absent" and "stored as empty" are different
// service behaviours and a scenario must be able to tell them apart.
func (r *Redis) Get(key string) (string, bool, error) { return r.Str("GET", key) }

// WaitForKey waits until a key exists and its value satisfies match. This is
// the Redis form of "wait on what the service did, never on a clock".
func (r *Redis) WaitForKey(ctx context.Context, key string, timeout time.Duration, match func(string) bool) (string, error) {
	var found string
	err := Until(ctx, timeout, fmt.Sprintf("redis key %q to hold the expected value", key), func(context.Context) (bool, error) {
		value, ok, err := r.Get(key)
		if err != nil || !ok {
			return false, err
		}
		found = value
		return match == nil || match(value), nil
	})
	return found, err
}

// FlushDB empties the selected database, for a clean fixture per run.
func (r *Redis) FlushDB() error {
	_, err := r.Do("FLUSHDB")
	return err
}

// Ready is the readiness probe to hand to Service.Ready.
func (r *Redis) Ready() func(context.Context) error {
	return func(context.Context) error {
		reply, err := r.Do("PING")
		if err != nil {
			return err
		}
		if reply != "PONG" {
			return fmt.Errorf("PING answered %v", reply)
		}
		return nil
	}
}

// BreakWrites makes the server reject writes, so the scenario where the store
// refuses a write can be provoked. The returned func restores it; always defer
// it, so the restore happens even when the scenario aborts.
func (r *Redis) BreakWrites() (func(), error) {
	if _, err := r.Do("CONFIG", "SET", "min-replicas-to-write", "1"); err != nil {
		return nil, fmt.Errorf("cannot make redis reject writes (CONFIG SET may be disabled): %w", err)
	}
	return func() { _, _ = r.Do("CONFIG", "SET", "min-replicas-to-write", "0") }, nil
}

func (r *Redis) write(args []string) error {
	var b strings.Builder
	fmt.Fprintf(&b, "*%d\r\n", len(args))
	for _, a := range args {
		fmt.Fprintf(&b, "$%d\r\n%s\r\n", len(a), a)
	}
	if _, err := r.rw.WriteString(b.String()); err != nil {
		return fmt.Errorf("writing redis command %v: %w", args, err)
	}
	return r.rw.Flush()
}

func (r *Redis) readReply() (any, error) {
	line, err := r.rw.ReadString('\n')
	if err != nil {
		return nil, fmt.Errorf("reading redis reply: %w", err)
	}
	line = strings.TrimRight(line, "\r\n")
	if line == "" {
		return nil, fmt.Errorf("empty redis reply")
	}
	body := line[1:]
	switch line[0] {
	case '+':
		return body, nil
	case '-':
		return nil, fmt.Errorf("redis error: %s", body)
	case ':':
		return strconv.ParseInt(body, 10, 64)
	case '$':
		n, err := strconv.Atoi(body)
		if err != nil {
			return nil, fmt.Errorf("bad bulk length %q: %w", body, err)
		}
		if n < 0 {
			return nil, nil // a nil bulk string: the key is absent
		}
		buf := make([]byte, n+2) // payload plus CRLF
		if _, err := readFull(r.rw, buf); err != nil {
			return nil, err
		}
		return string(buf[:n]), nil
	case '*':
		n, err := strconv.Atoi(body)
		if err != nil {
			return nil, fmt.Errorf("bad array length %q: %w", body, err)
		}
		if n < 0 {
			return nil, nil
		}
		items := make([]any, 0, n)
		for i := 0; i < n; i++ {
			item, err := r.readReply()
			if err != nil {
				return nil, err
			}
			items = append(items, item)
		}
		return items, nil
	default:
		return nil, fmt.Errorf("unrecognised redis reply type %q", line[0])
	}
}

func readFull(rw *bufio.ReadWriter, buf []byte) (int, error) {
	read := 0
	for read < len(buf) {
		n, err := rw.Read(buf[read:])
		if err != nil {
			return read, fmt.Errorf("reading redis payload: %w", err)
		}
		read += n
	}
	return read, nil
}
