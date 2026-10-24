# Frozen hypotheses — upstream sync (2026-10-24)

Inventory taken at 19:40 MSK against `upstream/master = 4bfe69e8`. Freeze written
before porting the remaining fixes; H1 was already observed during inventory and is a
control, not evidence for the other rows. Falsifiers are mandatory.

| # | Hypothesis | Falsifier | Reason |
|---|---|---|---|
| H1 | The `%` query fix (#928) is absent in our tree | `unescapeQuery` or equivalent already present | control: `stream.go` used `url.QueryUnescape` with a dropped error |
| H2 (expected FAIL) | #914 (`all.m3u` numbering) applies to our fork | `statusFromSpec` does not exist | our `all.m3u` builds nested per-torrent playlists, not a flat file list |
| H3 (expected uncertain) | Upstream torrent v1.2.34 is a strict superset of our `v1.2.32-mansio.1` DHT fix | v1.2.34 lacks `nextDhtAnnounceInterval` or different pacing | our fork adds the fix on 1.2.32; upstream bumped on 18.10 |
| H4 (expected FAIL) | #911 external subtitles can be applied without touching our cache/stream edits | it rewrites `server/torr/stream.go` or `readfile.go` in the same regions | #911 adds `/subs`, `server/subs`, gstreamer changes |
| H5 | DNS fix #892 applies to our `cmd/main.go` | our DNS block diverged too far to patch | our `main.go` still has the same `NewDNSChecker` block |

## Controls

- H1 must be true before the port; after the port `TestUnescapeQuery` must pass.
- H2 must be false (no `statusFromSpec`) or the port is required.
- The sync must not change runtime behaviour not covered by a test: every ported fix
  names its test or its explicit "not verified" reason in the report.
