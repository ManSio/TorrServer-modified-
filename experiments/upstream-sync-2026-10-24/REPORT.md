# REPORT — upstream sync 2026-10-24

- **Upstream:** `YouROK/TorrServer`, `master = 4bfe69e8` (2026-10-23)
- **Our base:** `bc427f28` + working tree; HEAD before this sync `bb69fe90`
- **Frozen hypotheses:** `frozen/HYPOTHESES.md`
- **Red Team:** `RED_TEAM.md`
- **Toolchain:** Go 1.27.0 windows/amd64, `CGO_ENABLED=0`

## Inventory (upstream commits after our snapshot)

| Upstream | Subject | Decision |
|---|---|---|
| `e806313a` | fix WaitInfo panics (#929) | **ported** |
| `6ccaebbf` | keep query values with literal `%` (#928) | **ported** |
| `4bfe69e8` | restart services after settings reset (#934) | **ported** (+ mutex) |
| `6fba8284` | fix DNS checker timeouts (#892) | **ported** |
| `7a101b8f` | number merged all.m3u like /stream (#914) | **N/A**: our `all.m3u` has no `statusFromSpec` (nested playlists) |
| `972e39f0` | preload log spacing | N/A (cosmetic) |
| `4e163a78` | migrate.go skips any existing key (#678) | deferred: changes overwrite semantics, needs owner decision |
| `5e9ed1f2` | Windows-1251 subtitle bytes (#923) | blocked on #911 |
| `d592c561` | external subtitles / `/subs` (#911) | **staged**, large feature |
| `bd07eb40` | bump torrent v1.2.34 | **OPEN**: our fork is `v1.2.32-mansio.1` with a custom DHT fix |
| `7c142d33` | follow-redirects 1.16.1 | not ported (web dependency) |
| `d84f4196` | update static web | not ported (artifact) |
| `#885/#899` | SSL lifecycle / HTTPS UI | not ported (feature, not a fix) |

## Verification

```
cd server
go build ./...                                  -> exit 0
go vet ./cmd/... ./web/api/... ./torr/...       -> exit 0
go test ./cmd/... ./web/api/... ./torr/... \
        ./utils/... ./settings/... ./torznab/... \
        ./torr/storage/torrstor/...             -> all ok
```

## Verdicts

| Hypothesis | Verdict |
|---|---|
| H1 (#928 absent) | ✅ CONFIRMED — `url.QueryUnescape` with dropped error replaced by `unescapeQuery` |
| H2 (#914 applies) | ❌ REFUTED (as frozen): no `statusFromSpec` |
| H3 (v1.2.34 superset) | ⏳ NOT TESTED — engine untouched |
| H4 (#911 applies cleanly) | ⏳ NOT TESTED — staged, no code written |
| H5 (DNS applies) | ✅ CONFIRMED — patched `cmd/main.go`, build/vet green |

## Not verified

- `-race` unavailable (`CGO_ENABLED=0`, no gcc).
- #929 fix is compile- and unit-tested, but the production drop-vs-WaitInfo race was not
  replayed under load.
- DNSSEC/hijack behaviour not tested on a hostile resolver; only the timeout path changed.

## Stage 2 (2026-10-24, after owner rejected #911)

| Item | Decision | Evidence |
|---|---|---|
| engine `bd07eb40` v1.2.34 | **adopted** | module grep: `nextDhtAnnounceInterval`/`dhtAnnouncer`/`announceToDht` present with the same 15 s retry logic as our fork; `InvalidateStorage` absent in both |
| `4e163a78` #678 | **adopted** | issue text: old `viewed.json` overwrote newer bbolt on every start; `TestMigrateAllKeepsTarget` + `TestMigrateAllFillsMissing` |
| `76cd0c17` #910 | **adopted** | `docker-entrypoint.sh`: `--tg`>`--tgtoken`, `--fuse`>`--fusepath` |
| `4aa1061f` #908 | **partial** | `/` > `no-cache`, quoted ETag, 304; `TestIndexIsRevalidated`. Full static/ETag regeneration deferred |
| `d592c561` #911 | **rejected by owner** | `/subs` not wanted |

Verification: `go build ./...` = 0; `go test ./torr/... ./torr/storage/torrstor/... ./settings/... ./web/pages/template/...` all ok.
Engine swap did not change the server tests; `-race` still unavailable.

Not verified: #908 with a real browser/proxy cache; v1.2.34 vs v1.2.32 behaviour beyond our test suite.
