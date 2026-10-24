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
