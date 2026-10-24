# REPORT — x-ui/Xray log noise (2026-10-24)

- **Host:** `212.113.101.151` (`imperial-salmon.aeza.network`), root
- **Stack:** 3x-ui 3.10.2, Xray 26.10.10, kernel WireGuard `wg0` (peer 37.215.39.68)
- **Symptom:** `ERROR - XRAY: proxy/wireguard: connection was refused` (302/24 h) and
  `... operation timed out` (776/24 h), plus `DEBUG - xray metrics unavailable` every 2 s.

## Hypotheses (recorded during the investigation, before the fixes)

| # | Hypothesis | Verdict | Evidence |
|---|---|---|---|
| H1 | Server-side network/firewall fault | ❌ REFUTED | `ping6` to Cloudflare/Yandex 0% loss; `curl -6` 200/302; only `wg-quick@wg0` active |
| H2 | The WireGuard inbound is unused | ❌ REFUTED | UDP 46630 active with 37.215.39.68; ~150 connections/90 s in the access log |
| H3 | The noise is per-connection failures of the VPN client | ✅ CONFIRMED | access log: client `2a12:5940:4130:1::2` → public IPv6 (Yandex, Cloudflare); failure rate ≈ 0.7% |
| H4 | `loglevel: none` silences it without breaking the tunnel | ✅ CONFIRMED | 0 `ERROR - XRAY` lines in 60 s after restart; UDP 46630 still flowing |
| H5 | The 2-second DEBUG line is a separate x-ui metrics poller | ✅ CONFIRMED | message says "metrics block not configured"; adding a metrics block stopped it (0 in 60 s) |

## Changes

- Backup: `/etc/x-ui/x-ui.db.bak-20261024-182104`
- `xrayTemplateConfig.log`: `loglevel` `warning` → `none` (access already `none`).
- Added `"metrics": {"listen": "127.0.0.1:11112"}` to the template; Xray validates it.
- Removed the temporary `/var/log/x-ui/access.log` used for the experiment.

## Verification

```
systemctl is-active x-ui            -> active, 0 restarts (since 18:26:51 UTC)
new_xray_errors (60 s)              -> 0
new_debug_metrics (60 s)            -> 0
tcpdump udp port 46630              -> packets still flowing both ways
ss -ltnp | grep 11112               -> xray listening on 127.0.0.1:11112
```

## Trade-off / Red Team

- Xray core logs are now off. Real Xray failures are not in `3xui.log`; health must be watched via
  the panel, `systemctl`, and WireGuard handshakes. Re-enable with one SQL update if debugging.
- The metrics listener is bound to loopback only.
- The root cause is the client's ordinary failed IPv6/internet connections (~0.7%). If the owner wants
  fewer failures at the source, disable IPv6 on the client at 37.215.39.68; this was not changed.

## Revert / re-enable logs

```
sqlite3 /etc/x-ui/x-ui.db "update settings set value=json_set(json_remove(value,'$.metrics'),'$.log.loglevel','warning') where key='xrayTemplateConfig';"
systemctl restart x-ui
```
Full revert: `cp /etc/x-ui/x-ui.db.bak-20261024-182104 /etc/x-ui/x-ui.db && systemctl restart x-ui`.
