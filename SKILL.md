---
name: e2e-test-app
description: Build a standalone end-to-end test application for a service from its source code plus a specification, run it against the real built binary, report what passed and failed, and hand back the suite compressed as an artifact. Use this whenever someone wants a service tested end to end, black box, or "for real" rather than with unit tests — phrasings like "write e2e tests for this API", "test my server against its spec", "build an integration test app for this worker", "I want a test application for these endpoints and consumers", "check my service actually does what the docs say", or when they hand over a repository and a contract/README/OpenAPI file and ask what breaks. Also use it when someone asks to test HTTP endpoints, queue or stream consumers, background workers, or the log lines a service produces. Requires both the application code and a specification; ask for whichever is missing rather than guessing.
---

# End-to-end test applications

A unit test asks whether a function is correct. This asks a different question:
does the built binary, wired to a real database and a real broker, do what its
documentation promises? Those two questions fail in different places — most of
what this finds lives in the seams the unit tests never cross, and in the
failures a service swallows rather than reports.

The deliverables, every time:

1. **A test application** — a standalone program in its own module, outside the
   repository it tests, importing nothing from it.
2. **A run** of that application against the real service.
3. **An HTML report** published as an artifact: every scenario, its result, the
   findings, and the suite's own source.
4. **The suite compressed** and handed to the user.
5. **A question**: which of the problems found should be fixed?

## Inputs this needs before starting

Two things, and it is worth being direct about asking for them:

- **The application code** — a path, a repository, or a branch. The suite is
  built against a real checkout it can compile and run.
- **A specification** — whatever states what the service is supposed to do:
  a README, a protocol or event contract, an OpenAPI document, a spec folder,
  a ticket. Doc comments on interfaces count and are often the sharpest source.

Both matter, for different reasons. Code alone tells you what the service does,
so a suite built from it only ever confirms the current behaviour, bugs
included. A spec alone tells you what it should do but not what to plug into.
The findings come from the gap between them, so if one is missing, ask for it
before writing anything. If the user insists there is no written spec, say what
you will use in its place (the README, the doc comments, the contract the
callers rely on) and get agreement on that before building — a suite whose
expectations nobody has agreed to is a suite whose failures nobody trusts.

## Phase 1 — Write down the contract before writing any test

Read the code and the spec together, and produce a short written contract: for
each entry point, what it accepts, what it must store or answer, what it must
refuse, and what it must record when it refuses. Include where the promises
disagree with the code — those are the first candidate findings.

Then work out what the service talks to and how each dependency will be stood
up for real. Prefer a real dependency in-process or local (an embeddable broker,
a local PostgreSQL, a temporary directory) over a mock, because the behaviours
worth testing — retained messages, constraint violations, connection failures —
only exist in the real thing. Note what the service needs from its environment:
env vars, ports, schemas, files.

When the service's storage has no migrations in the repository, reconstruct the
schema from the statements in its data-access code: every column it selects,
inserts or updates, typed as the value it scans into. Ship that as a `.sql` file
in the suite and say where it came from.

## Phase 2 — Enumerate the scenarios

Read `references/scenario-catalog.md` and work the checklist against the
contract. Aim for coverage of failure paths, not a round number of tests: the
happy path is one scenario, and the ways it can go wrong are twenty.

Group scenarios into suites by area (one per entry point, one for the logs, one
for lifecycle). Give each scenario a name that reads as a sentence about
behaviour (`state_off_without_an_open_run_records_nothing`) and a one-line `Doc`
naming the promise it pins and where that promise was made. When a scenario
fails, that line is what tells the reader whether the service or the suite is
wrong, so it earns its place.

## Phase 3 — Generate the suite

Read `references/architecture.md` for the layout and the reasoning, and
`references/harness-patterns.md` for the concrete mechanics — process control,
log-cursor synchronisation, the receipt marker, oracles, preflight checks.

The structure, which stays the same across services:

```
<service>-e2e/
  main.go            entry point: flags, environment, suite order, reporting
  README.md          what it covers, how to run it, what it currently reports
  scripts/           provisioning a caller runs once (database role, etc.)
  internal/runner/   scenario + suite types, assertion sink, the report
  internal/harness/  everything the service talks to, and how the suite watches it
  internal/scenarios/ the test cases, one file per suite
```

