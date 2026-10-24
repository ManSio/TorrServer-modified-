# Red Team — ported upstream fixes (2026-10-24)

Mode: attack own diff before commit. At least three attacks; unprotected findings
require a plan change, not a note.

| # | Attack | Result | Protection |
|---|---|---|---|
| A1 | Send `title=%2525` / `50%25` to `/stream`: second decode turns `%25` into `%`, so a title that literally contains `%25` is changed | **holds** (by design, upstream parity) | `TestUnescapeQuery` covers literal `%`; the double-decode contract is documented in the test names. Accepted risk: encoded-plus-literal-percent is still decoded twice |
| A2 | Fire two concurrent `POST /settings {"action":"set"}` with opposite `EnableDLNA`: interleaved `Stop/Start` can leave DLNA stopped while the final settings enable it | **mitigated** | `restartServicesMu` serializes the Stop/Start sequence (`server/web/api/settings.go`) |
| A3 | Call `drop()` while `WaitInfo` is between the snapshot and `torrent.GotInfo()`: the pointer stays valid, but `t.TorrentSpec` is still read/written without `bt.mu` | **holds, low** | snapshot of `t.Torrent` under `muTorrent` is the upstream fix; `TorrentSpec` is only written in `NewTorrent` under `bt.mu`, which is not held concurrently with `WaitInfo` in the normal path. Not proven by `-race` (no cgo) |
| A4 | DNS `pickServer` with all servers black-holed: goroutines leak | **mitigated** | context deadline (`Timeout`, 5 s), buffered channel sized to the server count; each goroutine exits on ctx |
| A5 | `action:"def"` in read-only mode reads the request instead of the effective settings | **mitigated** | `restartServices` reads `sets.BTsets`, i.e. what `SetSettings` actually applied |

## Unprotected / accepted

- A1 (double decode) and A3 (`TorrentSpec` without lock) are upstream-identical and not
  fixed here; recorded in the report as accepted, not as "safe".
- `-race` unavailable locally (`CGO_ENABLED=0`, no gcc), so A2/A3 thread safety is proven
  by reasoning + deterministic tests only.

## Stage 2 attacks

| # | Attack | Result | Protection |
|---|---|---|---|
| A6 | #678 "target wins" means a stale live DB never receives corrections from the source file | **holds by design** | target is the live DB; the bug was the opposite direction. Tested both directions |
| A7 | Engine swap v1.2.32>v1.2.34 changes DHT/tracker behaviour | **mitigated** | our server tests pass; upstream ships v1.2.34 for #922; `-race` not run |
| A8 | Browser sends a stale If-None-Match after upgrade | **mitigated** | ETag is derived from the embedded Indexhtml at startup, so it changes with the binary |
| A9 | Docker entrypoint typos silently drop the Telegram token | **mitigated** | flags now match the CLI names (`--tgtoken`, `--fusepath`) used in README; no automated test |

## Round 3 attacks (real holes)

| # | Attack | Result | Protection |
|---|---|---|---|
| A10 | Multi-peer duplicates: 3 copies of a chunk waste bandwidth, and on a slow swarm they may starve the rest of the zone | **bounded** | only the piece under a reader (`readerNowPieces`), hard cap 3, duplicate timer stays for all other pieces |
| A11 | `Client.Close` now closes DHT servers: an announcer may still hold a server reference | **holds, untested live** | close happens under `cl.lock`, announcers exit on `closed`; the post-deploy settings-save check exercises it |
| A12 | uTP replaced by `lampac-nextgen/utp v0.2.1` (third-party fork) | **mitigated** | module replace is one line and reversible; build + full touched-package tests pass; no independent audit |
| A13 | ffprobe `WithTimeout(5m)` cancels a probe that legitimately needs longer | **accepted** | same 5-minute bound as before, now released on early return |
| A14 | m3u ETag hashes the whole body on every request | **accepted** | lists are small; correctness beats the old constant tag |
| A15 | DNS cache now takes a write lock for reads | **accepted** | lookup is cold-path; map is tiny |
