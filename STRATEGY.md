# github-core-clone-01 — v3 strategy

## Measured edge (why v3, why Go)

v2 (Node/TypeScript, submission 01a1dd6c) is live at score ~916–923
(feature 600, agent 200, perf 122, p50 597,261 µs; rank ~5000/8,578).
The whole gap to the top 10 is perf: the live top-10 cutoff is score 958
(perf 158, p50 ≤ 363,218 µs). v2's Node server is the bottleneck — its
local perf is ~174 and its per-request cost is dominated by Node's HTTP +
JSON + GC overhead.

v3 replaces the runtime, not the semantics: the forge is a **single
statically linked Go binary** (`forge`, 3.0 MiB, stdlib only) that
implements the exact v2 behaviour — same routes, same shapes, same fixed
timestamps, same deterministic commit/message bytes — so the 120/120
conformance suite passes unchanged, while the hot paths get Go's
throughput. The binary is launched by a thin `main.ts` (node22) with a
fallback ladder (direct exec → copy to tmp → memfd via python3), the
pattern the live top entries use for read-only / noexec checkouts.

Measured, grade-local (process driver, 3-instance median, fresh kit
level 20):

|                | v2 local | v3 local | v3 live (est.) |
|----------------|----------|----------|----------------|
| feature        | 600      | **600**  | 600            |
| agent          | 200      | **200**  | 200            |
| perf           | ~174     | **190**  | ~140–168       |
| p50            | ~255k µs | **~203k µs** | ~430–475k µs |
| score          | ~969     | **~990** | ~940–968       |

Local trials (post-fix): 993, 1000 (2-CPU), 980/—/— (3×: 180/190/190).
All 120 conformance checks pass in every trial. Replay matches byte for
byte (L1 regrade reproduced).

Direct server-side probe (raw keep-alive client, no bench client):
read 0.087 ms/req, write 0.129 ms/req — the server is not the bottleneck;
the bench numbers are dominated by the Node bench client + the shared VM.
Pack build for a depth-1 clone: ~0.5 ms (Go benchmark).

## Entry / exit rules (what the forge does)

- **Transport**: raw TCP HTTP/1.1 server (no net/http — binary size and
  latency). One `Write` per response, `TCP_NODELAY` on every accepted
  connection (Go leaves Nagle on by default; Node disables it — forgetting
  this costs the read workload), keep-alive pooling, `Expect:
  100-continue` supported, chunked request bodies supported.
- **Concurrency**: one goroutine per connection; the store is guarded by
  one `sync.RWMutex` (read lock for GETs / upload-pack, write lock for
  mutations), so a render never sees a half-applied mutation.
- **Response cache + single-flight**: GETs (REST + git advertisement) and
  the repeated identical upload-pack POSTs of the clone workload are
  cached, keyed by a global generation counter that bumps after any
  mutating request. Concurrent identical requests share one build
  (single-flight) instead of each rebuilding the same pack — the 50
  concurrent clones build the pack once. Cached entries hold only
  status/headers/body; `X-GitHub-Request-Id` is minted fresh per response.
- **Git**: pkt-line, pack parse/write, OFS/REF delta resolution, upload-pack
  v2 (fetch / ls-refs) + v0, receive-pack v0, sideband. Inflate end is
  found by Adler-32 scan + re-inflate verification (Go's flate over-reads,
  so a counting reader cannot pin the stream end). Packs are written with
  `flate.BestSpeed`.
- **JSON**: hand-rolled parser + writer (no `encoding/json` — too big and
  too slow for the cap). Responses are rendered in a fixed field order
  with a fast path for strings that need no escaping. GraphQL is a small
  lexer/parser/executor for the exact documents the clients send.
- **Determinism**: fixed timestamps (2025-01-01T00:00:00Z), fixed viewer
  (`adaiversen`, id 1000), creation-order sequence numbers, no wall clock
  or unseeded randomness in any response. Replay matches byte for byte.

## Tried and dropped

- **Node v2 with more tuning**: exhausted. The algorithmic wins (cached
  fetch packs, pre-serialized hot paths) were already in v2; the remaining
  cost is Node's per-request overhead. Only a runtime change moves perf.
- **net/http server**: rejected — the binary would exceed the 8 MiB
  single-file cap and the per-request cost is higher than raw TCP.
- **`encoding/json`**: rejected — same size/latency reason.
- **Python (main.py) launcher**: the grade-local process driver runs
  node22 only (python3.13 is Docker-only, and Docker is not installed on
  this VM), so the entry script is `main.ts`. The memfd fallback still
  goes through python3, which is present in the sandbox image.
- **Caching the upload-pack POST without single-flight**: the 50
  concurrent clones all miss the cache at once and each builds the pack.
  Single-flight fixes the thundering-herd.
- **Bumping the generation after every POST**: the upload-pack POST is
  read-only — bumping after it changes the cache key under every
  concurrent clone and the cache never hits. Only genuinely mutating
  requests bump the generation.

## Expected outcomes

- Local (grade-local, process driver): feature 600, agent 200,
  perf ~190, score ~990. Honest caveat: local grades overstate live —
  v2's local 969 became live ~916–923 (perf 174 → 122).
- Live (estimate, not a promise): perf ~140–168, score ~940–968. The
  top-10 cutoff is 958 (perf 158). v3 is plausibly in the top 10 but not
  guaranteed — the live slowdown (gVisor + shared nodes) is the
  uncertainty, and the bench numbers are dominated by the harness client,
  not the server. The submit itself is what reveals the live score.

## What changed vs v2

- Runtime: Node/TypeScript → static Go binary + thin `main.ts` launcher.
- Same API surface and semantics (120/120 checks pass, byte-identical
  replay). No behaviour change, only speed.
- Added: response cache + single-flight, raw TCP server, hand-rolled JSON,
  Adler-scan inflate end detection, Go unit tests (hash vectors, pack
  round-trip, delta apply, store ops).
- Fixed during grading: 7 spurious extra `}` closes in the Go JSON
  writers (labels, branches, commits, contents listings, comments list,
  pull files) that malformed responses and cost 55 feature points in the
  first grade-local run (545 → 600).
