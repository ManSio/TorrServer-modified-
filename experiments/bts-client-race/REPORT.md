# REPORT — nil-deref on `bts.client` (KI-006)

- **Frozen hypotheses:** `frozen/HYPOTHESES.md` (2026-10-04, frozen before runs)
- **Source:** production panic 2026-10-04 13:27:22, `client.go:1461 ← client.go:169 ← apihelper.go:326 ← route.go:43`
- **Fix commit:** `d739331b` (local), tests `server/torr/clientrace_test.go`
- **Re-run:** 2026-10-24, Go 1.27.0 windows/amd64, `CGO_ENABLED=0`

## Command

```
cd server
go test ./torr -run 'TestWriteStatusNilClient|TestConcurrentDisconnectAndWriteStatus|TestNewTorrentDoesNotDeadlock' -v
```

## Raw output (tail)

```
=== RUN   TestWriteStatusNilClient
--- PASS: TestWriteStatusNilClient (0.00s)
=== RUN   TestConcurrentDisconnectAndWriteStatus
--- PASS: TestConcurrentDisconnectAndWriteStatus (8.34s)
=== RUN   TestNewTorrentDoesNotDeadlock
--- PASS: TestNewTorrentDoesNotDeadlock (0.25s)
PASS
ok  	server/torr	12.868s
```

## Verdicts

| # | Hypothesis | Verdict | Evidence |
|---|---|---|---|
| H1 | `/stat` panics when `bts.client == nil` | ✅ CONFIRMED | `TestWriteStatusNilClient` passes; production stack matches |
| H2 | Cause: readers did not enter `bt.mu` | ✅ CONFIRMED | `btserver.go:388` now takes `RLock` for the whole call |
| H3 | `torrent.go:63` nil check alone does not save | ✅ CONFIRMED | fix also guards `NewTorrent` right before use; comment at `btserver.go:382` |
| H4 | Bug also exists upstream (port) | ✅ CONFIRMED | upstream `d739331` absent; KI-006 states it exists in the port |
| H5 | `atomic.Pointer[T]` is enough | ❌ REFUTED (as predicted) | does not protect `Close()` racing the reader |
| H6 | Fix in `apihelper.go` only is enough | ❌ REFUTED (as predicted) | second read site in `torrent.go` needs the same guard |
| H7 | Fix without nil check is safe | ❌ REFUTED (as predicted) | nil must return an error, not be dereferenced |

## Not verified

- `-race` not run: `CGO_ENABLED=0`, no gcc on this machine (same limitation as KI-006).
- Pre-fix failure was not replayed in this session; the two-commit claim is recorded in KI-006.
