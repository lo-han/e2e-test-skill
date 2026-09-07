# The harness API

Everything below is already written, in `assets/`, and lands in the suite when
`scripts/scaffold.py` runs. **Read this file instead of the code.** Do not open
the adapters to find out what they do, and do not re-implement any of it: a
re-typed harness is a harness with fresh bugs in it, and it costs a few
thousand tokens to produce something that already exists.

Scenarios are the only code to write. Everything here is what they are handed.

## Every scenario

```go
runner.Scenario{
    Name: "state_off_without_an_open_run_records_nothing",
    Doc:  "contract §4: a stop for a device with nothing running is ignored",
    Run: func(t *runner.T) {
        // ...
    },
}
```

`*runner.T` collects failures rather than aborting on the first, so one
scenario reports every way it disagreed with the service.

| Call | |
| --- | --- |
| `t.Errorf(format, ...)` | record a failure, keep going |
| `t.Fatalf(format, ...)` | record a failure and stop this scenario |
| `t.Check(err, "while doing X")` | stop with a named cause when err != nil |
| `t.Logf(format, ...)` | a note, shown with `-v`, kept in the JSON |
| `t.Skipf(format, ...)` | not applicable, with the reason |

`T` satisfies testify's `TestingT`, so `assert.Equal(t, want, got)` works
unchanged after `go get github.com/stretchr/testify`.

## Waiting — never sleep

```go
harness.Until(ctx, timeout, "what is awaited", func(ctx) (bool, error))
harness.Stays(ctx, window, "what must hold", func(ctx) (bool, error))
```

`Until` polls until true and fails naming what it waited for. `Stays` asserts
something holds for a whole window — the shape behind "nothing was stored",
and only meaningful *after* a marker proving the service already processed the
input.

## The service process — `e.Service`

```go
Build(ctx) error                       // compile the checkout
Launch() error                         // start, no waiting
Start(ctx, timeout) error              // launch + wait until ready
WaitReady(ctx, timeout) error
WaitExit(timeout) (int, error)         // for "it must refuse to start"
Stop(sig, timeout) (int, error)        // signal, wait, exit code
Restart(ctx, timeout) error
Kill()
Running() bool
```

A process that died during startup is reported as such, not as a readiness
timeout. Its stdout/stderr go to `stdio.log`, where a panic lands.

## The log — `e.Log`

```go
WaitFor(ctx, timeout, fragments...) (Line, error)  // scan forward, move cursor past the match
Absent(ctx, window, fragments...) error            // assert no such line; cursor unmoved
Since() ([]string, error)                          // new lines, cursor unmoved
Drain() error                                      // jump to end
Cursor() int64                                     // save a position
Rewind(offset int64)                               // and come back to it
```

The cursor is what stops one scenario matching an earlier scenario's line. When
several lines may arrive in any order: save the cursor, wait for one, rewind,
then scan the window — do not chain waits in a guessed order.

## Ports

```go
harness.PortFree(hostPort) bool
harness.HoldPort(hostPort) (release func(), err error)   // provoke "port already taken"
harness.WaitPort(ctx, hostPort, timeout) error
```

## Adapters

Only the ones named at scaffold time are present. Each exposes `Ready()` for
`Service.Ready`, so readiness is always a real call.

### HTTP — `e.HTTP`

```go
Get/Post/Put/Patch/Delete(ctx, path[, body]) (Response, error)
Do(ctx, method, path, body) (Response, error)   // body: nil, []byte, string, or any JSON value
Ready(path) func(ctx) error
```

`Response{Status, Header, Body, Elapsed}` with `.Text()`, `.JSON(&v)`,
`.Map()`, `.List()`. Use `.Map()` for shape assertions: checking a field is
*absent* is a map lookup and impossible on a struct. Pass `[]byte` to send
bytes the decoder cannot parse.

### gRPC — `e.GRPC`

```go
DialGRPC(ctx, target, timeout)          // blocks: a lazy dial proves nothing
g.Conn                                   // hand to your generated stub: pb.NewFooClient(e.GRPC.Conn)
g.Ready(service) func(ctx) error         // standard health service
g.WithMarker(ctx, scenario) context.Context
g.WithMetadata(ctx, k, v, ...) context.Context
g.CallUnknownMethod(ctx, "/pkg.Svc/Nope") error   // must answer Unimplemented
harness.Deadline(ctx, d)                 // provoke a deadline shorter than the work
harness.Code(err) codes.Code             // assert the code the spec names
harness.StatusOf(err) *status.Status
harness.Details(err) []any               // typed error details
```

