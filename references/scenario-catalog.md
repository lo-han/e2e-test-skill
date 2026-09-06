# Turning a contract into scenarios

Work this checklist against the contract from Phase 1. Not every line applies to
every service; the ones that do usually double the scenario count, and the extra
ones are where the defects are.

## For every interface

- **The happy path**, asserted on the *effect*, not the status code: a row with
  the right value, in the right units, at the right time.
- **Identity** — the effect lands against the entity named in the request, and
  no other entity's state moved.
- **The unknown id** — what a caller gets for something that was never
  registered.
- **Malformed input** — bytes that do not parse. Nothing stored, something
  logged, and the service still serving afterwards.
- **The dependency failing underneath it** — provoked as in
  `harness-patterns.md`, then recovery asserted.
- **Two in a row** — one bad message must not stop the next good one being
  processed. This catches consumers that die quietly on a poison message.

## Read / query interfaces

- Every documented route, with its documented shape — field names and types as
  the spec writes them, not as the code happens to emit them.
- Empty results: an empty list, a missing entity, an entity that exists but has
  no history. Distinguish the answers the spec gives to each.
- Optional fields absent when they should be absent (a run that has not ended
  carries no end time), which catches serialisation that emits nulls or zeros.
- Ordering, when the spec promises it — seed at least three items so a reversed
  comparison cannot pass by luck.
- Methods and routes that should not exist: the wrong verb, a route one segment
  too long. Check what the framework advertises as allowed actually works — a
  route that answers `Allow: GET, HEAD` and then refuses `HEAD` is a real bug
  found exactly this way.
- Failures behind the response: what the caller gets and what the operator gets
  when the query itself fails.

## Ingest interfaces — consumers, workers, webhooks

- Each message type in the contract, including the ones that must store nothing.
- **Null is not zero** — a nullable measurement absent must never be recorded as
  a zero measurement. Assert the stored value is *unchanged*, not that it is
  zero.
- Missing optional envelope fields — a message with no timestamp should be
  recorded at receipt time, within a window the scenario brackets.
- Messages of another type on the same channel — ignored, and logged as ignored.
- State transitions in the wrong order: a stop with nothing running, two starts,
  a stop after a stop. Assert the earlier record was not rewritten.
- Only the intended record changes: with two open records, a close must take the
  right one and leave the other.
- Replay and idempotence — a retained or redelivered message, after a restart,
  must not record the same thing twice.
- Last wills and liveness messages, where the contract has them: "the publisher
  is unreachable" is not "the thing stopped", and inventing an end time
  fabricates history.

## Logs

Treat the log as an interface with a contract of its own. It is the only account
of everything the service chose not to store, so its wording is part of what it
delivers.

- The line format itself — prefix, timestamp, whatever the configuration
  promises.
- Startup: the dependencies it connected to, the channels it subscribed to, the
  port it serves on.
- One line per handled request or message, and the payload logged verbatim where
  the service does that.
- Every "I did nothing" path: what was skipped, for which entity, and why.
- Every failure path: the underlying error, not a generic message.
- Not buffered — a line is on disk by the time the next call is served.

## Lifecycle

- Restart: the service comes back, keeps serving, keeps consuming, and appends
  to its log rather than truncating it.
- Replayed messages after a reconnect record nothing new.
- A dependency that cannot be acquired at startup — a taken port, an unreachable
  store — is reported, not swallowed. A process that stays up with half its
  interfaces dead and says nothing is the worst outcome, and services do this
  more often than anyone expects.
- Graceful shutdown: the documented signal, each documented step in the log, and
  the exit code.
- After shutdown, the interfaces really are closed.

## Scoping

If the checklist yields more scenarios than the session can build well, cover
every interface shallowly before covering any interface deeply — a suite that
touches all of them and misses edge cases is more useful than one that exhausts
a single endpoint. Say what you left out, so nobody mistakes silence for
coverage.
