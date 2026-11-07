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

## [2026-10-24] REAL HOLES — Status: ✅ code + tests, deploy pending
- Engine `v1.2.34-mansio.2`: DHT servers closed on Client.Close (needs utp v0.2.1) and the reader
  piece fetched from up to 3 peers (`maxStallProneRequests`). Fork branch `fix/dht-close-multipeer`,
  tag `v1.2.34-mansio.2` (`36d9bf0`), pushed.
- Server: #909 state races (GotInfo/Close/expiredTime/Status copy), #938 m3u ETag from body,
  ffprobe 5-minute goroutine leak, DNS cache delete-under-RLock, CloseDB stale global.
- Not taken: drop-chunks-no-reader (kills background/pause prefetch), reader tiers (% regression).
- Tests: build 0, all touched packages green.
- Deployed 2026-10-24 21:06: `sha256 ce047db0…d6ad`, backup
  `/opt/torrserver/TorrServer.bak-20261024-210640-beforeholes`; service active, `/stat` 200.
- Live reconnect (settings save, exercises `Disconnect → Client.Close → DHT close`): 200 in 3.78 s,
  still active, 0 errors/panics.

## [2026-10-24] OPS — x-ui/Xray noise — Status: ✅ fixed
- Host 212.113.101.151: 302 refused + 776 timeouts/day were the VPN client's own failed IPv6
  connections (~0.7% of traffic), not a server fault (IPv6 ping/curl OK).
- Fix: Xray `loglevel: none`; added metrics block on 127.0.0.1:11112 (stops the 2-second DEBUG line).
- Verified: 0 ERROR/0 DEBUG in 60 s, UDP 46630 still carrying traffic, service active.
- Trade-off: Xray core logs off; re-enable with the SQL in `experiments/xray-noise-2026-10-24/REPORT.md`.

## [2026-10-24] TV CHiQ AI PONT (redwood) — Status: ✅ applied, ⚠️ one boot-drift
- Device: `DVB92CQ`/`redwood`, UKNV.260514.001, Android 14, 1 GB, 32-bit; ADB 5555.
- Applied: 12 safe + 6 optional disables, AdGuard Private DNS, animations 0.5, stay-awake.
- Launcher: cleanyourtv 2.7 built from source (512 KB), stock `tvlauncher` off, theme-nature
  (14 wallpapers, dock Lampa/Prisma/OTTPlayer/IPTV Pro/YouTube).
- Verified by screenshots: Home after reboot, YouTube launch, Inputs (HDMI1/2, AV, Live TV),
  Chromecast ports 8008/8009. Swap 469→~230 MB; launcher 95 MB vs stock 146 MB.
- Guard: `com.cltv.fast` returns on every boot → `check.py --fix` after reboot.
- Backup 232 MB + recovery JSONs + restore.ps1 under `experiments/tv-2026-10-24/`.
- Not touched: `homwee.dynamicbacklight` (picture), `mediatek.*`, codecs, `gms`.
- Cold boot opened Live TV (`tvcenter` → Homwee `ProtocolActivity`); fixed with PR #1445
  `power_on_home` (launcher-only patch) + `HomeKeyService`; log now ends on `com.gathin.tvhome`.
  Old APK kept for rollback; injected ADB keys don't disarm the helper (real remote does).
- SmartTube 32.68 (armeabi-v7a) installed, dock now 6 apps. Server cron on 192.168.2.6
  re-disables `cltv.fast` every 5 min (`tv-fix-cltv.sh`); verified.
- GitHub: comments #1321/#1445, PR #1509 reworked to `rca-ai-pont` CHiQ DVB92CQ variant
  (all checks pass; bot leans merge 65%). Factory/OTA inspected read-only only; not launched.
- Research #2: chill-tv-launcher/UAD/SmartTube bridge/NTP; no new packages for this unit;
  load 55 is normal, boot history only `reboot,shell`.
- Guts audit read-only: AVB locked/green, no root; panel C320Y23-M9-FK9GPSS01, board
  MT5867-H1V1-B2-S, tuner RDA5815M; factory `factoryPowerOnModel` + hotel receiver; OTA local
  zip paths; tvservice squashfs root-only. No factory launch/flash.
## [2026-10-25] XIAOMI MOEU1 INVENTORY — Status: ✅ read-only, nothing changed
- Device: `MiTV-MOEU1` / `lalaland`, MT9689 (`merak`, `ro.hardware=mt5896`), 4×A73 1.39 GHz, 2.32 GiB visible RAM,
  32 GB eMMC (25 GB `/data`), 55" CSOT 3840×2160@60 HDR10/HLG/HDR10+/DV, Wi-Fi6 MT7961, Android 14
  `UKN1.240806.017.V816.0.25.6.4.UOEMBTV`. HW decoders AV1/HEVC/VP9/VVC + Dolby Vision.
