# Sparkplug command downlink - implementation notes

Maintainer notes for the command path in `host/` (birthtable.go, commandenc.go). User-facing
documentation lives with the docs site.

- A command is addressed and encoded against the BIRTH metric table: alias only when the BIRTH
  aliased the metric, name otherwise (and always for Rebirth); the value is encoded exactly into
  the BIRTH-declared datatype or refused before the wire.
- A lookup answers `ErrNotBorn` when there is no usable table yet (node or device not born, the
  device not re-declared since a rebirth, or the Host session ended) - park and wait - and
  `ErrMetricUnknown` only when the table exists and does not declare the metric - refuse.
- **The last look before the wire is narrow, not zero.** The publish re-checks the table's
  (epoch, generation) under the tracker lock (`SessionTracker.Current`) AFTER the awaiting record is
  written and right before the broker write, and a table that moved is retracted and asked again
  (`ErrTableMoved`). An MQTT publish is never made while holding the lock, so a rebirth can still
  land in the instant between that look and the write, and a node may rebirth while the bytes are
  in flight; the command then goes out on an alias the node has just remapped. The confirmation
  stage settles this honestly: a write the node did not act on ends UNANSWERED.

## The dispatcher (`downlink/`)

The leader-only consumer of `device-commands`. It exists only when some source sets
`commands.enabled`; with nothing enabled the service builds none of it (no dispatcher, no durable,
no reader) and behaves exactly as before. The awaiting bucket is the one exception: the broker
administrator creates it wherever sparkplug-ingest is deployed, as it does for every declared
bucket, and nothing writes to it unless commands are enabled. It is started BEFORE the sources connect and stopped before
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

## Confirmation (`downlink/confirm.go`, `host/report.go`)

**"Confirmed" means the node REPORTED the requested value. It does not mean the node obeyed.**
Sparkplug has no command acknowledgement; the evidence is only as trustworthy as the broker's
publish ACLs (anything that can publish to the node's topic can make the metric read that value).

- **The tap.** `SessionTracker` copies out, under its lock, a `Report` per accepted
  non-historical, non-null metric of a BIRTH or DATA message (resolved through the alias table, the
  value in the form of the BIRTH-declared datatype: `WireValue`, which compares with `==`), and one
  per accepted NBIRTH. A redelivered NBIRTH of the live session, and any message the session
  machine rejected, report nothing. It is switched on by `EnableReports` only for a source with
  commands enabled. The Client hands the reports to the dispatcher's `Reported` hook AFTER
  `Observe` returned: matching and the response publish never run under `tr.mu`, and the response
  publish runs on a goroutine of its own, tracked by the term.
- **The mirror.** The awaiting set is mirrored in memory (`Dispatcher.entries`, by KV key;
  `byTarget` for matching; `byMetric` for supersede). An entry is added in the publish hook, after
  the KV write and before the bytes go out, so a node that answers in a millisecond finds it.
  Reports are matched on (tenant, source, target): the same names on another source's broker are
  another node.
- **Matching a write:** same metric, `isNull` false, not historical, value equal, and the metric's
  timestamp (else the payload's, else receipt) no earlier than `publishedAt - clockSkew`. A report
  of the metric at ANY OTHER value marks the entry contrary. Floats compare exactly.
- **Rebirth** is confirmed by any NBIRTH of the node received at or after the publish; **reboot**
  by an NBIRTH whose bdSeq differs from the one the node had at the publish (a same-bdSeq NBIRTH is
  the node answering a rebirth). Both sequences must be known: a birth, or a baseline, without a bdSeq
  proves nothing, so the reboot stays awaiting and ends UNANSWERED at its window. A command addressed to a device resolves its rebirth to the node.
- **UNCHANGED.** At the publish the Client reads the table's last-reported value; if it already
  was the requested value the entry is marked. That read comes BEFORE the entry joins the mirror,
  so it is not trusted at the end: the window's end re-reads the value (`Source.LastReported`, off
  `d.mu`) on the table the command was addressed against (epoch and generation are kept on the
  entry), and it must still be the requested one. A contrary report in the gap, or a node that died
  or re-birthed meanwhile (no table any more), ends UNANSWERED. With the mark, no contrary report,
  that re-check and `hostRole: primary` the window resolves SUCCESSFUL `via: UNCHANGED, since:
  <last report>`. Secondary hosts never get it (another host may be driving the value).
- **What is captured.** Reports and last-reported values are kept only for the metrics the
  source's `writableMetrics` allowlist names (plus node births, which a rebirth and a reboot are
  confirmed by); the table would otherwise hold a value for every metric of every node. An integer
  a datatype cannot hold (300 in an Int8) is not a report of that datatype.
