# EXPERIMENTS_LOG вЂ” TorrServer-modified

One row per experiment. Frozen inputs and hypotheses live in `experiments/<name>/frozen/`;
the full write-up is `experiments/<name>/REPORT.md`. A PASS is not a success unless the
report says what it proves.

| ID | Experiment | Frozen | Command | Raw result | Verdict |
|---|---|---|---|---|---|
| E-001 | `bts.client` nil-deref (KI-006) | 2026-10-04 | `go test ./torr -run 'TestWriteStatusNilClient\|TestConcurrentDisconnectAndWriteStatus\|TestNewTorrentDoesNotDeadlock' -v` | all PASS, `ok server/torr 12.868s` | H1вЂ“H4 CONFIRMED; H5вЂ“H7 REFUTED (as predicted) |
| E-002 | #925 zero-read on partial eviction (KI-009) | 2026-10-19 | `go test ./torr/storage/torrstor -run 'Issue925' -v`; fork `go test -run TestInvalidateStorage ./` | fork test PASS; server build/tests PASS; deployed sha `8a8b8829вЂ¦` | Deployed 2026-10-24 20:32; live zero-read not proven |
| E-003 | opencode failure corpus | 2026-10-19 | see `experiments/oc-failure-analysis/scripts/` | 807 labeled, rule-vs-LLM 90.8% | superseded numbers recorded in that diary |
| E-004 | Zed failure corpus | 2026-10-20 | see `experiments/zed-failure-analysis/scripts/` | 669 threads, 85,433 tool events | closed; see `REPORT.md` |
| E-005 | Upstream sync (#928/#929/#934/#892) | 2026-10-24 | `go build ./... && go vet ./... && go test ./cmd/... ./web/api/... ./torr/...` | exit 0, all ok | 4 ported; #914 N/A; #911 rejected |
| E-006 | Engine v1.2.34 + #678 + #910 + #908 | 2026-10-24 | `go build ./... && go test ./torr/... ./settings/... ./web/pages/template/...` | build 0, all ok | v1.2.34 adopted; #678/#910 adopted; #908 partial |

## Negative results (do not repeat)

- E-002: a green wiring test does **not** prove a fix when the production call site is absent.
- E-002: adding `go.mod` `replace` to a local fork is not a fix until the tag is published and pinned.
- E-001: `atomic.Pointer` alone does not protect against `Close()` racing a reader.

| E-007 | Real holes from closed PRs/engine v1.2.36 | 2026-10-24 | `go test ./torr/... ./torr/storage/torrstor/... ./cmd/... ./web/api/... ./settings/...`; fork `go test ./` | all ok; deployed `ce047db0…`; settings reconnect 200 in 3.78 s, 0 errors | #909/#938 + DHT-close + multi-peer adopted; tiers/drop-chunks rejected; multi-peer A/B not measured |

| E-008 | x-ui/Xray log noise (212.113.101.151) | 2026-10-24 | access log + IPv6 probes + loglevel/metrics experiment | 0 ERROR/0 DEBUG in 60 s; UDP 46630 active | noise = client per-connection failures; loglevel none + metrics block |

| E-009 | Upstream catch-up MatriX.148–150 | 2026-11-06 | `go test ./torr/... ./torr/storage/torrstor/... ./web/api/... ./torznab/... ./cmd/...`; reconcile `-count=20` | all ok; 20/20 | #957/#941/#942/#955/#952 ported; #954/#933/WAF deferred |
| E-010 | Upstream audit 17.10–07.11 | 2026-11-07 | `go test ./torr/... ./torrfs/... ./torznab/... ./web/api/...`; `go build ./...` | all ok; deployed `1e68436c…` | #949/#933/#947 ported; #954/#958 N/A (no gst); WAF/subs N/A |

| E-011 | Core benchmarks baseline (torrstor/torr) | 2026-11-07 | `go test -run '^$' -bench . -benchmem ./torr/storage/torrstor ./torr` | MemWrite 4.9µs/256K; MemRead64K 1.5µs; DiskWrite 11.8µs; DiskRead64K 4.8µs; evict64 2.3µs/9 allocs; buffered 6.8µs vs raw 1.2µs (memory source) | baseline; no optimization until a profile shows a hot spot |
