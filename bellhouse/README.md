# bellhouse (prototype)

Household push notifications for the swee.net ecosystem, with no native app
and no Apple Developer Program fee. Doorbell, power cuts, appliance cycles —
per user, per device, with quiet hours.

> **Staging location.** This lives in a statehouse branch only because the
> session that wrote it could not create repositories. It is its own Go module
> (`github.com/sweeney/bellhouse`) and is meant to move to `sweeney/bellhouse`
> with `git mv`/`git filter-repo` — statehouse's `go build ./...` and
> `go test ./...` do not see it.

## Why this works on iPhone

Since iOS 16.4, a site added to the Home Screen can receive standard Web Push
(VAPID, self-generated keys — no Apple account, no certificates). Since iOS
18.4, **Declarative Web Push** lets Safari display a notification straight from
a JSON payload with no service worker involved, which removes the main cause of
early iOS web push flakiness. Every message bellhouse sends is declarative:

```json
{"web_push":8030,"notification":{"title":"Doorbell","body":"Front door is ringing","navigate":"https://bells.swee.net/","tag":"doorbell","lang":"en-GB"}}
```

Browsers without declarative support (Chrome, Firefox, Android, iOS < 18.4)
hand the same payload to `web/sw.js`, which renders it. One payload, both paths.

## Shape

```
statehouse ── house/events/derived ─┐
ups-mqtt ──── ups/+/outage ─────────┼─► bellhouse ─► rules ─► dedupe ─► per-user prefs ─► Web Push ─► Apple / Google / Mozilla
ups-mqtt ──── ups/+/last_outage ────┘        ▲
binday etc ── POST /api/notify (service token)┘
PWA ───────── /api/me/* (user token, PKCE via id.swee.net)
```

| File | Role |
|---|---|
| `rules.go` | MQTT message → candidate notifications. Pure, table-tested. |
| `store.go` | Users, prefs, subscriptions, dedupe keys. One JSON file, atomic writes. |
| `push.go` | Fan-out, declarative payload, Urgency/TTL/Topic headers, prunes 404/410. |
| `http.go` | API + embedded PWA. User tokens for `/api/me/*`, service tokens for `/api/notify`. |
| `web/` | PWA: manifest, service worker, settings page. |

## Traps in the source data (handled)

- statehouse emits `cycle_finished` for `binary_state_device` too (boiler
  relays). Only `cycle_power_device` counts as an appliance.
- `intercom_ringing` fires for every call, including internal extension-to-extension
  calls. Only `rules.door_extensions` count as the doorbell.
- ups-mqtt re-publishes `outage` every poll and retains it; `last_outage` is
  retained forever. Notifications dedupe on `outage_started_at`, persisted, and a
  `last_outage` is only news if `ended_at` is within `restored_window` (30 min).

## Push tuning

| Kind | Urgency | TTL | Topic (collapses at push service) |
|---|---|---|---|
| doorbell | high | 60 s — a late doorbell is noise | `doorbell` |
| ups on battery / low | high | 1 h / 30 min | `ups-<label>` |
| ups restored | normal | 6 h | `ups-<label>` |
| appliance finished | normal | 6 h | `cyc-<device>` |

## Known limits

- Assume no Time-Sensitive / Critical interruption level for web push: a Focus
  mode silences Bells unless the user allows it in that Focus.
- iOS has been seen to revoke subscriptions silently and never fires
  `pushsubscriptionchange` ([WebKit 273063](https://bugs.webkit.org/show_bug.cgi?id=273063)).
  The app re-posts its subscription on every open/resume; the device list shows
  "last delivered" and "opened" so a dead device is visible.
- `data_dir/vapid.json` must be backed up. Losing it orphans every subscription.
- Notifications need HTTPS (Cloudflare Tunnel, as id/config already use).

## Run locally

```
go test ./...
go run . -config bellhouse.example.yaml   # auth off, MQTT off, http://localhost:8787
```

Chrome treats `http://localhost` as secure; Safari does not, so test on iPhone
via the real HTTPS hostname.

## Going live

1. Register a public PKCE client `bellhouse` in identity, redirect `https://bells.swee.net/`.
2. Route `bells.swee.net → :8787` through the Cloudflare Tunnel on garibaldi.
3. Fill in the commented production block in `bellhouse.example.yaml`
   (`door_extensions` especially).
4. On each iPhone: open the site in Safari → Share → Add to Home Screen → open
   **Bells** from the Home Screen → Enable → Send test.

## Status

Tests cover rules against real payload shapes, prefs/quiet hours, dedupe across
restart, and an end-to-end send (real RFC 8291 encryption to a fake push
service, header checks, 410 pruning). The PWA was driven in Chromium against
the running server for prefs, subscribe, device list and test send. **Not yet
verified: delivery to a real iPhone** — the build sandbox could not reach
`web.push.apple.com`.

Alternatives considered: Pushover ($4.99 one-off, has emergency priority — a
good belt-and-braces for UPS low-battery only), ntfy (iOS relays via ntfy.sh),
Bark (iOS-only, no shared login).
