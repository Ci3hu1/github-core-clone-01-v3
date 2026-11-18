# github-core-clone-01 v3 — notes

## Entry

- Repo: https://github.com/Ci3hu1/github-core-clone-01-v3 — PUBLIC, created
  2026-11-18T19:57:37Z (after the 14 Nov opening), About (description +
  homepage) set. HEAD commit `b19c1d8e492b99b41fd8cfb22e4f7793bbad4397`
  (main). This is the exact entry to present to Joni for approval.
- `main.ts` — node22 launcher for the `forge` binary (fallback ladder:
  direct exec → copy to tmp → memfd via python3)
- `forge` — statically linked Go binary, 3,047,584 bytes (cap: 8 MiB in
  any one file, 16 MiB total tree). Built from `forge-src/` with
  `go build -ldflags="-s -w" -trimpath` (see `build.sh`).
- `forge-src/` — Go source (module `forge`, go 1.27, stdlib only):
  `main.go` (register/poll lifecycle), `http.go` (raw TCP server, response
  cache + single-flight), `store.go`, `git.go`, `rest.go`, `graphql.go`,
  `mcp.go`, `json.go`, `util.go`, plus `forge_test.go` (unit tests) and
  `bench_test.go` (pack-build / issue-render benchmarks).
- `tests/` — smoke test (`smoke_forge.py`, fake env + REST/GraphQL/MCP
  flow) and the server-side latency probe (`probe_server.py`).
- `grade-v2-baseline/` — v2 local grade for comparison.
- `grade-v3-t1/` — first v3 grade (feature 545: the 7 extra-`}` bug).
- `grade-v3-t2/` — second v3 grade (feature 600, score 993).
- `grade-v3-2cpu/` — taskset 2-CPU run (score 1000, perf 200).
- `grade-v3-3x/` — 3-instance median run (perf 180/190/190).
- Kit: fresh level 20
  (`~/workspace/tools/makefun/github-core-clone-01-kit-v3/github-core-clone-01-kit/`,
  harness 6cd2f710, scorer program@99d09372). Go 1.27.4 toolchain at
  `~/go-toolchain/go`.

## v2 (live, for comparison)

- Submission 01a1dd6c-65cd-7691-8487-dd44e63f75ad (round 2, scored),
  commit 30644a87, repo https://github.com/Ci3hu1/github-core-clone-01-v2
- Live: score ~916–923, feature 600, agent 200, perf 122,
  p50 597,261 µs, rank ~5000/8,578. v1 (01a1d93b) superseded, live 916.

## Live top 10 (19 Nov ~02:20 WIB, re-check before submit)

1. vinzsenzo 966 (perf 166, p50 322,015); 2. aryanpatelx 965;
3. ketong 963; 4. nughair-dex 960; 5. am-ai-abandonedmuse-ai-account 960;
6. satesapi 959; 7. haliluge 959; 8. mmo-alpha-agent 958; 9. makegreat 958;
10. alpaca 958. Cutoff for top 10 = 958 (perf 158, p50 ≤ 363,218 µs).
Only 11 entries have perf ≥158 / score ≥958. Leaders' pattern (titles):
static Go core, raw-TCP HTTP/1.1, zero-alloc hot paths, cached fetch
packs, pre-serialized hot paths, pooled inflaters, response cache.
No published first-place records (GET /api/records returned empty).

## Grade-local results (v3, process driver)

| run | feature | agent | perf | p50 (µs) | score | notes |
|-----|---------|-------|------|----------|-------|-------|
| t1 | 545 | 200 | 192 | 184,261 | 937 | 11 checks failed (extra-`}` bug) |
| t2 | 600 | 200 | 193 | 202,905 | 993 | bug fixed |
| 2-CPU | 600 | 200 | 200 | 163,957 | 1000 | taskset -c 0,1 |
| 3x t0 | 600 | 200 | 180 | 239,506 | 980 | write 39, clone 41 |
| 3x t1 | 600 | 200 | 190 | 211,239 | — | write 45, clone 45 |
| 3x t2 | 600 | 200 | 190 | 192,257 | — | write 50, clone 45 |

3-instance median: perf 190, p50 ~211k µs, feature 600, agent 200.
Per-workload (median): push ~275 ms (50 pts), clone ~90 ms (45 pts),
read ~670 ms (50 pts), write ~114 ms (45 pts).
Replay matches byte for byte in every run (L1 regrade reproduced).

## Honest live estimate

Local grades overstate live. v2: local 969 (perf 174) → live ~916–923
(perf 122). Applying the same local→live relationship to v3 gives live
perf ~140–168, score ~940–968. The top-10 cutoff is 958. v3 is
plausibly in the top 10 but NOT guaranteed — the live sandbox slowdown
(gVisor + shared nodes) is the uncertainty, and the bench numbers are
dominated by the harness client, not the server (direct probe: read
0.087 ms/req, write 0.129 ms/req server-side; pack build ~0.5 ms).
The submit itself reveals the live score.

## Checks

- `makefun check` (19 Nov, against the worktree): **6 passed**
  (competition_open, snapshot, license_osi, entrypoint, entry_size,
  committed_provider_key), **1 failed** (transcripts_present — no
  --transcripts bundle was given; the bundle is created at submit time),
  14 skipped (transcript/thinking checks need the bundle), 5 checked at
  submission by the site (repo_public, repo_owner, repo_created_after_open,
  commit_in_repo, duplicate_snapshot — repo is public, owned by ci3hu1,
  created after opening, commit pushed).
- Unit tests: `cd forge-src && go test ./...` (hash vectors, pack
  round-trip incl. deltas, inflate sizes 0–70000, store ops). All pass.
- Smoke test: `python3 tests/smoke_forge.py` (fake env + REST/GraphQL/
  MCP flow incl. labels, branches, commits, comments). Passes.

## Submit (DO NOT do without Joni's fresh 'Setuju' on this exact entry)

- Entries close 21 Nov 2026 13:30 WIB. Competition quota: 5/day for this
  competition, 3/day UTC account-wide shared (1 used today by the btc-race
  submit; check `makefun status --competition github-core-clone-01`
  before submitting).
- Withhold-source is a submit-time setting — decide it at submit time.
- Never print the API key or any claim code. Claim codes live only in
  `~/.config/makefun/<slug>-submission.json` (0600).
- After submit: verify round 1 passes and record the live score + rank.
