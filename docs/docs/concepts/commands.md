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
because the transport that owns the connection says so directly. A periodic pass re-checks
the withheld set against what the platform currently believes about each device, so a
backlog is still released if that announcement is itself lost. Both paths depend on the
platform learning the device is back: it is the presence record changing that releases the
hold, and the announcement only makes it happen sooner.

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

A backlog of withheld commands drains two ways and no others: a device comes back, or a
command's TTL lapses and it records `EXPIRED`. For a fleet that stays off, that means the
backlog sits until the TTL horizon. So it is bounded per tenant, and the bound is a real
number at every level — **no setting means "unlimited."** An unbounded backlog is a
tenant-triggered, operator-invisible growth in durable storage.

The limit resolves down a cascade: the tenant's own override if it has one, otherwise its
tier's, otherwise the platform default of **10,000**. A tenant already at its limit has the
next command refused with the code `HELD_CEILING_EXCEEDED`.

#### Part of the ceiling is reserved for delivery {#delivery-machinery-reserve}

Not all of that ceiling is available to you. A share of it — **20% by default**, so 2,000 of
the platform's 10,000 — is kept for the platform's own command delivery, and only the
platform draws on it. Everything that issues commands on your behalf is bounded by the
remainder: the console, the SDKs, `dcctl`, and your own integrations alike.

This exists because of what a fleet write can otherwise do. "Reboot every pump" is a single
legitimate request that can fill the whole ceiling at once, and from that moment every
command your automation rules try to send is refused — until the backlog drains, which for
an offline fleet can mean days. The reserve keeps your alarm-driven automation working
while a fleet write is in flight.

It applies the same way whether you send one command or ten thousand: a batch is admitted
up to the same limit a loop of single commands would reach, so there is no way around it
and no advantage to either shape. See [One command, many devices](#command-batches).

A refusal names the limit that actually applied, and when the reserve is what bound you it
says how much was set aside — so a caller refused at 8,000 against a visible ceiling of
10,000 can tell the two numbers apart. The reserve is an operator setting, not a tenant
one; it cannot be raised or lowered per tenant.

:::info It bounds undelivered work, not just held work
The count is every command **`QUEUED` or `HELD`** — not only the ones withheld for absent
devices. A tenant whose fleet is entirely present can still be refused, purely on in-flight
enqueue volume. Queued commands drain within a tick, so their steady-state contribution is
about one tick of enqueue rate: small, but not zero, and a tenant issuing near its ceiling
at a high rate will feel it. The bound is on undelivered work, and undelivered is
undelivered.
:::

That refusal is the **only temporary one** the enqueue path produces. Every other rejection
describes a request that will be exactly as wrong on the next attempt; this one clears on
its own as those commands go out. It is the one code worth retrying, and the rest are worth
surfacing to a person. See
[Sending a command](../guides/sending-commands.md#when-an-enqueue-is-refused).

Cancelling a command records `CANCELLED`. Cancellation and TTL expiry shared the single
value `EXPIRED` until recently, so commands cancelled before that change still read as
`EXPIRED`; both appear in historical data, and nothing recorded which `EXPIRED` rows came
from a cancel, so they cannot be told apart after the fact.

**Only the device can report success.** `SUCCESSFUL` and a device-reported `FAILED` are the
two outcomes it alone can produce; every other terminal is one the platform writes on its
own — `TIMEOUT`, `EXPIRED`, `CANCELLED`, or a `FAILED` recorded because the transport
cannot carry the command at all. Reporting the result
is the device's half of the contract — see
[Responding to a command](../guides/connecting-a-device.md#responding-to-a-command). A
device that never responds leaves its commands in `SENT` until their TTL turns them into
`TIMEOUT`. Every command carries a TTL — one you set with `expiresAt`, or the platform
default of seven days — so set your own if your devices do not report outcomes and a week
is longer than the command stays useful.

Each device receives commands on a topic scoped to that device alone, and is authorized
for that topic only — a device cannot observe commands addressed to any other device in
its tenant.

## One command, many devices {#command-batches}

A **command batch** fans one command out to many devices as a single, recorded operation.
The devices are either named explicitly or resolved from an **entity group**, and every one
of them receives the same command key and the same payload. To issue one, see [Commanding a
fleet](../guides/commanding-a-fleet.md).

Everything above still applies per device: each command is validated against that device's
capability contract, withheld if the device is away, tracked through the same lifecycle, and
bound by the same TTL. A batch changes nothing about what happens to one command — it
changes what the platform *remembers* about the operation as a whole.

That record is the point. A device the platform refuses is given no command row at all —
there is no state meaning "wanted but not created" — so without a batch record a refusal
would leave no trace, and an operator who fires a fleet push and comes back in the morning
would have nothing to read. The record keeps how many devices the target resolved to, how
many were actually enqueued, and which ones were refused and why.

Three things distinguish it from a loop of single commands, and none of them is convenience:

- **A group target is frozen at fire time.** The batch resolves the group's **published**
  membership — never a draft selector — and records the group version it resolved against.
  Editing the group afterwards cannot change what already went out, and an audit can still
  answer what the group *meant* when the batch fired.
- **A partial fan-out is a decision, not a default.** On a real fleet some devices cannot
  receive the command. The caller must state whether that is acceptable: if it is not, the
  whole batch is refused and **nothing is created**, including the record, because nothing
  happened. If it is, the devices that can receive the command get it and the rest are
  recorded as refusals.
- **It can be called off as one operation.** Undoing a loop means cancelling each command
  separately, and still knowing every token it was issued under.

The counts on the record — how many devices resolved, how many were accepted — describe
**the moment the batch fired**, not the present. Command rows are not immortal, so a live
count could drift below the creation-time truth with no refusal explaining the gap. Present-
tense questions ("of the 5,000 queued, how many have gone out?") are answered by searching
the commands the batch created, not by re-reading the batch.

The refusals are stored two ways for the same reason a large fan-out needs both: an
individual list, capped so one fleet write cannot store an unbounded blob, and complete
per-code totals that are never truncated. The totals are what make the record self-auditing
— the devices resolved always equal those accepted plus the sum of the refusal counts, even
when the named sample is short.

### Cancelling a batch stops what has not gone out {#cancelling-a-batch}

Cancelling a batch moves its commands from `QUEUED` or `HELD` to `CANCELLED`. Commands
already `SENT` are left alone, and the devices holding them will still act on them.

That is narrower than cancelling a single command, deliberately. `SENT` means the command is
already at the device, so cancelling it recalls nothing — all it does is make the platform
stop listening for the device's answer, replacing a real outcome with a record saying an
operator called it off. Doing that to thousands of commands at once would destroy the
evidence of what the fleet actually did. Cancelling a single command is still permitted from
`SENT` today; that will be narrowed to match.

A batch cancel never refuses. A brake that declined to engage because part of the fleet had
already moved would leave the rest of the fleet commanded, which is the worst available
outcome — so it always engages and reports what it caught. The batch record is stamped with
when it was cancelled and how many commands that call caught, and the stamp is first-wins: a
second cancel does not overwrite what the first recorded.
