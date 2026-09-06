# Adapters: standing up the stack the service actually uses

The suite is only black box if it speaks the service's real protocols. This file
is what turns the stack inventory from Phase 1 into a harness: for each adapter,
how to stand it up, how to know it is ready, what the deterministic "it has
processed this" marker is, how to force it to fail, and the scenarios that exist
only for that adapter.

Read the sections for the adapters the service actually uses and ignore the
rest. If an adapter is not here, work the five questions at the bottom.

Nothing in the rest of the skill assumes MQTT, PostgreSQL or HTTP. Where an
example names one, it is an example.

---

# Transports

## HTTP / REST

The default assumption elsewhere in the skill, and the least surprising.

- **Ready:** startup log line, then poll a health route until it answers.
- **Marker:** the response itself; the effect is asserted in the store.
- **Force failure:** bind its port before the service starts; malformed bodies;
  wrong content type; oversized payload.
- **Its own scenarios:** the wrong verb, a route one segment too long, a
  `HEAD`/`OPTIONS` the `Allow` header advertises, redirects, and the shape of
  the error body when the spec fixes it.
- Keep the raw body and headers on the client, not just a decoded struct — half
  the findings are about what the service emitted, not what parses out of it.

## gRPC

The transport that most changes the harness, for one reason: the suite needs
stubs, and the black-box rule says it may not import the service's.

- **Stubs:** generate them into the suite's own module from the `.proto` files,
  with the suite's own `protoc`/`buf` invocation. Never import the service's
  generated package — it makes the suite agree with the server by construction,
  which is exactly the agreement under test. Commit the generated code into the
  suite and record which `.proto` revision it came from. If the repository has
  no `.proto`, pull the schema from server reflection and say in the report that
  the contract was recovered from the server rather than from a document —
  a suite built that way can only confirm current behaviour.
- **Ready:** the standard health service (`grpc.health.v1.Health/Check`) where
  the service registers it, otherwise a real unary call. A successful dial
  proves nothing: gRPC connects lazily, so a plain `Dial` succeeds against a
  port with nothing behind it. Dial with a blocking option or call and retry.
- **Marker:** the response, plus the same store/log waits as anywhere else.
- **Errors are status codes, not bodies.** Assert the `code` the spec names
  (`InvalidArgument`, `NotFound`, `FailedPrecondition`, `AlreadyExists`), the
  message, and `status.Details` where the contract promises typed error details.
  A service that answers `Unknown` for everything is a finding on its own.
- **Force failure:** a deadline shorter than the work (assert the server
  observes cancellation, leaves no half-written record, and logs it); a message
  above `MaxRecvMsgSize` for `ResourceExhausted`; missing required metadata;
  plaintext against a TLS-required server; a call to a method that does not
  exist for `Unimplemented`.
- **Its own scenarios:**
  - Unknown method on a known service, and a known method sent the wrong
    request type — the gRPC equivalent of "the wrong verb, a route one segment
    too long".
  - **Streaming, one per mode the contract uses.** Client-streaming: half-close
    and assert the single response. Server-streaming: assert the message
    sequence, then that the stream is closed rather than left open.
    Bidirectional: interleaving, and a client cancel mid-stream — the server
    must stop work rather than keep writing.
  - An error raised *mid-stream*, after messages have already been delivered:
    services routinely handle a failed first message and mishandle a failed
    fifth.
  - Unknown fields in a request must be ignored and must survive a round trip
    (proto3 retains them) — this is what makes rolling upgrades safe, and it is
    rarely tested.
  - Whether reflection is enabled, when the contract says either way.
- **The receipt marker** (`harness-patterns.md` §3) goes in request metadata
  rather than in the message, since a proto message has no room for a field the
  schema does not define.

## Kafka

- **Standing it up:** there is no in-process Kafka the way there is an embeddable
  MQTT broker, so this is the documented exception to preferring in-process
  dependencies. In order of preference: a container (a single-node KRaft broker,
  or Redpanda, which starts in about a second) driven by a container library; or
  an already-running local broker addressed by a `-brokers` flag, with preflight
  failing loudly when it is absent. Either way the dependency sits in the
  *suite's* module — which is precisely why the suite is a separate module.
- **Ready:** metadata for the topics the service consumes is available, the
  service's startup log names its group and topics, and — the honest check —
  the group appears with an assigned member. Producing before the consumer has
  joined is the most common source of a suite that passes alone and fails in CI.
- **Create topics explicitly** with the partition count the contract implies,
  and wait for metadata to propagate before the first produce. Relying on auto
  creation races the first message and gives it one partition, which quietly
  destroys any ordering scenario.
- **Marker:** the usual store and log waits, plus one Kafka has and MQTT does
  not — **consumer group lag at zero** for the service's group is a precise
  "everything published so far has been processed", and it is the right wait
  for the scenarios asserting that *nothing* was stored.
- **Force failure:** a message whose bytes do not deserialise; a message on a
  topic the service subscribes to but whose type it does not handle; killing the
  service between processing and offset commit (below); a broker made
  unreachable mid-run, then restored, with recovery asserted.
