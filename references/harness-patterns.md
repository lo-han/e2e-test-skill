# Harness patterns

The mechanics that make a black-box suite deterministic. Examples are Go, but
each pattern is about the shape of the problem, not the language.

## 1. Wait on what the service did, never on a clock

`time.Sleep(200ms)` is the seed of every flaky suite: too short on a loaded
machine, wasted time everywhere else, and it never says what it was waiting for.

Two honest waits cover almost everything:

**Poll the state** for anything the service was supposed to write.

```go
func (db *DB) WaitForRow(ctx context.Context, id string, match func(Row) bool, timeout time.Duration) (*Row, error) {
    deadline := time.Now().Add(timeout)
    for {
        row, err := db.Row(ctx, id)
        if err != nil { return nil, err }
        if row != nil && match(*row) { return row, nil }
        if time.Now().After(deadline) {
            return row, fmt.Errorf("row %q never reached the expected state within %s", id, timeout)
        }
        time.Sleep(20 * time.Millisecond)
    }
}
```

**Wait for the log line** for anything the service was supposed *not* to write.
"Nothing was stored" is only a fact once you know the message was actually
processed; before that it is a race you will win most of the time and lose in
CI. Most services log a message on receipt, which is exactly the marker needed.

## 2. The cursor-based log reader

Treat the log as a stream with a read position. A wait scans forward from the
cursor and, on a match, moves the cursor past the matched line.

```go
type LogFile struct { path string; cursor int64 }

func (l *LogFile) WaitFor(ctx context.Context, fragment string, timeout time.Duration, more ...string) (LogLine, error)
func (l *LogFile) Since() ([]LogLine, error)   // new lines, cursor unmoved
func (l *LogFile) Drain() error                // jump to end
func (l *LogFile) Cursor() int64               // save a position
func (l *LogFile) Rewind(offset int64)         // and come back to it
```

This is what stops one scenario matching an earlier scenario's line — with a
cursor, a scenario can only ever see what happened after it started. It also
means scenarios must run sequentially, which they should anyway.

Two habits that avoid self-inflicted failures: when a scenario needs to assert
on the very line a helper already consumed, have the helper hand the line back
rather than searching for it again; and when several lines may arrive in any
order, save the cursor, wait for one, then rewind and scan the whole window
instead of chaining waits in a guessed order.

## 3. The receipt marker

Give every message the suite publishes an extra field naming the scenario that
sent it:

```json
{"event":"level","device":"tank-01","valid":true,"distance_cm":62.5,"e2e_case":"unreadable_sensor_stores_nothing"}
```

Most contracts say unknown fields are ignored, so it changes nothing the service
does — check that the contract does say so. Because services typically log the
raw payload, it gives each scenario one unambiguous line to wait for even when
two scenarios publish otherwise identical messages. Match it in whatever form
the log renders it (a `%q`-quoted payload has its inner quotes escaped).

If the contract forbids unknown fields, vary a harmless field per scenario
instead — a timestamp, an id — and match on that.

## 4. Process lifecycle

The suite owns the service process: build it, start it, know when it is ready,
restart it, signal it, and know how it exited.

```go
func BuildServer(ctx context.Context, cfg Config) (*Server, error)  // compile the checkout
func (s *Server) Launch() error                                     // start, no waiting
func (s *Server) Start(ctx, log *LogFile) error                     // launch + wait until ready
func (s *Server) WaitExit(timeout) (int, error)                     // for "it should refuse to start"
func (s *Server) Restart(ctx, log *LogFile) error
func (s *Server) Stop(timeout) (int, error)                         // signal, wait, exit code
func (s *Server) Kill()
```

Readiness is "the service says it is listening *and* answers", not a sleep: wait
for its startup log lines and then poll its health endpoint. Give the process
its own process group so a signal aimed at it is not also delivered to the
suite, and send its stdout/stderr to a file — a panic goes there, not to the
service's own log, and it is the first thing you will want when a start fails.

Keeping `Launch` and `WaitExit` separate from `Start` is what makes "it must
refuse to start when its port is taken" testable at all.

## 5. Oracles, checked against the code's own tests

When the suite must know an expected value that the service computes, write that
calculation from the specification rather than copying the implementation — a
transcribed copy agrees with the code even when both are wrong.

Then pin the oracle: take input/output pairs from the service's own unit tests
and assert your implementation reproduces them, as the first scenario in the
first suite. That catches the case where you misread the spec, and it is why the
oracle's later disagreements can be trusted.

## 6. Preflight, so failures name their cause

Before starting anything, check the things whose absence produces confusing
failures: is the port free (a leftover process will answer for the service and
every log assertion will fail bizarrely), is the store reachable, does the
checkout look right. Fail with a sentence that names the fix.

## 7. Forcing the failure paths

Error handling is where services hide their worst bugs, and it needs provoking
from outside:

- **Constraint violations** — reference an entity that does not exist, where the
  schema has a foreign key.
- **A store that rejects writes** — install a trigger that raises on `UPDATE` of
  one table, or revoke a permission, then remove it afterwards.
- **A query that fails** — rename a table out of the way inside a helper that
  restores it with a deferred call, so the restore happens even when the
  scenario aborts. Renaming keeps the table's identity, so pooled connections
  and cached statements survive it.
- **A port already taken** — bind it from the suite before starting the service.
- **Malformed input** — bytes the service's own decoder cannot parse.
- **Replay** — publish a retained/queued message, then restart the service so
  the broker replays it into a fresh subscription.

Always undo these with a deferred restore, and afterwards assert the service
still works: recovery from a transient failure is itself a promise worth
testing.

## 8. Fixtures that include the broken ones

Seed entities that are *meant* to fail: one whose type has no registered
handler, one whose stored configuration fails validation, one referenced by
events but never registered. Without them the error paths are unreachable from
outside, and those paths are where the findings are.
