#!/usr/bin/env python3
"""Assemble an end-to-end suite from the pre-coded core and adapters.

The suite's runner, process control, log reader, waits and protocol adapters
are the same for every service — only the scenarios change. Generating them
token by token each run is waste, and a re-typed harness is a harness with
fresh bugs in it. This copies them instead, wires main.go for the adapters the
service actually uses, and leaves exactly one thing to write: the scenarios.

    python3 scripts/scaffold.py --name payments \\
        --module example.com/payments-e2e \\
        --adapters http,postgres,kafka \\
        --out ../payments-e2e

Then write internal/scenarios/*.go and run `go mod tidy`.

Adapters: http, grpc, postgres, mysql, redis, mqtt, kafka.
Use --list to see them with the dependencies each pulls in.
"""

import argparse
import pathlib
import re
import shutil
import sys

ROOT = pathlib.Path(__file__).resolve().parent.parent
SUITE = ROOT / "assets" / "suite"
ADAPTERS = ROOT / "assets" / "adapters"

PLACEHOLDER_MODULE = "e2e/suite"

# Per adapter: the go.mod requirements, the harness field, how it is built in
# main.go, and the flags it needs. Keeping this table here rather than in prose
# is the point: the wiring is derived, not remembered.
ADAPTER_SPEC = {
    "http": {
        "deps": [],
        "field": "HTTP *harness.HTTP",
        "flags": [("http", "127.0.0.1:8080", "the service's HTTP address")],
        "build": 'h.HTTP = harness.NewHTTP("http://"+*httpAddr, 10*time.Second)',
        "ready": 'h.HTTP.Ready("/health")',
    },
    "grpc": {
        "deps": ["google.golang.org/grpc"],
        "field": "GRPC *harness.GRPC",
        "flags": [("grpc", "127.0.0.1:9090", "the service's gRPC address")],
        "build": 'h.GRPC, err = harness.DialGRPC(ctx, *grpcAddr, 15*time.Second)',
        "ready": 'h.GRPC.Ready("")',
        "errcheck": True,
    },
    "postgres": {
        "deps": ["github.com/lib/pq"],
        "field": "DB *harness.Postgres",
        "flags": [("db", "postgres://e2e:e2e@127.0.0.1:5432/e2e?sslmode=disable", "postgres DSN")],
        "build": "h.DB, err = harness.OpenPostgres(ctx, *dbDSN)",
        "errcheck": True,
        "close": "h.DB.Close()",
    },
    "mysql": {
        "deps": ["github.com/go-sql-driver/mysql"],
        "field": "DB *harness.MySQL",
        "flags": [("db", "e2e:e2e@tcp(127.0.0.1:3306)/e2e?parseTime=true", "mysql DSN")],
        "build": "h.DB, err = harness.OpenMySQL(ctx, *dbDSN)",
        "errcheck": True,
        "close": "h.DB.Close()",
    },
    "redis": {
        "deps": [],
        "field": "Redis *harness.Redis",
        "flags": [("redis", "127.0.0.1:6379", "redis address")],
        "build": "h.Redis, err = harness.DialRedis(*redisAddr, 0, 5*time.Second)",
        "errcheck": True,
        "close": "h.Redis.Close()",
    },
    "mqtt": {
        "deps": ["github.com/eclipse/paho.mqtt.golang"],
        "field": "MQTT *harness.MQTT",
        "flags": [("mqtt", "tcp://127.0.0.1:1883", "mqtt broker URL")],
        "build": 'h.MQTT, err = harness.ConnectMQTT(*mqttBroker, "e2e-suite", 10*time.Second)',
        "errcheck": True,
        "close": "h.MQTT.Close()",
    },
    "kafka": {
        "deps": ["github.com/segmentio/kafka-go"],
        "field": "Kafka *harness.Kafka",
        "flags": [("brokers", "127.0.0.1:9092", "comma-separated kafka brokers")],
        "build": 'h.Kafka = harness.ConnectKafka(strings.Split(*brokers, ","))',
        "close": "h.Kafka.Close()",
    },
}

