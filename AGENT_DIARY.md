# AGENT_DIARY — TorrServer-modified

Living diary for this fork. The authoritative issue registry is `KNOWN_ISSUES.md`;
experiment reports live under `experiments/<name>/REPORT.md`.

## [2026-10-01] CACHE-LIMIT (KI-003) — Status: 🟢 deployed 2026-10-01
- Root Cause: budget counted only in-memory caches; orphaned disk bytes were invisible (`8.6 GiB` counted vs `129.5 GiB` on disk).
- Fix: disk-truth budget (`filledSize` + `orphanBytes`), transfer on `Cache.Close`, 2-phase eviction, reader window from readahead.
- Guard: `cache_limit_test.go`, `leak_repro_test.go`, `[CACHE]` log, `/stat` via `WriteCacheStatus`.
- Not verified: `-race` (no gcc), live torrent with a reader.

## [2026-10-04] CLIENT-RACE (KI-006) — Status: ✅ fixed `d739331`
- Root Cause: readers of `bts.client` did not enter `bt.mu`; `Disconnect()` set it to nil under the lock.
- Fix: readers hold `bt.mu.RLock` for the whole call; nil client returns an error.
- Guard: `TestWriteStatusNilClient`, `TestConcurrentDisconnectAndWriteStatus`, `TestNewTorrentDoesNotDeadlock`.
- Report: `experiments/bts-client-race/REPORT.md`.

## [2026-10-17] BACKPORTS (KI-007) — Status: 🟢 deployed
- Fixes: preload vs drop (`c3a0642e`), stream 404/503 (`ef4e461d`), torznab URL+UA (`74e65fb2`).
- Guard: `preload_nil_test.go`, `stream_status_test.go`, `torznab_backport_test.go`, `torznab_url_test.go`.

## [2026-10-17] DHT COLD START (KI-008) — Status: 🟢 deployed
- Root Cause: `announceToDht` ignored `done`, fixed 5-minute wait on an empty DHT table.
- Fix: fork tag `v1.2.32-mansio.1`, pacing moved to `dhtAnnouncer` (15 s → 5 min).
- Guard: pinned `replace` + behavioural test in the fork; live swarm not tested.

## [2026-10-24] ISSUE #925 (KI-009) — Status: 🔴 open, NOT fixed in built tree
- Corrects `.agent_task_state.md`: pinned `v1.2.32-mansio.1` has no `InvalidateStorage`, and
  `Piece.Release` never calls it. Repro still prints `BUG REPRODUCED ... reads as ZEROS`.
- H5 refuted: the wiring test proves only no-panic, not the fix.
- Next: tag `v1.2.32-mansio.2` + call in `Release`; close only when `BUG NOT REPRODUCED`.
- Report: `experiments/issue925/REPORT.md`.

## [2026-10-24] SNAPSHOT REGRESSION — Status: ✅ fixed
- Symptom: `go vet ./...` failed on `web/api/utils/link.go:74`; `server/web/api/utils` did not build for tests.
- Root Cause: snapshot commit `b6894e4d` replaced upstream `fmt.Errorf("unknown scheme %q: %v", ...)` with `fmt.Errorf("unknown scheme:", ...)`.
- Fix: restored the upstream format; `go vet ./web/api/utils` now exits 0.

## Open threads

| Thread | Age | Next |
|---|---|---|
| KI-002 P0: `ReadAt`/`Release` under `p.mu.Lock` | since 2026-08-29 | measure contention; the old `maxRemovePerCycle` plan is stale |
| KI-009 #925 fix | 2026-10-19 | publish fork tag + wire `Release` |
| `-race` unavailable (`CGO_ENABLED=0`, no gcc) | permanent locally | run in CI with cgo |
