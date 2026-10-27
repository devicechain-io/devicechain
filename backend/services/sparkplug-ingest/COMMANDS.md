# Sparkplug command downlink - implementation notes

Maintainer notes for the command path in `host/` (birthtable.go, commandenc.go). User-facing
documentation lives with the docs site.

- A command is addressed and encoded against the BIRTH metric table: alias only when the BIRTH
  aliased the metric, name otherwise (and always for Rebirth); the value is encoded exactly into
  the BIRTH-declared datatype or refused before the wire.
- A lookup answers `ErrNotBorn` when there is no usable table yet (node or device not born, the
  device not re-declared since a rebirth, or the Host session ended) - park and wait - and
  `ErrMetricUnknown` only when the table exists and does not declare the metric - refuse.
- **The revalidate-to-publish window is irreducible.** `RevalidateMetric` is checked under the
  tracker lock, the publish is not (an MQTT publish is never made while holding it), so a rebirth
  can land in between and the command goes out on an alias the node has just remapped. The
  confirmation stage settles this honestly: a write the node did not act on ends UNANSWERED.

## The dispatcher (`downlink/`)

The leader-only consumer of `device-commands`. It exists only when some source sets
`commands.enabled`; with nothing enabled the service builds none of it (no durable, no bucket, no
reader) and behaves exactly as before. It is started BEFORE the sources connect and stopped before
they are disconnected, because it publishes over their sessions.

Gate order (the first gate that matches decides; the numbers are the ones in the code comments):

1. parse the subject's tenant and the envelope (poison: ack, count)
2. tenant deleted: ack, count
3. `Dc-Transport` is not `sparkplug`: ack-drop, count `not_served`. An ABSENT header is unknown, not
   "not mine": it is served only when one of the tenant's sources already holds the device
4. no source of the tenant enables commands: UNSENT. Never ack-dropped (the row would sit in SENT)
5. locate: on an enabled source continue; only on a disabled source UNSENT; on none PARK, under the
   envelope's dispatch nonce, because there is no BIRTH table yet
6. breaker open: UNSENT `NOT_DELIVERED`; 7. expired: UNSENT
8. validate against the BIRTH table (`ErrNotBorn` parks, anything else refuses with a reason)
9. rate limit, charged BEFORE the claim; the waiting message is marked in progress
10. claim (`confirmCommandDispatch` live, `markCommandSent` on a drain); a lost claim is acked and
    sent nowhere
11. supersede an awaiting write to the same (device, metric)
12-14. record in the awaiting bucket, then publish, inside `Client.PublishMetric` / `PublishRebirth`:
    the record is written after the publish's last re-check and immediately before the bytes go
    out, so it describes exactly what was sent (alias and digest included), and a failed write
    means nothing is published
15. ack

A drained row (woken by a birth) runs the same gates on a row it has already claimed, so its
refusals quote the claim's nonce and a table that is not there yet hands the row back again.

What is NOT here yet (the confirmation slice): matching a report against the awaiting entries,
the window expiry, UNCHANGED, re-arming entries at the start of a term. Until it lands a published
command stays SENT and ends TIMEOUT, so `commands.enabled` must not be switched on in a release
that does not carry it. This slice resolves a command itself only when it knows the outcome
without a report: refused before the wire, superseded, or the broker dropped the session right
after the publish.

Things worth knowing:

- **The reverse index** (`host/commands.go`) is filled from the resolve each accepted birth already
  makes (the registrar's cache answers it) and emptied at death, at an NDEATH cascade and at the
  end of the Host session. A session that is down therefore holds nothing: it reads as not born.
- **The breaker** (`host/breaker.go`) is per Client and in memory. A session lost within five
  seconds of a command publish opens it (5 minutes, doubling to an hour); half-open admits one.
- **`Dc-Command-Expires`** is a routing-side header command-delivery stamps for Sparkplug commands
  so the rate wait can look at the expiry again. Absent means unknown; the claim is still the
  authority on expiry.
- **A reboot that had to be parked is refused when drained**: the impact header exists only on the
  live message, and an unknown impact fails closed. Re-issue it.
- **The one-reboot-per-window rule is held in memory**, per term, so a change of leader forgets it.
- **Stranded-SENT**: Sparkplug stays off the allow list, deliberately. Re-arming a QoS 0 DCMD would
  actuate it a second time.