FLAG_VAR = {
    "http": "httpAddr", "grpc": "grpcAddr", "db": "dbDSN",
    "redis": "redisAddr", "mqtt": "mqttBroker", "brokers": "brokers",
}


def die(message):
    print(f"scaffold: {message}", file=sys.stderr)
    sys.exit(2)


def render_main(name, adapters):
    """Build main.go: flags, harness construction, teardown, suite order."""
    specs = [(a, ADAPTER_SPEC[a]) for a in adapters]

    flags, fields, builds, closes, readies = [], [], [], [], []
    for adapter, spec in specs:
        for flag, default, help_text in spec["flags"]:
            var = FLAG_VAR[flag]
            flags.append(f'\t{var} = flag.String("{flag}", "{default}", "{help_text}")')
        fields.append(f"\t{spec['field']}")
        if spec.get("errcheck"):
            builds.append(f"\t{spec['build']}\n\tif err != nil {{\n\t\treturn nil, nil, err\n\t}}")
        else:
            builds.append(f"\t{spec['build']}")
        if spec.get("close"):
            closes.append(f"\t\t{spec['close']}")
        if spec.get("ready"):
            readies.append(spec["ready"])

    imports = ["context", "flag", "fmt", "os", "os/exec", "regexp", "time"]
    if "kafka" in adapters:
        imports.append("strings")
    if not any(s.get("errcheck") for _, s in specs):
        builds.insert(0, "\t_ = err")
    import_block = "\n".join(f'\t"{i}"' for i in sorted(set(imports)))

    ready = readies[0] if readies else "func(context.Context) error { return nil }"
    nl = chr(10)
    tab = chr(9)
    flags_block = nl.join(flags)
    fields_block = nl.join(fields)
    builds_block = nl.join(builds)
    closes_block = nl.join(closes)
    env_fields_block = nl.join(
        tab + tab + f.strip().split()[0] + ": h." + f.strip().split()[0] + ","
        for f in fields)
    ready_note = "" if readies else (
        "\t// No transport adapter defines readiness: replace this with a real\n"
        "\t// call that proves the service is serving, never a sleep.\n")

    return f'''// Command {name}-e2e drives the built service over its real interfaces and
// reports what it does against what it promises.
//
// The runner, harness and adapters here are pre-coded and service-agnostic.
// Service knowledge belongs in internal/harness/fixtures.go and the
// expectations belong in internal/scenarios/.
package main

import (
{import_block}

\t"{{MODULE}}/internal/harness"
\t"{{MODULE}}/internal/runner"
\t"{{MODULE}}/internal/scenarios"
)

var (
\trepo    = flag.String("repo", "", "checkout of the service under test (required)")
\tpkg     = flag.String("pkg", ".", "main package within the repo")
\twork    = flag.String("work", "", "working directory for the binary and logs (default: a temp dir)")
\tkeep    = flag.Bool("keep", false, "keep the working directory after the run")
\trunExpr = flag.String("run", "", "only scenarios whose suite/name matches this regexp")
\tlist    = flag.Bool("list", false, "list what would run, without running it")
\tverbose = flag.Bool("v", false, "per-scenario notes while debugging")
\tjsonOut = flag.String("json", "", "write machine-readable results here for the report script")
{flags_block}
)

// Harness is everything the scenarios are handed.
type Harness struct {{
\tService *harness.Service
\tLog     *harness.LogFile
{fields_block}
}}

func main() {{
\tflag.Parse()
\tif *repo == "" && !*list {{
\t\tfmt.Fprintln(os.Stderr, "scaffolded suite: -repo is required (the checkout to build and run)")
\t\tos.Exit(2)
\t}}

\tvar filter *regexp.Regexp
\tif *runExpr != "" {{
\t\tvar err error
\t\tif filter, err = regexp.Compile(*runExpr); err != nil {{
\t\t\tfmt.Fprintf(os.Stderr, "bad -run expression: %v\\n", err)
\t\t\tos.Exit(2)
\t\t}}
\t}}
\tr := &runner.Runner{{Filter: filter, Verbose: *verbose}}

\tif *list {{
\t\tr.List(scenarios.All(nil))
\t\treturn
\t}}

\tctx := context.Background()
\th, cleanup, err := setup(ctx)
\tif err != nil {{
\t\t// A suite that could not be stood up and a suite that found a bug are
\t\t// different answers, and CI must treat them differently.
\t\tfmt.Fprintf(os.Stderr, "\\nthe environment could not be stood up: %v\\n", err)
\t\tos.Exit(3)
\t}}

\tpassed := r.Run(scenarios.All(h))
\tr.Summary()

\tif *jsonOut != "" {{
\t\tif err := r.WriteJSON(*jsonOut); err != nil {{
\t\t\tfmt.Fprintf(os.Stderr, "writing %s: %v\\n", *jsonOut, err)
\t\t}}
\t}}

\t// Tear down before exiting: a leaked service process keeps holding its
\t// port and will quietly answer the next run's requests.
\tcleanup()
\tif !passed {{
\t\tos.Exit(1)
\t}}
}}

func setup(ctx context.Context) (*scenarios.Env, func(), error) {{
\tdir := *work
\tif dir == "" {{
\t\tvar err error
\t\tif dir, err = os.MkdirTemp("", "{name}-e2e-"); err != nil {{
\t\t\treturn nil, nil, err
\t\t}}
\t}}
\tif err := os.MkdirAll(dir, 0o755); err != nil {{
\t\treturn nil, nil, err
\t}}
\tfmt.Printf("working directory: %s\\n", dir)

\th := &Harness{{}}
\tvar err error
{builds_block}

\tlogPath := dir + "/service.log"
\tif h.Log, err = harness.OpenLog(logPath); err != nil {{
\t\treturn nil, nil, err
\t}}

{ready_note}\th.Service = &harness.Service{{
\t\tRepo:      *repo,
\t\tPackage:   *pkg,
\t\tBinPath:   dir + "/service",
\t\tStdioPath: dir + "/stdio.log",
\t\tEnv:       []string{{"LOG_FILE=" + logPath}},
\t\tReady:     {ready},
\t}}
\tif err := h.Service.Build(ctx); err != nil {{
\t\treturn nil, nil, err
\t}}
\tif err := h.Service.Start(ctx, 30*time.Second); err != nil {{
\t\treturn nil, nil, err
\t}}

\tenv := &scenarios.Env{{
\t\tCtx: ctx, Service: h.Service, Log: h.Log,
{env_fields_block}
\t}}
\tcleanup := func() {{
\t\th.Service.Kill()
{closes_block}
\t\tif !*keep {{
\t\t\tif *work == "" {{
\t\t\t\t_ = os.RemoveAll(dir)
\t\t\t}}
\t\t}} else {{
\t\t\tfmt.Printf("kept: %s\\n", dir)
\t\t}}
\t}}
\treturn env, cleanup, nil
}}

var _ = exec.Command // kept for scenarios that shell out during provisioning
'''