- **A commanded rebirth is marked pending BEFORE the bytes go out** (`putOnWire`), because a node
  can answer with its same-bdSeq NBIRTH before the publish call returns, and an answer that finds no
  mark is dropped as a redelivery. A definitely-not-sent rebirth gives the mark back (restoring a
  request the session machine had made itself); an unknown outcome keeps it.
- **Window expiry.** A sweep (`SweepEvery`, 1 s) resolves entries past their deadline
  UNANSWERED, which command-delivery records as TIMEOUT/UNKNOWN at once. The deadline is the
  publish plus the source's window (120 s write/rebirth, 600 s reboot), clamped to the command's
  expiry.
- **Every resolution deletes the KV key after the response is published.** A response that cannot
  be published leaves the entry decided and in place; the sweep retries it every 5 s with the
  SAME outcome (a confirmed command is never re-decided into a timeout). The decided outcome is
  also stored with the record (`Awaiting.Decided`), so a new leader settles the command with it.
  Retrying stops at the command's own expiry (command-delivery has ended the row by then), or two
  hours after the publish (the bucket's lifetime), whichever is first.
- **Handover.** A graceful end of term resolves nothing and leaves every entry. The next term's
  `seedTerm` runs before it serves (and before its sources connect): it lists the bucket, re-arms
  every entry of a tenant it serves with the time left on its deadline, resolves at once the ones
  past it (UNANSWERED, with the nonce stored with the entry), and loads the reboot marks. The new
  leader's rebirth-all then delivers BIRTHs that carry the current values, which settle most
  re-armed entries. Entries of tenants this service has no source for are left alone.
  An entry is decided and claimed BEFORE it joins the mirror, and the "already there" check and
  the add are one critical section (`addEntryIfAbsent`). While the bucket could not be read, the
  keys settled meanwhile are remembered and skipped, because the listing the load finally gets may
  predate their settlement: without that a settled command is re-armed and answered a second time.
- **Two leaders can overlap.** A lost lease does not stop the old leader instantly, and the
  dispatch nonce does NOT fence the two: both hold the nonce stored with the entry, so both can
  publish a response for the same command. command-delivery keeps the FIRST terminal answer and
  ignores the rest (`MarkResponse` on a terminal row is a no-op). The window of overlap is the lease
  TTL; nothing here tries to close it, and nothing relies on it being closed.
- **The one-reboot-per-window rule is persisted.** `PutReboot` writes `tenant|reboot|group/node`
  (value: publishedAt) in the same bucket, with the awaiting record and before the bytes go out;
  it is NOT deleted when the reboot resolves, it is seeded into `d.reboots` at term start, and it
  ages out with the bucket's two-hour lifetime (which exceeds the largest reboot window). A reboot
  that is not sent (the session dropped, the table moved, the mark could not be written) takes its
  mark back. A leader that cannot read the bucket refuses reboots (`refused_reboot_gate`) until it
  can, and keeps retrying in the background; writes and rebirths are served meanwhile.

Decisions taken in this slice, so the next does not reopen them:

- **A re-armed entry never confirms UNCHANGED**, including one resolved at the start of a term
  because its deadline passed. The reports that would have shown a contrary value may have been the
  previous leader's to see (or nobody's), so "nothing contrary was seen" cannot be asserted. It can
  still be confirmed by a report of the value (the rebirth-all BIRTH), and otherwise ends UNANSWERED.
  Success is never claimed without evidence.
- **An unknown-outcome publish is awaited, not answered.** It is counted `publish_failed`, kept in
  the bucket and in memory, watched by the breaker, and settled by the window like any other: a
  report of the value confirms it, none ends it UNANSWERED. It is never handed back to be sent
  again. (The dispatcher answered it UNANSWERED at once before this slice; with the window running
  there is nothing to gain by answering early, and a report would have been thrown away.)
- **Late reports.** A response that loses to the row's TTL in command-delivery is ignored there
  (`MarkResponse` on a terminal row is a no-op), so nothing extra is needed.
- The `command_confirm_seconds` histogram observes only confirmed commands; unconfirmed ones are
  `commands_total{result="unconfirmed"}`.

