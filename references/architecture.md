# The suite's architecture

## Why a program and not a test package

A suite built with the language's test runner is tempting: less scaffolding, a
familiar command. It goes wrong in three ways that matter here.

It runs inside the service's module, so the service picks up test-only
dependencies — an embedded broker, a container library — that ship with it
forever. It gets the runner's execution model, which parallelises and randomises
by default, while these scenarios share one server process, one database and one
log file and must run in a fixed order. And its output is a pass/fail stream
designed for a terminal, when the deliverable here is a report someone reads.

So: a program. It owns its own module, its own flags, its own lifecycle, and its
own report. It can be handed to someone who has never seen the service and run
in one command.

## Layout

```
<service>-e2e/
  main.go              flags, config, environment setup, suite order, exit code
  go.mod               its own module; the service's module never learns it exists
  README.md            what it covers, how to run it, what it currently reports
  scripts/             one-time provisioning a person runs (database role, etc.)
  internal/runner/
    runner.go          Scenario, Suite, Result, the runner, the report
  internal/harness/
    config.go          ports, paths, topics, timeouts, preflight checks
    schema.sql         the storage schema, reconstructed from the service's queries
    <store>.go         apply schema, seed fixtures, read rows back, poll for writes
    fixtures.go        the entities scenarios act as, including ones meant to fail
    <transport>.go     the broker/queue the service consumes from, in-process
    events.go          the payloads the spec fixes, exactly as they go on the wire
    server.go          build, start, wait-until-ready, restart, signal, stop
    logfile.go         cursor-based reader over the service's log
    api.go             HTTP client that keeps status, headers and raw body
    oracle.go          any value the suite must compute independently
  internal/scenarios/
    env.go             what a scenario is handed, plus publish/await helpers
    <area>.go          one file per suite of scenarios
```

## What each layer owes the others

**runner** knows nothing about the service. It defines what a scenario is,
collects assertion failures, catches panics, runs suites in order, and prints
the report. Keeping it service-agnostic is what lets the same framework carry
the next service's suite.

Use the language's usual assertion library rather than writing one — most take
an interface small enough to implement over the runner's own failure collector
(in Go, testify's `assert` needs only `Errorf`). That buys good diffs for free.

**harness** owns every piece of the outside world and every way of watching the
service. Scenarios should never call a database driver or an HTTP client
directly; when they do, waiting and error handling get reinvented per scenario
and diverge. If a scenario needs a new way to observe the service, that goes in
the harness.

**scenarios** are the only place with expectations in them. Each is a name, a
`Doc` naming the promise it pins, and a function. They run in declaration order
and may depend on the state the previous ones left, but each should seed what it
needs so a `-run` filter over a single scenario still works.

## Ordering

Suites run in a fixed order, roughly cheapest and most foundational first:

1. **Self-check** — the suite's own oracle and fixtures. If this fails, nothing
   after it means anything, and the reader needs to know the suite is at fault.
2. **Read/query interfaces** — seeded state in, responses out. Fast, and they
   establish that the service is alive and wired up.
3. **Ingest interfaces** — one suite per consumer or writer.
4. **Logging** — the wording of what the service records, especially for the
   paths that store nothing.
5. **Lifecycle** — restart, replay, port conflicts, shutdown. Last, because it
   stops the service.

## The command

Flags worth having, because each answers a question someone will have:

| Flag | Why |
| --- | --- |
| `-repo` | which checkout to build; the suite lives outside it |
| `-db` / connection settings | the environment it runs against |
| `-run <regex>` | work on one area without waiting for the rest |
| `-list` | see what would run without running it |
| `-v` | per-scenario notes while debugging |
| `-json <path>` | machine-readable results, which the report script consumes |
| `-work` / `-keep` | where the built binary and logs go, and whether they survive |

Exit `0` when everything passed, `1` when a scenario failed, and something else
when the environment could not be stood up — a suite that could not run and a
suite that found a bug are different answers, and CI should treat them
differently.

One subtlety worth getting right: if the run ends by exiting the process on
failure, make sure teardown has already happened. A leaked service process keeps
holding its port and will quietly answer the *next* run's requests, which
produces failures that look nothing like their cause.