def render_env(adapters):
    fields = [ADAPTER_SPEC[a]["field"] for a in adapters]
    nl = chr(10)
    tab = chr(9)
    env_fields = nl.join(tab + f for f in fields)
    return f'''// Package scenarios holds the only code in this suite with expectations in it.
//
// Each scenario is a name, a Doc naming the promise it pins and where that
// promise was made, and a function. They run in declaration order.
package scenarios

import (
\t"context"

\t"{{MODULE}}/internal/harness"
\t"{{MODULE}}/internal/runner"
)

// Env is everything a scenario is handed. Scenarios never construct a client
// or a driver themselves: waiting and error handling would get reinvented per
// scenario and diverge.
type Env struct {{
\tCtx     context.Context
\tService *harness.Service
\tLog     *harness.LogFile
{env_fields}
}}

// All returns every suite, in the order they must run:
//
//  1. self-check — the suite's own oracle and fixtures; if this fails,
//     nothing after it means anything
//  2. read/query interfaces — seeded state in, responses out
//  3. ingest interfaces — one suite per consumer or writer
//  4. logging — the wording of what the service records
//  5. lifecycle — restart, replay, port conflicts, shutdown; last, because
//     it stops the service
//
// It is called with a nil Env by -list, so it must not dereference e here.
func All(e *Env) []runner.Suite {{
\treturn []runner.Suite{{
\t\t// selfCheck(e),
\t\t// readAPI(e),
\t\t// ingest(e),
\t\t// logging(e),
\t\t// lifecycle(e),
\t}}
}}
'''


