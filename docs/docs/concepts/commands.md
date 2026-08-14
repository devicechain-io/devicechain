---
title: Commands
---

# Commands

Command dispatch in DeviceChain is **two-way and persistent**. A command you issue is
validated against the device's capability contract, stored, dispatched only when the
device can actually receive it, and tracked until the device reports what happened or
the command's time-to-live runs out. Nothing here is fire-and-forget.

To issue one, see [Sending a command](../guides/sending-commands.md).

## The capability contract {#commands-and-the-capability-contract}

A device profile can declare the **commands** its devices accept, each with a typed
parameter schema (name, data type, required, min/max, enum). Those declarations are what
make the profile a contract rather than a label.

When a command is enqueued, it is validated against the **published** profile version — not
the draft. Three outcomes:

- **The profile declares no commands.** Anything is accepted. Declaring a vocabulary is
  opt-in, so a profile that has not adopted one keeps working exactly as before.
- **The profile declares commands, and the key matches one.** The payload is validated
  against that command's parameter schema: unknown parameters, wrong types, out-of-range
  values, and missing required parameters are all rejected.
- **The profile declares commands, and the key matches none.** Rejected — a device cannot
  be sent a command its capability contract does not include.

Command keys are matched **exactly**, including case. A mis-cased key is a mis-keyed
actuation, which is the thing this validation exists to stop.

Validation reads the published snapshot deliberately. A definition you have authored but
not yet published has not been communicated to anything downstream, so enforcing it would
reject commands the device actually accepts. Publish the profile to put a new command into
force.

The published vocabulary is readable, not just enforceable: a device reports which commands
it currently accepts, and the console uses that to offer them directly — a picker of
declared commands and a typed form built from the selected command's parameter schema,
rather than a free-text box. A profile that declares no commands still gets the free-text
form, matching what the platform will accept. Commands you have authored but not published
are named alongside the picker as unavailable, so a missing command reads as "not published
yet" rather than as a missing feature.

## Command lifecycle

An issued command is persisted and tracked, not fire-and-forget. It moves through:

These states mean the command is not finished yet:

- **`QUEUED`** — accepted and validated, awaiting its first dispatch decision. Genuinely
  transient: a command does not linger here.
- **`HELD`** — the platform is deliberately withholding the command because the device is
  known to be away. This is where an offline fleet's backlog collects, and it can sit for
  days. A held command counts as in flight: it can still be cancelled, and a TTL that
  lapses on one records `EXPIRED` rather than `TIMEOUT`, because the command never went
  out. It returns to `QUEUED` when the device comes back.
- **`SENT`** — published to the device's own command topic, awaiting its response.

These are terminal, and nothing moves out of a terminal state:

- **`SUCCESSFUL`** / **`FAILED`** — the device reported the outcome.
- **`TIMEOUT`** — it was dispatched and the device never answered.
- **`EXPIRED`** — its TTL elapsed before it ever went out.
- **`CANCELLED`** — an operator or a tenant called it off.

`EXPIRED` and `TIMEOUT` answer different questions, and mistaking one for the other sends
you looking in the wrong place: `EXPIRED` means the command never left the platform, so a
run of them says deliveries are not being attempted; `TIMEOUT` means it did go out and
nothing came back, which points at the device.

### Commands to a device that is away

Over MQTT a command reaches only a device that is connected and subscribed at that
instant — the broker does not hold it for a device to collect later. So a command
published toward an absent device is simply lost, and the platform would have no way to
know: it recorded the command as sent, nothing answered, and a week later it read
`TIMEOUT` — a permanent record blaming a device that was never given the command.

The platform therefore checks before it publishes. When a transport reports that a device
is not connected, its commands move to `HELD` instead of being published, and return to
`QUEUED` when the device comes back — normally within a second or two of it reconnecting,
because the transport that owns the connection says so directly. A periodic pass catches
anything that announcement misses, so a returning device's backlog is always released
eventually even if the platform never hears about the reconnection itself.

Three limits are worth knowing:

- **The check needs a transport that reports connections.** For a device whose transport
  only carries data, "no events recently" is not evidence the device cannot receive — a
  device reporting hourly is quiet for 59 minutes of every hour and reachable throughout.
  Those commands are dispatched as before.
- **It is a check, not a queue.** A device that disconnects between the check and the
  publish still loses the command. What is removed is the case the platform could see
  coming.
- **Commands to Sparkplug devices are not delivered at all.** The path is not built —
  Sparkplug nodes live on your own MQTT infrastructure rather than the platform's, and
  nothing bridges the two. A command issued to one is recorded `FAILED` immediately, with
  that as its reason, rather than being held for a return that would not help.

A run of `TIMEOUT` against devices you know are intermittent is therefore still worth
reading as a statement about when they were connected rather than about their firmware —
but it should now be a much shorter run.

### How much backlog a tenant may hold {#held-command-ceiling}

Nothing drains a `HELD` backlog on its own. It grows for as long as devices stay away and
shrinks only as they come back, so it is bounded per tenant — and the bound is a real
number at every level. **No setting means "unlimited."** An unbounded backlog is a
tenant-triggered, operator-invisible growth in durable storage.

The limit resolves down a cascade: the tenant's own override if it has one, otherwise its
tier's, otherwise the platform default of **10,000** held commands. Issuing a command that
would push a tenant past whichever applies is refused with the code
`HELD_CEILING_EXCEEDED`.

That refusal is the **only temporary one** the enqueue path produces. Every other rejection
describes a request that will be exactly as wrong on the next attempt; this one clears on
its own as absent devices return and their commands drain. It is the one code worth
retrying, and the rest are worth surfacing to a person. See
[Sending a command](../guides/sending-commands.md#when-an-enqueue-is-refused).

Cancelling a command records `CANCELLED`. Cancellation and TTL expiry shared the single
value `EXPIRED` until recently, so commands cancelled before that change still read as
`EXPIRED`; both appear in historical data, and nothing recorded which `EXPIRED` rows came
from a cancel, so they cannot be told apart after the fact.

**A command only reaches a terminal outcome if the device answers.** Reporting the result
is the device's half of the contract — see
[Responding to a command](../guides/connecting-a-device.md#responding-to-a-command). A
device that never responds leaves its commands in `SENT` until their TTL turns them into
`TIMEOUT`. Every command carries a TTL — one you set with `expiresAt`, or the platform
default of seven days — so set your own if your devices do not report outcomes and a week
is longer than the command stays useful.

Each device receives commands on a topic scoped to that device alone, and is authorized
for that topic only — a device cannot observe commands addressed to any other device in
its tenant.
