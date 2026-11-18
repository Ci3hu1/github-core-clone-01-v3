# github-core-clone-01 — v3

A static Go forge for the [github-core-clone-01](https://make.fun/competitions/github-core-clone-01)
competition: a GitHub-core clone (REST v3, GraphQL v4, MCP, smart HTTP git)
that scores on feature, agent behaviour and raw speed.

## What it is

- `forge` — a single statically linked Go binary (3.0 MiB, stdlib only)
  that implements the whole forge: repositories, branches, commits,
  contents, issues, pull requests, GraphQL, MCP tools and the git smart
  HTTP protocol. Built from `forge-src/` (`go build -ldflags="-s -w"
  -trimpath`, see `build.sh`).
- `main.ts` — the node22 entrypoint. A thin launcher that execs `./forge`
  with a fallback ladder: direct exec → copy to a temp dir → memfd via
  python3 (read-only / noexec-safe checkout).
- `forge-src/` — the Go source: raw TCP HTTP/1.1 server, response cache +
  single-flight, hand-rolled JSON, pack/delta git engine, GraphQL
  lexer/parser/executor, MCP JSON-RPC tools. Unit tests
  (`forge_test.go`) and benchmarks (`bench_test.go`) included.
- `STRATEGY.md` — the measured edge, entry/exit rules, tried-and-dropped,
  expected outcomes. `NOTES.md` — build notes, grade results, submit
  checklist. `tests/` — smoke test + latency probe.

## Results (grade-local, fresh kit level 20)

- feature **600** (120/120 conformance checks), agent **200**
- perf **190** (3-instance median), p50 ~211k µs, score **~990**
- replay matches byte for byte in every run

v2 (Node) for comparison: live ~916–923 (perf 122). The live top-10
cutoff is 958 (perf 158). Live estimate for v3 is ~940–968 — plausibly
top 10, not guaranteed; local grades overstate live (see STRATEGY.md).

## Build

```sh
./build.sh   # rebuilds ./forge from forge-src/
```

## Test

```sh
cd forge-src && go test ./...        # unit tests
python3 tests/smoke_forge.py         # fake-env + REST/GraphQL/MCP flow
```