def render_gomod(module, adapters):
    deps = sorted({d for a in adapters for d in ADAPTER_SPEC[a]["deps"]})
    lines = [f"module {module}", "", "go 1.22"]
    if deps:
        # No require block with invented versions: `go mod tidy` resolves the
        # real ones from the imports, and a placeholder version makes it fail
        # with "unknown revision" instead.
        lines += ["", "// `go mod tidy` adds these from the adapter imports:"]
        lines += [f"//   {d}" for d in deps]
    return "\n".join(lines) + "\n"


def main():
    ap = argparse.ArgumentParser(description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
    ap.add_argument("--name", help="service name, used in the suite's own name")
    ap.add_argument("--module", help="Go module path for the suite")
    ap.add_argument("--adapters", default="", help="comma-separated: " + ", ".join(ADAPTER_SPEC))
    ap.add_argument("--out", help="directory to create")
    ap.add_argument("--force", action="store_true", help="overwrite an existing directory")
    ap.add_argument("--list", action="store_true", help="list adapters and exit")
    args = ap.parse_args()

    if args.list:
        print(f"{'adapter':10} {'dependency':40} field")
        for name, spec in ADAPTER_SPEC.items():
            print(f"{name:10} {(spec['deps'][0] if spec['deps'] else '(stdlib only)'):40} {spec['field']}")
        return

    for required in ("name", "module", "out"):
        if not getattr(args, required):
            die(f"--{required} is required")

    adapters = [a.strip() for a in args.adapters.split(",") if a.strip()]
    unknown = [a for a in adapters if a not in ADAPTER_SPEC]
    if unknown:
        die(f"unknown adapter(s): {', '.join(unknown)}. Known: {', '.join(ADAPTER_SPEC)}")
    if not adapters:
        die("--adapters is required: name what the service actually connects to")

    stores = [a for a in adapters if a in ("postgres", "mysql")]
    if len(stores) > 1:
        die(f"pick one SQL store, not {stores}: both define the Env field DB")

    out = pathlib.Path(args.out).resolve()
    if out.exists():
        if not args.force:
            die(f"{out} already exists (use --force to overwrite)")
        shutil.rmtree(out)

    # 1. the core, copied verbatim
    shutil.copytree(SUITE, out)

    # 2. the adapters the service actually uses
    for adapter in adapters:
        for path in sorted((ADAPTERS / adapter).glob("*.go")):
            shutil.copy(path, out / "internal" / "harness" / path.name)

    # 3. the wiring, derived from the adapter table
    (out / "main.go").write_text(render_main(args.name, adapters))
    (out / "internal" / "scenarios" / "env.go").write_text(render_env(adapters))
    (out / "go.mod").write_text(render_gomod(args.module, adapters))

    # 4. the module path, in every file that refers to it
    for path in out.rglob("*.go"):
        body = path.read_text()
        replaced = body.replace("{MODULE}", args.module).replace(PLACEHOLDER_MODULE, args.module)
        if replaced != body:
            path.write_text(replaced)

    written = sorted(p.relative_to(out).as_posix() for p in out.rglob("*") if p.is_file())
    print(f"scaffolded {out} with adapters: {', '.join(adapters)}\n")
    for path in written:
        print(f"  {path}")
    print(f"""
Next, and only this:

  1. cd {out} && go mod tidy
  2. write internal/harness/fixtures.go — the entities scenarios act as,
     including the ones meant to fail
  3. write internal/scenarios/<area>.go — one file per suite — and list them
     in All() in env.go
  4. go run . -repo <checkout> -json results.json

Everything else — the runner, process control, the log cursor, the waits and
the adapters — is already here and already works. Do not rewrite it.""")


if __name__ == "__main__":
    main()