- ADB: `adb_wifi_enabled` stays 0 (no `_adb-tls-connect` in mDNS) — Xiaomi build has no working Wireless debugging;
  `adb_enabled=1` + firmware `service.adb.tcp.port=5555` is enough. Xiaomi toggle = `Settings.Global.enableXiaomiAdb`.
- Artifacts: `experiments/tv-2026-10-25-xiaomi-moeu1/` (REPORT.md, RESEARCH.md, collect.ps1, backup/ 48 files, 5.0 MB).
- Guard: on platform-tools 37.0.1 `adb exec-out shell <cmd>` fails (`/system/bin/sh: shell: inaccessible`);
  use `adb exec-out "<cmd>"`. Screenshots only via `screencap` to `/data/local/tmp` + `pull`.
- Not verified: marketing name (X Pro 55 2026 hypothesis), Widevine level, CEC (`mIsCecAvailable=false`), standby ADB.

## [2026-10-25] XIAOMI MOEU1 DEBLOAT BATCH 1 — Status: ✅ applied + reboot verified
- Applied 11 (`pm disable-user`): telemetry (`miui.tv.analytics`, `xiaomi.statistic`, `tvqs.overseas.y24`,
  `google.android.feedback`, `tv.feedbackconsent`), promos (`mitv.tvhome.atv`, `.michannel`, `xm.webcontent`,
  2× autoinstalls, `partnersetup`). Kept by owner: Live TV/CI+, OTA chain, `com.mitv.dream`.
- Recovery: `~/.cleanyourtv/recovery-192.168.2.42_5555-d5c875c4.json` (+ copy in backup/recovery), restore: `restore.ps1`.
- Verified after reboot: 17 disabled persist, `logcat -b crash` empty, HOME = tvlauncher, DVB+ATV+3×HDMI alive.
- Numbers: MemAvailable 891 MB → 856–1038 MB, swap 334 → 404 MB used, load 55 → 19. No confirmed RAM win yet
  (single post-boot sample); needs ≥10 min idle × 2–3 runs.
- Guard (script bug fixed): `$home` is a read-only PS variable — use `$homeAct`; `TV_INPUT_HARDWARE` is granted
  to telemetry packages on Xiaomi, so the guard now skips packages in `dumpsys tv_input` packageSet instead.
- Finding: after reboot the stock launcher shows a `VK Видео: Рекомендации` row → app channels work; Prisma has
  `TvContract`/`PreviewProgram` strings but no row yet.
- Not ours: `play.games`, `youtube.tvmusic`, `mitvplus` went disabled at 10:22 with Settings' AppManagementActivity
  open — owner action via UI; recorded in the recovery JSON as `enabled`.

## [2026-10-25] TVPROBE: TvProvider на Xiaomi MOEU1 — Status: ✅ measured
- Built `experiments/tv-2026-10-25-xiaomi-moeu1/tvprobe` (10 KB, no deps), grant READ_TV_LISTINGS,
  dump `backup/tvprobe.json` (1.2 MB): 20 channels / 688 preview / 45 watch-next.
- Prisma publishes 5 channels + 103 previews with Russian titles, posters and `intent:` URIs → the
  stock Google TV launcher renders only VK of them, so a custom launcher can add the rows.
- Trap: `Manifest.permission.READ_TV_LISTINGS` and `TvContract.*.PACKAGE_NAME` did not compile against
  this platform-34 jar; string literals work. Shell `content query` cannot see rows (no permission).
- PII: Lampa poster URLs carry the owner e-mail (`nl.imagetmdb.com/...?email=`). Mask in any report.

## [2026-11-06] UPSTREAM CATCH-UP (MatriX.148–150) — Status: ✅ critical ported
- #957 MPTCP off (`//go:debug multipathtcp=0`) — CVE-2026-98432 RCE mitigation.
- #941 malformed hash no longer panics; #942 `set` without `sets` → 400.
- #955 RAM eviction counts the allocated buffer; #952 Torznab host without scheme.
- KI-011 flake fixed with `Storage.WaitInitialScan()` (20/20).
- Deferred: #933, #954 (sid), #947, WAF #943/#944/#946, web/gstreamer UI fixes, #924 (no /subs).

## [2026-11-06] DEPLOY MatriX.148-150 fixes — Status: ✅ deployed
- Binary `sha256 6855dd53…da07` over `ce047db0…`; backup `/opt/torrserver/TorrServer.bak-20261106-233106-before148`.
- Live: `/stat` 200, malformed hash 404, set-without-sets 400, `multipathtcp` present, 0 errors/panics.

## [2026-11-07] UPSTREAM AUDIT 3 WEEKS — Status: ✅ #949/#933/#947 ported, deployed
- #954/#958 skipped: gstreamer is behind the gst build tag and this binary has no gst.
- WAF (#943/#944/#946), /subs (#924) and web UI are not in our build.
- #949: reader uses ReadContext with req.Context(), so a disconnected client cancels a read
  waiting for a piece; compiled and deployed, no dedicated cancellation test.
- Servers: prod 192.168.2.6 and 212.113.101.151 both on 1e68436c…f8e2, active, no errors.
