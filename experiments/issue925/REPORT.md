# REPORT — issue #925: zero-read after eviction of a partial piece

- **Frozen hypotheses:** `frozen/HYPOTHESES.md` (2026-10-19, frozen before runs)
- **Upstream:** https://github.com/YouROK/TorrServer/issues/925
- **Re-run:** 2026-10-24, Go 1.27.0 windows/amd64, `CGO_ENABLED=0`

## Command

```
cd server
go test ./torr/storage/torrstor -run 'Issue925' -v
```

## Raw output

```
=== RUN   TestIssue925ReleaseWiringWithRealTorrent
--- PASS: TestIssue925ReleaseWiringWithRealTorrent (0.15s)
=== RUN   TestIssue925_ResponsiveEvictionZeroRead
    issue925_repro_test.go:64: positive control OK: pre-eviction read returns original bytes
    issue925_repro_test.go:78: eviction done: buffer=nil, Size=0, Complete=false
    issue925_repro_test.go:88: post-release read: n=0 err=EOF (responsive reader would retry)
    issue925_repro_test.go:118: re-read first 32KB: n=32768 zeros=true original=false
    issue925_repro_test.go:124: BUG REPRODUCED: evicted region reads as ZEROS — this is what the demuxer sees (#925)
    issue925_repro_test.go:134: negative control (never-written region): n=1024 zeros=true err=<nil>
    issue925_repro_test.go:142: CHARACTERIZED: #925 present — eviction of partial MemPiece + partial re-download yields zero-read for previously-received region
=== RUN   TestIssue925_EvictsPartialWhenOnlyCandidates
    issue925_repro_test.go:185: evicted 1/12 partial pieces when no complete candidates existed
    issue925_repro_test.go:189: CHARACTERIZED: #925 present — evicted 1 partial pieces, each losing received bytes the client still counts
PASS
ok  	server/torr/storage/torrstor	2.947s
```

## Verdicts

| # | Hypothesis | Verdict | Evidence |
|---|---|---|---|
| H1 | `Piece.InvalidateStorage()` clears dirty chunks | ⏳ NOT IN BUILT TREE | method exists only in `../_forks/torrent/piece.go:250`; pinned module `v1.2.32-mansio.1` has **no** `InvalidateStorage` |
| H2 | Without the fix the storage repro still shows zero-read | ✅ CONFIRMED (negative control) | `BUG REPRODUCED ... reads as ZEROS` |
| H3 | `iterUndirtiedChunks` returns all chunks | ⏳ NOT IN BUILT TREE | same as H1 (fork only) |
| H4 | Storage-level tests do not change behaviour after the fix | ✅ CONFIRMED | repro still reports the bug; fix belongs to the library layer |
| H5 | Server `Piece.Release` calls the new method | ❌ REFUTED | `torr/storage/torrstor/piece.go:114-126` calls `SetPriority(None)` + `UpdateCompletion()`, **no `InvalidateStorage`**; the wiring test only proves no panic/deadlock |

## Conclusion

**#925 is NOT fixed in the built tree.** The repro is valid and still reproduces zero-read.
The fix was prototyped in the external fork but was never tagged, pinned, or called from the server.
`TestIssue925ReleaseWiringWithRealTorrent` is a smoke test of the call site, not proof of the fix.

## Next

1. Tag the fork `v1.2.32-mansio.2`, bump the `replace` in `server/go.mod`.
2. Insert `piece.InvalidateStorage()` in `Piece.Release` before `SetPriority(None)`.
3. Re-run this report; a valid outcome is `BUG NOT REPRODUCED`.