Generate stubs into the suite's own module from the service's `.proto`. Never
import the service's generated package — that makes the suite agree with the
server by construction, which is the agreement under test.

### PostgreSQL — `e.DB` / MySQL — `e.DB`

Same surface for both; pick one at scaffold time.

```go
ApplySchemaFile(ctx, path) error
Exec(ctx, query, args...) error
Rows(ctx, query, args...) ([]map[string]any, error)
Row(ctx, query, args...) (map[string]any, error)
Count(ctx, query, args...) (int, error)
WaitForRow(ctx, timeout, match func(row) bool, query, args...) (row, error)
WaitForCount(ctx, timeout, want int, query, args...) error
TruncateAll(ctx, tables...) error
Ready() func(ctx) error
```

Failure injection — always `defer` the returned restore:

```go
restore, err := e.DB.RejectWrites(ctx, "runs")   // trigger that raises on write
restore, err := e.DB.HideTable(ctx, "runs")      // rename away; identity preserved
e.DB.KillConnections(ctx, appNameOrUser)         // drop connections mid-flight
```

MySQL only, and worth running in the self-check suite, because each makes a
scenario prove nothing when it is false:

```go
e.DB.AssertStrictMode(ctx)              // else bad input is repaired, not rejected
e.DB.AssertInnoDB(ctx, "runs", "devices") // else FOREIGN KEY is parsed and ignored
```

### Redis — `e.Redis`

```go
Do(args...) (any, error)
Get(key) (string, bool, error)          // absent and "" are distinguishable
Str/Int/List(args...)
WaitForKey(ctx, key, timeout, match func(string) bool) (string, error)
FlushDB() error
BreakWrites() (restore func(), err error)
Ready() func(ctx) error
```

### MQTT — `e.MQTT`

```go
Publish(topic, qos, retained, body) error          // body: []byte, string, or any JSON value
PublishMarked(topic, qos, retained, event, scenarioName) error
ClearRetained(topic) error
Watch(topic, qos) error                            // then WaitForMessage
WaitForMessage(ctx, topic, timeout, match func(Message) bool) (Message, error)
Drain()
```

Wait for the service's subscription log line before the first publish: a
message published before it subscribes is lost silently.

### Kafka — `e.Kafka`

```go
CreateTopic(ctx, topic, partitions) error   // explicit; auto-creation races the first produce
Publish(ctx, topic, key, body, headers...) error   // same key ⇒ same partition
PublishMarked(ctx, topic, key, body, scenarioName) error
PublishToPartition(ctx, topic, partition, body) error
Lag(ctx, group, topic) (int64, error)
WaitForGroupCaughtUp(ctx, group, topic, timeout) error   // the broker's own "it consumed everything"
ResetGroup(ctx, group, topic) error         // replay; the service must be stopped
Read(ctx, topic, max, timeout) ([]kafka.Message, error)  // outbound or dead-letter topics
```

`WaitForGroupCaughtUp` is the right wait before asserting nothing was stored.
`ResetGroup` + restart is the replay that proves idempotence under the
at-least-once delivery Kafka actually promises.

## The receipt marker

`harness.MarkerField` (`e2e_case`) names the scenario that sent a message, so
each scenario has one unambiguous log line to wait for. `PublishMarked` adds it
— in the payload for MQTT, in a header for Kafka, in metadata for gRPC. Check
the contract says unknown fields are ignored; if it forbids them, vary a
harmless field instead.

## What a scenario file looks like

```go
func ingest(e *Env) runner.Suite {
    return runner.Suite{
        Name: "ingest",
        What: "the level consumer, against contract §3",
        Scenarios: []runner.Scenario{{
            Name: "unreadable_sensor_stores_nothing",
            Doc:  "contract §3.2: a reading marked invalid must not be recorded",
            Run: func(t *runner.T) {
                cursor := e.Log.Cursor()
                t.Check(e.MQTT.PublishMarked("tank/level", 1, false,
                    map[string]any{"device": "tank-01", "valid": false},
                    t.Name()), "publishing the reading")

                // Wait for proof the service handled it before asserting absence.
                _, err := e.Log.WaitFor(e.Ctx, 5*time.Second, t.Name())
                t.Check(err, "waiting for the service to log the message")

                n, err := e.DB.Count(e.Ctx, "SELECT count(*) FROM readings WHERE device = $1", "tank-01")
                t.Check(err, "counting readings")
                if n != 0 {
                    t.Errorf("an invalid reading was stored: %d rows for tank-01, want 0", n)
                }
                e.Log.Rewind(cursor)
            },
        }},
    }
}
```