- **Its own scenarios:**
  - **Redelivery is the headline scenario.** Kill the service after it has
    written its effect but before it commits the offset, then restart: the
    message is delivered again. At-least-once is what the broker promises, so
    the suite must assert the service is idempotent — one record, not two. This
    single scenario finds more than the rest of a Kafka suite combined.
  - **Ordering is per-partition only.** Scenarios that assert order must publish
    with the same key. Then write the opposite scenario: publish related events
    under different keys, so they land on different partitions, and assert the
    service does not assume a global order it was never promised.
  - **A poison message must not stall its partition.** The generic "two in a
    row" check is stronger here: publish the bad message and the next good one
    *to the same partition*, since a consumer that stops committing blocks
    everything behind it, not just itself.
  - **Rebalancing**, when the service is meant to scale: start a second instance
    in the same group, assert partitions are shared and nothing is processed
    twice or dropped, then stop one and assert takeover.
  - **Replay** is offsets, not retained messages: reset the group to the
    earliest offset (or start a fresh group id) and restart, then assert no
    duplicate records — the equivalent of the MQTT retained-message replay.
  - Tombstones (a null value) where the contract uses compacted topics, and the
    dead-letter topic where the contract has one: assert what lands there and
    that the envelope keeps enough to diagnose the original.
- **Headers** are the envelope: the receipt marker belongs in a header when the
  payload schema forbids unknown fields.

## MQTT

- **Standing it up:** an embeddable broker in the suite's own process. No
  container, no fixture files, and the suite stays one binary.
- **Ready:** the service's subscription log lines. Publishing before it has
  subscribed loses the message silently — MQTT has no offsets to fall back on.
- **Its own scenarios:** retained messages replayed into a fresh subscription
  after a restart; last will and testament — "the publisher is unreachable" is
  not "the thing stopped", and inventing an end time fabricates history; QoS
  levels the contract names; clean-session behaviour across a reconnect;
  wildcard subscriptions receiving a topic the service must ignore.

## AMQP / RabbitMQ

- **Ready:** the queue exists, is bound, and has a consumer registered — the
  management API states all three, so assert them rather than sleeping.
- **Its own scenarios:** ack, nack-with-requeue and nack-without; the redelivery
  flag set on a second delivery; the dead-letter exchange receiving what the
  service rejects; prefetch honoured under a burst; a queue that already holds
  messages when the service starts.

---

# Stores

## PostgreSQL

- **Schema:** apply migrations from the repository where they exist; otherwise
  reconstruct `schema.sql` from the statements in the data-access code.
- **Force failure:** a `BEFORE UPDATE` trigger that `RAISE`s on one table; a
  `REVOKE` of one privilege; `ALTER TABLE ... RENAME TO` inside a helper with a
  deferred restore — renaming keeps the table's identity, so pooled connections
  and cached statements survive it.
- **Its own gotchas:** errors on a statement's *execution* may surface only when
  rows are read, so a suite that checks a driver's immediate return can miss
  them — the defect this skill was written from was exactly that.

## MySQL / MariaDB

Close enough to PostgreSQL to be dangerous: the same suite shape works, and
three differences silently change what a scenario proves.

- **The engine has to be InnoDB.** A `FOREIGN KEY` on a MyISAM table is parsed
  and ignored, so a constraint-violation scenario passes against a constraint
  that does not exist. State the engine in the reconstructed schema rather than
  inheriting a default.
- **`sql_mode` decides whether bad input is an error.** Without
  `STRICT_TRANS_TABLES`, an over-long string is truncated and an invalid date
  becomes a zero date, both with a warning rather than an error — so "the
  service rejects malformed input" can pass because MySQL quietly repaired it,
  and a real defect stays hidden. Read the mode the service runs under, assert
  it in the self-check suite, and say in the report which mode the findings
  hold for.
- **Datetime precision defaults to seconds.** `DATETIME` without `(6)` truncates
  fractional seconds, which turns "recorded at receipt time, within a bracketed
  window" into a flake at window edges. Declare the precision the service's
  values need.
- **Force failure:** a `BEFORE UPDATE` trigger raising with
  `SIGNAL SQLSTATE '45000'` (there is no `RAISE`); `REVOKE ... ON db.table` for
  a rejected write; `RENAME TABLE` for a failing query — but verify the service
  actually errors, since the table cache and prepared statements behave
  differently here than in PostgreSQL. MySQL also offers a cleaner way to break
  a connection mid-flight than PostgreSQL does: find the service's threads in
  `information_schema.PROCESSLIST` and `KILL CONNECTION`, then assert it
  reconnects and the in-flight write did not report success.
- Reconstructing the schema: `AUTO_INCREMENT` rather than `SERIAL`, `utf8mb4`
  rather than an assumed default, and no `RETURNING` before 10.5 / MariaDB.

## Stores without a schema — Mongo, Redis, DynamoDB, files

The `schema.sql` step becomes "reconstruct the document, key or item shape from
the access code", shipped as whatever the store's setup takes (a script creating
collections and indexes, a key-space description). Everything else holds: seed
fixtures including broken ones, poll for the effect rather than sleeping, and
force failures by removing a permission, dropping a collection inside a helper
with a deferred restore, or filling a quota.

Two adapter-specific habits: assert on **indexes** the service depends on, since
a missing one is invisible until production; and for stores with eventual
consistency, poll rather than read once, and say in the report which consistency
level the scenarios ran at.

---

# An adapter that is not listed here

Find the client library in the dependency manifest, read how the service
configures it, and answer five questions in writing before any scenario:

1. **How does it get stood up for real?** In-process, container, or an external
   instance a flag points at — in that order of preference.
2. **How do you know it is ready?** Something the service or the dependency
   states, never a duration.
3. **What is the deterministic marker that a message was processed?** The effect
   in the store, a log line, or a broker-side position. Without one, every
   "nothing was stored" scenario is a race.
4. **How do you force it to fail, and undo it?** Always with a deferred restore,
   always followed by asserting recovery.
5. **What does it do that the others do not?** Redelivery, ordering guarantees,
   retention, cancellation, back pressure, consistency. That answer is the list
   of scenarios no generic checklist would have produced — and it is where the
   findings are.

Write the five answers into the contract from Phase 1. They are what the reader
of the report needs in order to know what the suite could and could not see.
