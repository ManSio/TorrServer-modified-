# EXPERIMENTS_LOG — TorrServer-modified

One row per experiment. Frozen inputs and hypotheses live in `experiments/<name>/frozen/`;
the full write-up is `experiments/<name>/REPORT.md`. A PASS is not a success unless the
report says what it proves.

| ID | Experiment | Frozen | Command | Raw result | Verdict |
|---|---|---|---|---|---|
| E-001 | `bts.client` nil-deref (KI-006) | 2026-10-04 | `go test ./torr -run 'TestWriteStatusNilClient\|TestConcurrentDisconnectAndWriteStatus\|TestNewTorrentDoesNotDeadlock' -v` | all PASS, `ok server/torr 12.868s` | H1–H4 CONFIRMED; H5–H7 REFUTED (as predicted) |
| E-002 | #925 zero-read on partial eviction (KI-009) | 2026-10-19 | `go test ./torr/storage/torrstor -run 'Issue925' -v`; fork `go test -run TestInvalidateStorage ./` | fork test PASS; server build/tests PASS | Fixed in code: tag v1.2.34-mansio.1 + Release call; not deployed |
| E-003 | opencode failure corpus | 2026-10-19 | see `experiments/oc-failure-analysis/scripts/` | 807 labeled, rule-vs-LLM 90.8% | superseded numbers recorded in that diary |
| E-004 | Zed failure corpus | 2026-10-20 | see `experiments/zed-failure-analysis/scripts/` | 669 threads, 85,433 tool events | closed; see `REPORT.md` |
| E-005 | Upstream sync (#928/#929/#934/#892) | 2026-10-24 | `go build ./... && go vet ./... && go test ./cmd/... ./web/api/... ./torr/...` | exit 0, all ok | 4 ported; #914 N/A; #911 rejected |
| E-006 | Engine v1.2.34 + #678 + #910 + #908 | 2026-10-24 | `go build ./... && go test ./torr/... ./settings/... ./web/pages/template/...` | build 0, all ok | v1.2.34 adopted; #678/#910 adopted; #908 partial |

## Negative results (do not repeat)

- E-002: a green wiring test does **not** prove a fix when the production call site is absent.
- E-002: adding `go.mod` `replace` to a local fork is not a fix until the tag is published and pinned.
- E-001: `atomic.Pointer` alone does not protect against `Close()` racing a reader.