Write it in the service's own language when that language can build and run a
subprocess comfortably; Go is a good default even for services written in
something else, since the suite ships as one binary with no runtime to install.

Four properties make the difference between a suite people trust and one they
delete:

- **Black box.** It imports nothing from the service. It drives the built
  binary over its real interfaces, so what passes is what deploys.
- **Deterministic.** Every wait is on something the service actually did — a
  row appearing, a log line landing — never a sleep. Flakes destroy a suite's
  credibility faster than gaps in it.
- **Self-checking.** Where the suite computes an expected value, pin that
  oracle against numbers the service's own tests already assert, and run it as
  the first suite. An oracle that disagrees with the code's tests is not an
  oracle.
- **Honest about its own bugs.** Suite bugs and service defects both show up as
  red. Telling them apart is the work; see Phase 4.

## Phase 4 — Run it, and separate suite bugs from real defects

Run the suite. For each failure, decide which of two things it is:

- **A suite bug** — a wrong expectation, a race, a fixture mistake. Fix it in
  the suite and rerun.
- **A real defect** — the service does not keep a promise. Leave the scenario
  failing, and rewrite its failure message so it states the defect and its
  consequence, not just the mismatch. "The run was not recorded and nothing was
  logged: the event was lost with no trace" is a bug report; "expected 1, got 0"
  is a puzzle.

Never weaken an assertion to get green, never skip a failing scenario, and never
edit the service to make the suite pass — at this stage the service is evidence,
not a work item. Before reporting a defect, confirm the mechanism: reproduce it
a second, independent way, or with a small probe against the library involved.
A finding you have only inferred is a finding that wastes someone's afternoon.

Then run the whole suite at least twice and compare the outcomes. Identical
results are what let you say the failures are real; differing ones mean you have
a flake to fix before reporting anything.

## Phase 5 — Report

Summarise in the conversation first: a per-suite pass/fail table, then each
finding with its mechanism, its consequence, and the fix you would make.

Then build the HTML page with the bundled script rather than hand-writing one —
it owns the layout and the palette, so every report in this style comes out
consistent:

```bash
python3 scripts/build_report.py manifest.json report.html
```

The manifest schema is documented at the top of the script, and
`references/report-manifest.example.json` is a filled-in one. It carries the
scenarios and their results, the findings, how to run the suite, and the suite's
own source, which the page renders as a browsable, syntax-highlighted listing.
Take the scenario list from the suite's machine-readable output where it has one
rather than retyping it, and trim assertion-framework noise out of failure
details so each reads as a sentence.

Publish the page with the Artifact tool. Give it a real name — the service's
suite, not "Test Report" — and a one-sentence description.

## Phase 6 — Hand back the suite, compressed

```bash
tar -czf <service>-e2e.tar.gz <service>-e2e
```

Deliver that archive to the user with whatever file-delivery tool is available,
alongside the artifact link.

**The suite never enters the service's repository.** Not as a commit, not as a
branch, not in a pull request — not even in a PR that also carries fixes it
found. This is deliberate: it keeps test-only dependencies out of the service's
module, keeps its CI unchanged, and keeps a large diff of test code from
drowning a two-line fix under review. If the suite already sits inside a
checkout while you work, keep it untracked, and if a repository must eventually
hold it, that is a separate decision the user makes explicitly.

When the suite lives outside the repository it tests, make sure it says so: a
`-repo` style flag pointing at the checkout, and a README that does not assume
it sits inside one.

## Phase 7 — Ask what should be fixed

Close by asking the user which of the findings to fix, using `AskUserQuestion`
with one option per finding plus "all" and "none". Give each option enough
context to decide — what breaks today, and how big the fix is.

Fixing is a separate piece of work with a different rule: **fixes go to the
service's repository, on a branch, in a pull request — and the suite still does
not.** Describe the defect and the evidence in the PR body; the suite is how you
found it, not something the PR carries. Then rerun the suite against the fixed
service so the report reflects the change, and say what moved.

## Reference files

- `references/architecture.md` — the module layout, what each file owns, and why
  the suite is a program rather than a test package.
- `references/harness-patterns.md` — the mechanics that make it deterministic:
  process lifecycle, log-cursor waiting, receipt markers, oracles, preflight.
- `references/scenario-catalog.md` — the checklist for turning a contract into
  scenarios, by interface type.
- `references/report-manifest.example.json` — a filled-in manifest for the
  report script.
