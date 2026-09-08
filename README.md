# e2e-test-skill

A [Claude Code](https://claude.com/claude-code) skill that builds **end-to-end
test applications** for a service, from the service's own source code plus its
specification.

A unit test asks whether a function is correct. This asks a different question:
does the built binary, wired to a real database and a real broker, do what its
documentation promises? Those two questions fail in different places — most of
what this finds lives in the seams unit tests never cross, and in the failures a
service swallows rather than reports.

## What it produces

1. **A test application** — a standalone program in its own module, outside the
   repository it tests, importing nothing from it.
2. **A run** of that application against the real service.
3. **An HTML report** published as an artifact: every scenario, its result, the
   findings, and the suite's own source.
4. **The suite, compressed**, handed back to you.
5. **A question** — which of the problems found should be fixed.

## Pre-coded, not re-generated

The runner, process control, log cursor, waits and one adapter per technology
are **shipped as working Go** under `assets/`, and a script assembles them:

```bash
python3 scripts/scaffold.py --name payments \
    --module example.com/payments-e2e \
    --adapters http,postgres,kafka \
    --out ../payments-e2e
```

That emits a suite that compiles and runs, with `main.go` wired for those
adapters — their flags, readiness probes and teardown already correct. Only the
scenarios, the fixtures and the schema are written per service, because only
those actually differ between one service and the next.

The point is cost. Roughly 2,200 lines of harness — about 18k tokens — is
copied rather than generated, on every service tested, and the model reads a
one-page API summary (`references/harness-api.md`) instead of the sources. What
is left to write is the part that needed judgement anyway.

## Any stack

The suite is built against whatever the service actually connects to, determined
by reading its dependency manifest, configuration and connection code rather
than assumed. `references/adapters.md` carries, per adapter, how to stand it up,
how to know it is ready, what counts as a deterministic "it processed this",
how to force it to fail, and the scenarios that exist only because of it:

| | |
| --- | --- |
| **Service interfaces** | HTTP/REST, gRPC (including streaming, deadlines and status details) |
| **Ingest** | Kafka, MQTT (AMQP documented, not yet pre-coded) |
| **Stores** | PostgreSQL, MySQL/MariaDB, Redis, and stores with no schema at all |

Anything not on that list is handled by a five-question method in the same file,
so an unlisted adapter degrades to "work it out deliberately" rather than to
"assume it looks like HTTP". The architecture, the runner and the report are the
same whatever the answers are; the harness and a good part of the scenario list
are not.

The generated suite deliberately never enters the service's repository or its
pull requests: test-only dependencies stay out of the service's module, and a
large diff of test code never drowns a two-line fix under review.

## Installing

Add this repository as a marketplace, then install the plugin:

```bash
claude plugin marketplace add lo-han/e2e-test-skill
claude plugin install e2e-test-app@e2e-test-skill
```

That installs it for your user. Pass `--scope project` to scope it to the
repository you are working in instead, and run
`claude plugin update e2e-test-app` to pick up later changes.

## Using it

Give Claude the two things the skill requires — it will ask for whichever is
missing, because it needs both:

- **the application code**: a path, a repository, or a branch it can build;
- **a specification**: a README, a protocol or event contract, an OpenAPI
  document, a spec folder, or the doc comments on the service's interfaces.

Then ask for what you want, in whatever words come naturally:

> Build an end-to-end test suite for ./payments-api against docs/api-contract.md,
> run it, and tell me what breaks.

Code alone only ever confirms the service's current behaviour, bugs included. A
spec alone says what should happen but gives nothing to run. The findings come
out of the gap between them.

## What's in here

| Path | |
| --- | --- |
| `SKILL.md` | the workflow: required inputs, contract, scenarios, generation, the run, the report, and what to do about what it found |
| `references/architecture.md` | the suite's module layout, what each layer owns, and why it is a program rather than a test package |
| `references/harness-patterns.md` | the mechanics that keep a black-box suite deterministic: cursor-based log waiting, per-scenario receipt markers, process lifecycle, oracles, forcing error paths |
| `references/scenario-catalog.md` | a checklist for turning a contract into scenarios, by interface type — what every service owes its callers, whatever it runs on |
| `references/adapters.md` | per-adapter behaviour and the scenarios that exist only for it — HTTP, gRPC, Kafka, MQTT, AMQP, PostgreSQL, MySQL, Redis, schemaless stores — and how to work out one that is not listed |
| `references/harness-api.md` | the pre-coded harness surface the scenarios call, in a page — read instead of the adapter sources |
| `assets/suite/` | the service-agnostic core: runner, process control, log cursor, waits |
| `assets/adapters/` | one adapter per technology, copied into a suite by the scaffold |
| `scripts/scaffold.py` | assembles a suite from the core plus the chosen adapters, and wires `main.go` |
| `references/report-manifest.example.json` | a filled-in manifest for the report script |
| `scripts/build_report.py` | renders the run's report page — scenarios and results, findings, and the suite's source — from a JSON manifest |
| `.claude-plugin/` | the plugin and marketplace manifests that make `claude plugin install` work |

## License

MIT
