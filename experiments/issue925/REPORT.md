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
| H1 | `Piece.InvalidateStorage()` clears dirty chunks | ✅ CONFIRMED | fork tag `v1.2.34-mansio.1`; `TestInvalidateStorageClearsDirtyChunks` PASS |
| H2 | Without the fix the storage repro still shows zero-read | ✅ CONFIRMED (negative control) | `BUG REPRODUCED ... reads as ZEROS` |
| H3 | `iterUndirtiedChunks` returns all chunks | ✅ CONFIRMED | same fork test: every chunk pending again |
| H4 | Storage-level tests do not change behaviour after the fix | ✅ CONFIRMED | repro still reports the bug; fix belongs to the library layer |
| H5 | Server `Piece.Release` calls the new method | ✅ CONFIRMED | `piece.go:Release` calls `InvalidateStorage()` before `SetPriority(None)`; wiring test PASS |

## Conclusion

**#925 is fixed in code on 2026-10-24 20:20.** The fork tag `v1.2.34-mansio.1` adds
`InvalidateStorage`, and the server `Release` calls it. The storage-level repro still prints
`BUG REPRODUCED` by design — it exercises the storage layer only, not the library fix.

Not verified: elimination of zero-read on a live torrent/swarm (needs a real playback session).
The deployed binary (`550ea84b…`, 2026-10-17) predates this fix.

## Next

1. Build and deploy a binary from this tree; re-run the live check.
2. Close YouROK/TorrServer#925 with the tag + the two-commit evidence.