## Enablement (chart, alerts, console)

- **Turning it on.** `functionalAreas.sparkplug-ingest.config.sources[].commands.*` in the chart values.
  dcctl's values overlay refuses `sparkplug-ingest` sources, so commands are enabled through Helm values
  only. The chart derives command-delivery's `sparkplugCommands` flag from the sources (matching keys
  without regard to case, as the service binds them) and fails the render on a hand-set flag, on commands
  without `command-delivery` or its in-cluster address, on a non-boolean `enabled`, and on the same key
  spelled twice under different capitalisation. `hack/check-sparkplug-commands-chart.sh` pins all of it,
  with a self-test that plants each defect.
- **Alerts** (edge rule group): `SparkplugCommandsUnconfirmed`, `SparkplugCommandBreakerOpen` (fires when
  the breaker OPENED in the last 20 minutes; the real breaker goes open -> half-open on its own and only a
  published probe closes it, so "not closed" would fire forever on an idle source) and
  `SparkplugCommandResponsesFailing` (the five fault results: claim_failed, park_failed, publish_failed,
  kv_failed, response_failed).
- **Console.** "Add Sparkplug standard commands" on the profile, a free-text metric and value form for
  `sparkplug.write`, and outcome help under the command's status. The help reads the stored response
  and keys on its machine-readable `reason` code, not on the human error text. A code the console does
  not know says nothing extra.
- **Not shipped:** the Tahu and Mosquitto interop run, and the amendment of the Sparkplug ADR.

Accepted deviations from the design, and rulings recorded here so the next slice does not reopen
them:

- **Supersede runs after the claim** (gate 11), in the design's order, so a command that is claimed
  and then refused has already resolved the one it replaced.
- **`Dc-Command-Expires`** and the extra `commands_total` results (`claim_failed`, `park_failed`,
  `publish_failed`, `tenant_gone`) are accepted. The alert on faults must cover `claim_failed`,
  `park_failed` and `publish_failed` as well as `kv_failed` and `response_failed`.
- **The awaiting bucket** is created wherever sparkplug-ingest is deployed, with a two-hour lifetime
  that clears the largest reboot window (one hour); a test pins that it exceeds both maximum windows.
- **Reboots are never parked.** A reboot is sent to a node that is online when it is dispatched or
  it is refused (`refused_reboot_gate`, "re-issue it when the node is online"), live and drained.
- **A publish whose outcome is unknown is not "not sent".** Only "not connected" and the pre-wire
  errors are answered UNSENT. Any other failure leaves the record in place, counts for the breaker's
  watch, and is awaited like a published command (see Confirmation). Only a finished term
  (`ErrTermEnded`) hands a claimed row back to be sent again.
- **Stopping a client ends its Host session** exactly as a lost one is ended (not up, no index, no
  tables), so a next term cannot publish before ONLINE or on a stale alias.
- **Ordering:** a wake already pending for a device is drained before its next live command is
  taken, so a live command does not overtake rows parked before it.

Things worth knowing:

- **The reverse index** (`host/commands.go`) is filled from the resolve each accepted birth already
  makes (the registrar's cache answers it) and emptied at death, at an NDEATH cascade and at the
  end of the Host session. A session that is down therefore holds nothing: it reads as not born.
- **The breaker** (`host/breaker.go`) is per Client and in memory. A session lost within five
  seconds of a command publish opens it (5 minutes, doubling to an hour); half-open admits one. Its
  state is exported as `command_breaker_state{tenant,source}`, read at scrape time so a backoff that
  has ended reads half-open without anything having to touch it.
- **Metric names** are the service's usual `devicechain_sparkplugingest_` prefix plus
  `commands_total{result}`, `commands_awaiting`, `command_confirm_seconds`,
  `command_breaker_state{tenant,source}` and `command_breaker_trips_total` (not the design's
  `sparkplug_` spelling).
- **`Dc-Command-Expires`** is a routing-side header command-delivery stamps for Sparkplug commands
  so the rate wait can look at the expiry again. Absent means unknown; the claim is still the
  authority on expiry.
- **A reboot that had to be parked is refused when drained**: the impact header exists only on the
  live message, and an unknown impact fails closed. Re-issue it.
- **Stranded-SENT**: Sparkplug stays off the allow list, deliberately. Re-arming a QoS 0 DCMD would
  actuate it a second time.
