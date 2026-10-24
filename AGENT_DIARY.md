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

## [2026-10-24] PROD VERIFIED (read-only) — Status: ✅ recorded
- `192.168.2.6`: binary sha `550ea84b…`, `vcs.revision=bc427f28`, `vcs.modified=true`, go1.27.0,
  active since 2026-10-17 14:07, 0 restarts, `/stat` 200, 0 errors in 48 h.
- `Cache 103.9 GiB of 146.7 GiB`, disk `104G`, `#Torrents: 0`.
- Symbols present: `totalOnDisk`, `orphanBytes`, `readAheadBytes`, `AutoTune`, `dhtAnnouncer`, `v1.2.32`.
- Symbol **absent**: `InvalidateStorage` → #925 is not in the deployed binary.

## [2026-10-24] UPSTREAM SYNC — Status: ✅ 4 fixes ported, 1 staged
- Ported: `e806313a` WaitInfo (#929), `6ccaebbf` literal `%` (#928), `4bfe69e8` settings restart (#934),
  `6fba8284` DNS timeouts (#892). Report: `experiments/upstream-sync-2026-10-24/REPORT.md`.
- N/A: #914 (our `all.m3u` has no `statusFromSpec`). #911 `/subs` rejected by the owner.
  Engine v1.2.34 adopted in the stage-2 entry below.
- Guard: `TestUnescapeQuery`; `restartServicesMu`; build/vet/tests green.

## Open threads

| Thread | Age | Next |
|---|---|---|
| KI-002 P0: `ReadAt`/`Release` under `p.mu.Lock` | since 2026-08-29 | measure contention; the old `maxRemovePerCycle` plan is stale |
| KI-009 #925 fix | 2026-10-19 | ✅ code fixed (tag `v1.2.34-mansio.1`); deploy + live check |
| `-race` unavailable (`CGO_ENABLED=0`, no gcc) | permanent locally | run in CI with cgo |

## [2026-10-24] UPSTREAM SYNC — STAGE 2 — Status: ✅ engine + #678 + #910 + partial #908
- Engine: `tsynik/torrent v1.2.34` replaces `ManSio v1.2.32-mansio.1`; v1.2.34 already contains the
  DHT fix we patched, so the fork is redundant. #925 still needs a fork tag on the v1.2.34 base.
- #678: `MigrateAll` now keeps the existing target (old JSON no longer overwrites newer bbolt).
- #910: docker flags `--tgtoken`/`--fusepath`; #908: `/` is `no-cache` + ETag/304 (partial).
- #911 `/subs` rejected by the owner.
- Guard: `TestMigrateAllKeepsTarget`, `TestMigrateAllFillsMissing`, `TestIndexIsRevalidated`.

## [2026-10-24] ISSUE #925 CLOSED IN CODE — Status: ✅ fixed, not deployed
- Fork: `ManSio/torrent` branch `fix/invalidate-storage-v1.2.34`, tag `v1.2.34-mansio.1` (`9bb0213`).
- Base is `tsynik/torrent v1.2.34` = merge of our PR #11, so the fork carries only `InvalidateStorage`.
- Server: `Piece.Release` calls `InvalidateStorage()` before `SetPriority(None)`.
- Evidence: fork `TestInvalidateStorageClearsDirtyChunks` PASS; `go build ./...` = 0;
  `go test ./torr/storage/torrstor -run Issue925` PASS.
- Not verified: live swarm; deployment. Prod still runs the 17.10 binary.

## [2026-10-24] DEPLOY #925 — Status: ✅ deployed, ⚠️ live behaviour not proven
- Binary `sha256 8a8b8829…0111c`, `vcs.revision=db2309b9`, built linux/amd64 from the current tree.
- Replaced `550ea84b…`; backup `/opt/torrserver/TorrServer.bak-20261024-203242-before925`.
- Post-checks: service active (20:32:48), 0 restarts, `/stat` 200, 0 errors, 0 panics,
  `InvalidateStorage` symbol count 2, cache 103.9 GiB / 146.7 GiB unchanged.
- Not proven: zero-read elimination on a live swarm — needs controlled eviction (separate instance or
  ~40 GiB above the limit). Recorded as open verification, not as issue closure.
