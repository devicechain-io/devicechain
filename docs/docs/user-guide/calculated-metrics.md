---
title: Calculated metrics
sidebar_position: 5
---

# Calculated metrics

A **calculated metric** is a metric on a device profile whose value is computed from a formula
instead of being reported by the device. A meter that reports `voltage` and `current` can have a
calculated `power_kw` of `voltage * current / 1000`, and from then on `power_kw` is stored,
charted, alarmed on and shown on the device like any measurement the device sent. This chapter is
for people who author device profiles. It covers what a calculated metric is, how to write one,
the formula language, and what happens when a formula cannot produce a value.

Calculated metrics are **off by default**. Your instance operator turns them on by giving your
tenant's tier a ceiling above zero. See [Turning calculated metrics on](#turning-calculated-metrics-on).

## What a calculated metric is

- A formula reads the metrics of **one reading**: the **measured** values a device reports together
  in a single measurement, other **calculated** metrics of the same profile (see
  [Chaining calculated metrics](#chaining-calculated-metrics)), and optionally numbers the **device
  itself** carries, such as a calibration factor (see [Device constants](#device-constants)). It
  never reads an earlier reading, another device, or the clock.
- A **reading** (a sample) is every measurement one message carries for the same instant, however
  the device split them into entries. A device that sends one metric per entry, as Sparkplug and
  LwM2M devices do, still gets a formula over two of those metrics computed, as long as they share
  an instant. The instant must match exactly: metrics a device timestamps one by one, even a
  millisecond apart, are separate samples, and a formula never reads across them. A formula also
  reads only the metrics that message carries, so a device that reports only what changed (as
  Sparkplug nodes usually do) gets a value only when every input arrives together. If the same
  metric appears twice at one instant, the formula uses the first of the two; both values are
  still stored as sent.
- The result is **computed once, when the reading arrives** (or later by a
  [backfill](#backfilling-history), for readings that have none), and stored as a normal
  measurement next to the values the device sent. Dashboards, history, exports, the device's
  latest values and detection rules all read it like any other metric, by its key. A backfilled
  value appears in dashboards, history and exports only; it does not change latest values and is
  never evaluated by detection rules.
- **Stored values are never changed.** Editing a formula and publishing the profile changes what
  is computed for readings that arrive afterwards; values already stored keep the formula that was
  active when they were computed. A backfill only adds values to past readings that have none.
- Whether a metric is measured or calculated is **fixed when you create it**. You can edit a
  calculated metric's formula, but you cannot turn a measured metric into a calculated one or the
  other way round. Delete it and create a new one instead.
- A device cannot overwrite a calculated value. If a device sends a value under the key of a
  calculated metric, the device's value is dropped and the computed one is kept.
- When you bind a dashboard widget to a device, the widget's **Measurements** field lists the metrics the device's published profile declares, and marks calculated ones with a **Calculated** badge. Select a metric to add or remove it from the widget. You can still type any key by hand.

## Authoring a calculated metric

Open a device profile and choose the **Metrics** tab (see
[Metrics](device-types-and-profiles.md#metrics)). Choose **New metric**.

1. Set **Kind** to **Calculated**. The choice is final, so it is locked when you edit a metric.
2. Fill in the **Metric key**, the **Token** and the **Data type**: **Double**, **Integer** or
   **Boolean**. The formula must produce exactly that type (see [Types](#types)). Calculated
   metrics cannot be strings.
3. Optionally set a **Unit**, and a **Min** and **Max**. A result outside that range is skipped,
   not stored.
4. Type the **Formula**.
5. Choose **Create metric**.

Calculated metrics carry a **Calculated** badge in the Metrics list.

Like every profile edit, a new or changed formula applies to devices only after you
[publish a version](device-types-and-profiles.md#publishing-a-version).

### Checking a formula as you type

The formula box asks the server to check the formula as you type, using the same compiler that
checks it at publish time and computes it on real readings. Below the box you see:

- the **compile error**, in words, if the formula is not valid;
- the **estimated cost** against the ceiling of 100. A formula whose estimated cost is above 100
  is refused;
- **warnings**, which never block a publish. For example, that the published version declares this
  key as a measured metric (so devices may be sending it, and those values will be dropped), that
  a result of magnitude 1e12 or more cannot be stored, or that units are not converted.

Once the formula compiles, a **sample** panel lists the measured metrics the formula reads. Type a
value for each, and the panel shows the **value** the formula would produce or the **reason** it
would produce none. If the formula reads other calculated metrics, the panel also shows the
**chain** it builds on, and asks for the measured metrics the whole chain reads: the calculated
ones are computed from your sample, in order, exactly as they are when a reading arrives. If the formula reads [device constants](#device-constants), a **Device
constants** group lists them too: type the number the device's metadata would hold for each, and
leave one blank to see what a device without it gets. The sample is hand-typed, and nothing is
stored.

### Trying a formula on recent history

Below the sample, **Try it on recent history** runs the formula over one device's stored readings.
Enter a **Device token**, choose a window (the last 1, 24 or 168 hours) and choose
**Run on history**. The panel shows how many samples it evaluated, how many produced a value, how
many were skipped and why, and the newest 50 samples with the inputs the formula read and what it
gave. A sample is the readings one message carried for one instant, which is exactly what a formula
sees when readings arrive. Nothing is stored.

- The inputs are the **stored** values, which keep 8 decimal places (see
  [Storage and precision](#storage-and-precision)). The value computed when a reading arrives uses
  the device's full-precision value, so the two can differ for an input with more decimal places
  than that: a current of `0.000000015` A is stored as `0.00000002`. The panel says so on every
  result, and what it shows is a preview, not the value that will be stored.
- Only the metrics the formula reads are shown, and a message that carried none of them is not a
  sample. A calculated metric the formula reads is taken from the value **stored** for it when
  each reading arrived, under the formula that was published then, not recomputed from the draft.
- If the formula reads [device constants](#device-constants), the panel uses the typed device's
  **current** metadata for them, for every sample: not the values the device had when each reading
  was recorded. It says so under the result. A constant the device's metadata lacks makes its
  samples `missing_input`.
- A window is at most 7 days. One holding more than 50,000 samples, or more than 250,000 input
  values, is refused rather than cut short. Choose a shorter window.
- It needs permission to read the device's events. A member limited to some customers sees only
  the readings recorded while the device belonged to one of them.

### What the publish check refuses

Publishing a profile checks every calculated metric and reports all the problems at once. It
refuses a profile when:

- a formula does not compile or does not produce the declared type;
- a formula reads a key that is not a declared metric of the same profile;
- calculated metrics read each other in a cycle (the message names it, for example
  `a -> b -> a`), or a metric reads itself;
- a chain of calculated metrics is longer than 4 (the message names the chain);
- a formula reads no metric at all (a device constant is not a metric);
- a formula reads more than 8 device constants, or the formulas of one profile together read more
  than 32;
- a formula is longer than 1,024 bytes, its estimated cost is above 100, or the formulas of one
  profile together cost more than 1,000. The formulas of a chain are all evaluated for each reading,
  so a chain costs the sum of its formulas;
- a calculated metric's Min is above its Max, it declares allowed values, or its data type is not
  Double, Integer or Boolean;
- a key is declared twice;
- the profile has more calculated metrics than your tenant's tier allows.

## Calculated metrics in a rule preview

When you preview a detection rule (**Preview against history**, on a rule form or on the automation
canvas), the preview uses the profile's **draft** calculated metrics. Each replayed reading has its
calculated metrics recomputed from its measured values with the draft's formulas, replacing the
values stored when it arrived. A rule over a calculated metric you have just added or edited is
therefore previewed against the values that metric will have once you publish, and a calculated
metric the draft no longer has gives the preview no value. Calculated metrics that read each other are recomputed in dependency
order, as they are when a reading arrives. The preview says how many readings were
recomputed and how many formula results produced no value.

A formula that reads [device constants](#device-constants) is recomputed with each device's
**current** metadata, read when you run the preview. A reading stored under an earlier value
therefore previews under today's, and can differ from the value that was stored then. A device
whose metadata lacks a constant gets no value for its readings. If the constants cannot be read,
or the profile has more than 10,000 devices, the result says so rather than computing with
defaults.

The recomputation reads each reading's values as the device sent them, at full precision, so it
gives the same value the published formula would compute for that reading. Two things it cannot
reconstruct: a value that was dropped when the reading arrived (for example one a device sent under
a key that was a calculated metric at the time), and changes to the definitions of measured
metrics. Only calculated values are recomputed. Nothing a preview computes is stored, and none of
it reaches the live rules.

## The formula language

Formulas are written in a small expression language. It has no loops, no strings and no
state, so every formula runs in a small, bounded amount of time.

### Inputs

Each measured metric of the profile is a variable named by its key, such as `voltage` or `temp`, and
so is each calculated metric it may read (see [Chaining calculated
metrics](#chaining-calculated-metrics)). A key that is not a plain identifier, such as `flow-rate`,
is read as `m["flow-rate"]`, wrapped in the conversion for its type: `double(m["flow-rate"])`,
`int(m["count"])` or `bool(m["door"])`.

### Operators

| Kind | Operators |
| --- | --- |
| Arithmetic | `+` `-` `*` `/` `%` and unary `-` |
| Comparison | `==` `!=` `<` `<=` `>` `>=` |
| Logic | `&&` `\|\|` `!` |
| Choice | `condition ? a : b` |

### Math functions

`math.abs(x)`, `math.floor(x)`, `math.ceil(x)`, `math.round(x)` (halves round away from zero),
`math.sqrt(x)`, `math.pow(x, n)`, `math.min(a, b)`, `math.max(a, b)` and
`math.clamp(x, low, high)`.

`math.pow` takes a **whole-number exponent** only: `math.pow(x, 2)` works and `math.pow(x, 0.5)`
does not. Fractional powers can differ in the last digit between processor architectures, and a
stored value must not depend on which machine computed it. For a square root, use `math.sqrt`.

### Presence

`has(m.fan)` and `"fan" in m` test whether a reading carries a value for `fan`. This is how you
give a missing input an explicit default: `has(m.fan) ? fan : 0.0`.

### Chaining calculated metrics {#chaining-calculated-metrics}

A formula can read another calculated metric of the same profile, by its key, exactly as it reads
a measured one. Real power, apparent power and power factor make a chain of three:

| Key | Formula |
| --- | --- |
| `kva` | `voltage * current / 1000.0` |
| `kw` | `kva * pf_in` |
| `pf` | `kw / kva` |

- **A chain is at most 4 calculated metrics long.** A formula over measured metrics only is a
  chain of one, `kw` above is two and `pf` three. Publishing refuses a longer chain and names it.
  Calculated metrics also cannot read each other in a cycle, directly or through others, and
  publishing names the cycle.
- **They are evaluated in dependency order**, once per reading, whatever order you declared them
  in. Each formula runs after everything it reads.
- **A downstream formula reads the value the reading records.** The result of `kva` is formatted as
  its data type before `kw` reads it, so an Integer metric is a whole number and a Boolean is `1` or
  `0`, and `kw` reads exactly the value `kva` is given on the reading: the one detection rules and
  live views see. Storage then rounds it to 8 decimal places (see
  [Storage and precision](#storage-and-precision)), so for a value with more decimal places than
  that, the history shows a less precise `kva` than the one `kw` was computed from.
- **If an upstream metric produces no value, so do the formulas that read it.** If `kva` is
  skipped, `kw` is skipped as `missing_input`, and so is `pf` through it. To give such a formula a
  fallback, test for the metric as for any input: `has(m.kva) ? kva : 0.0`.
- **A device cannot supply a link.** A value a device sends under the key of a calculated metric is
  dropped, so it never reaches the formulas that read that key.
- Nothing about a reading's identity changes: calculated values, chained or not, are not part of
  it, so a resent reading is still recognised as the same reading.
- A chained value is computed from the reading and the profile version that was active, like any
  other, so the same reading gives the same values on every replica and in every preview.

### Device constants {#device-constants}

A formula can read a number that belongs to a **device** rather than to a reading, such as a
calibration factor or a current-transformer ratio. Write it as `device.` followed by the name:
`voltage * device.calibration`. For a name that is not a plain identifier, write
`device["ct-ratio"]`. The name `device` is reserved for this: a measured metric whose key is
`device` is read through `m`, wrapped in the conversion for its type, such as
`double(m["device"])`.

The number comes from the **device's metadata**. Edit the device and put the constant in its
metadata as a JSON number under the same name:

```json
{ "site": "north plant", "calibration": 1.02 }
```

- **Naming a constant declares it.** There is nothing else to set up on the profile. Publishing
  cannot know which devices carry which constants; it checks that the formula compiles and uses
  constants as numbers.
- Every constant is a **double**. A whole number is a double with no fraction, and
  `int(device.step)` reads it as an integer. A formula that reads constants but no measured metric
  is refused.
- A formula reads at most 8 constants, and the formulas of one profile together at most 32.
- A constant must be a **JSON number**. A number written as text (`"1.02"`), a boolean, `null`, an
  object or a list is not a number, and is never converted.
- Like a measured input, a constant is **required** unless the formula tests for it. A device whose
  metadata lacks it is skipped as `missing_input`, and one that holds something that is not a
  finite number is skipped as `invalid_input`. For a default, test for it:
  `has(device.calibration) ? device.calibration : 1.0`, or `"calibration" in device`.
- **The value is frozen into the result.** The constant is read when the reading arrives, and the
  computed value is stored like any other. Editing the device's metadata later changes what is
  computed for the readings that arrive after the change (a service can take a few seconds to see
  it) and never changes a value already stored.
- Each device has its own constants, so two devices of one type can have different values, and the
  same formula gives each its own result.

### Types

A formula is checked against the metric's data type, and the checks are strict:

- An **integer** and a **double** mix in `+ - * /` and in ordering comparisons (the integer is
  promoted). They do not mix in `==` or `!=`, or in the two branches of `condition ? a : b`: write
  `80.0`, not `80`, when comparing with a double.
- A formula for a **Double** metric must produce a double. A formula that produces an integer
  needs `double(...)`.
- A formula for an **Integer** metric must produce an integer. Convert a double with `int(...)`.
  `math.sqrt` and `math.pow` return doubles, so wrap them in `int()` for an Integer metric.
- A formula for a **Boolean** metric must produce a boolean, such as `temp > 80.0 && fan == 0`.
  Booleans are stored as `1` and `0`.

### Not available

Strings (except as the key in `m["key"]`), lists, maps, time and durations, randomness,
comprehensions, regular expressions, and anything that looks outside the reading.

## When a formula produces no value

If a formula cannot produce a value for a reading, that one metric is **skipped** for that
reading. The rest of the reading is stored as usual, and nothing is ever substituted: there is no
default value and no zero. The reasons are:

| Reason | Meaning |
| --- | --- |
| `missing_input` | An input the formula reads was not in the reading, or a device constant it reads is not in the device's metadata. An input or constant is required unless the formula tests for it with `has()` or `in`. |
| `invalid_input` | An input was present but is not a valid value for its type, or a device constant is present but is not a number. |
| `non_finite` | A number was not finite: an input or device constant that was not a finite number, a division by zero, a result too large for a double, or the square root of a negative number. |
| `div_zero` | An integer division or remainder by zero. |
| `overflow` | An integer result or conversion overflowed 64 bits. |
| `range` | The result is outside the metric's Min and Max. |
| `unstorable` | The result is 1e12 or larger in magnitude, which measurements cannot be stored with. |
| `eval_error` | Any other failure, such as `math.clamp` with its bounds reversed. |

**A reading that carries none of a formula's inputs is not a skip.** Devices often send their metrics in separate messages (a temperature every second, a battery level once an hour). A formula is evaluated only for a reading that carries at least one of the measured metrics it depends on, directly or through the calculated metrics it reads; a reading with none of them is simply not that formula's reading, and is neither computed nor counted in `calcmetric_skipped_total`. `missing_input` therefore means a reading carried **some** of a formula's inputs and lacked one it requires. A formula that tests every input it reads with `has()`, or whose untested inputs are only calculated metrics that do, is evaluated for every reading as before.

An absent input is noticed whatever the order of the operands. `temp > 80.0 || fan > 1` is skipped
on a reading with no `fan`, even though a hot reading would make the first half true. If you want
the formula to work without `fan`, test for it with `has()`.

## Checking that a metric is being computed {#checking-that-a-metric-is-computed}

A formula can compile, publish and still never produce a value, for example when its inputs arrive under a different name than the one it reads. On the profile's **Versions** tab, the **Calculated metrics in use** block shows, for each calculated metric of the **active** version, whether values are being stored for it. It is for staff with read access to devices and events; a customer-scoped user does not see it, because the counts are tenant-wide. A superseded version has no block, because no device resolves it.

The block is **counted from stored readings**. A calculated value is stored like any reading, so for each metric it compares how many values were stored under the metric's key with how many readings its inputs have (the measured metrics the formula reads, including through the calculated metrics it chains). Pick **Last 24 h** or **Last 168 h** (7 days) to change the window. Nothing is sampled or estimated.

| Status | Meaning |
| --- | --- |
| **Computing** | Values were stored for the metric in the window. |
| **Unconfirmed** | Values were stored under the metric's key, but another profile also declares a metric with that key, so they may all be that profile's (see below). |
| **Not computing** | The inputs have stored readings in the window and no value was stored for the metric. Use **Why?** to find out. |
| **No input readings** | Neither the metric nor its inputs have readings in the window, so there was nothing to compute from. |
| **Inputs unknown** | The active version's formula cannot run on this release (it does not compile, or its data type cannot be calculated), so it computes nothing and its inputs cannot be read. Counts for the metric are not shown. |

Things to know when reading the counts:

- **Counts start when the version became active.** A reading stored earlier cannot have been computed by this version, so it is left out, and the line above the table says since when. For a version activated before activation times were recorded, the time is read from the profile's version history; it can be earlier than the true activation, never later, so the counts can include a few readings from just before it.
- **Separate messages are fine.** A device that sends one metric every second and another once an hour is not "skipping" the formula on the seconds it does not send the second one. The block never compares against the number of messages, only values stored against input readings.
- **Counts are by when the reading occurred**, so a burst of old readings forwarded late lands in the hours it describes. They reach back only as far as your tenant's measurement retention.
- **A name shared with another profile.** If another profile of the tenant also declares a metric with the same key, the counts include that profile's readings and cannot show this formula is working. The row says so; use **Why?** on a device of this profile instead.

### Asking why {#asking-why}

**Why?** opens the same "Try it on recent history" view used while authoring, for the metric's formula in the active version, with the inputs that version reads (the draft may differ). Enter the token of a device that reports the formula's inputs and run it: it evaluates the formula over that device's stored readings and tallies, by reason, the readings that produced no value (see [When a formula produces no value](#when-a-formula-produces-no-value)). Nothing is stored.

## Backfilling history {#backfilling-history}

A calculated metric is computed when a reading arrives. Readings stored before the version that
defines it was published therefore have no value for it, and a chart of that time shows a gap. A
**backfill** computes the metric for those stored readings and adds the missing values, so charts
and exports of past time show it too.

### Starting a backfill

Open the profile, choose the **Versions** tab and, under **Backfilling history**, choose **Backfill
history**. Starting and cancelling a backfill need the `device:write` permission, and a
customer-scoped user never sees any of this. After you publish a version that adds a calculated
metric, the console asks once whether to fill the history for it.

1. **Version.** The published version whose formulas are used. It opens on the active version. A
   draft cannot be backfilled.
2. **Metrics to fill.** The metrics that are **new in the chosen version** (the previous published
   version did not calculate them) are selected. You can also select a metric whose formula
   changed since an earlier version. It is flagged, because the backfill then adds values with this
   version's formula to readings that have none, while readings stored under the older formula
   keep theirs. A metric that reads another calculated metric you did not select gets a warning
   too: that other metric may hold values from an older formula, and the new values would be
   computed from them.
3. **From and To.** The range to fill. It opens on the 30 days before the version became active,
   which is where the live computation takes over. A range is at most **90 days**; fill a longer
   history in several backfills.
4. **Devices.** Optional. Name up to 1,000 devices by token, or leave it empty to fill every device
   of the profile.

#### Try one device first

Before filling a whole fleet, use **Try one device first**: enter one device's token and start. It
runs the same backfill on that one device. It is safe to run and to repeat, because a backfill
never changes a value that is stored. Open that device's chart, check the values, then start the
rest. Devices already filled are counted as already holding a value and are not written again.

### What a backfill does

- **It only adds values.** A reading that already holds a value for the metric keeps it, and is
  counted as *already present*. Stored values are never changed.
- **It reads what is stored.** Inputs are the stored readings, which keep 8 decimal places, and the
  device's constants are read as the device's metadata holds them when the backfill reaches the
  device. A backfilled value can therefore differ in the last digits from the value that would have
  been computed when the reading arrived. The job says so ("8 decimal places").
- **It is silent.** A backfilled value appears in dashboards, history and exports. It does not
  change the device's latest values, and detection rules never see it, so a backfill raises no
  alarm for a past reading.
- **Long-range charts catch up when the job finishes.** Charts that read pre-aggregated data
  (long ranges) show the filled values once the job has refreshed that data, which it does as its
  last step.
- **It does not reach before the device existed.** Readings dated before the device was created
  are not filled, so a reused token never gets the values of an earlier device.

### Limits

| Limit | Value |
| --- | --- |
| Range | At most 90 days, ending no later than now. |
| Retention | The range cannot start before your tenant's measurement retention allows; such a request is refused rather than shortened. Stretches that age out while the job runs are skipped. |
| Compressed history | History the event store has compressed (older than 7 days by default) is **skipped** in this release and counted on the job. |
| Devices | Up to 1,000 named devices, or every device of the profile. |
| Concurrency | One active (queued or running) backfill per tenant. Starting a second is refused and names the active one. |

### Reading a job

The jobs panel lists each backfill with its status (queued, running, done, cancelled or failed), the
devices done out of the total, the values written, the readings looked at and how many already had
a value, and what was not computed, by reason. The reasons are the ones in [When a formula
produces no value](#when-a-formula-produces-no-value), plus:

| Reason | Meaning |
| --- | --- |
| `ambiguous` | One stored reading holds the same input twice with different values, so no single value can be chosen for it. Nothing is written for that reading. |

The panel also counts stretches of compressed history and stretches beyond retention that were
skipped. **Cancel** stops a queued job at once and a running job at its next batch; values already
written stay. A failed job names why; start a new backfill to continue, which skips what is already
filled.

A backfill groups stored readings exactly as the live computation does: a reading is every
measurement of one message at the same instant (see [What a calculated metric is](#what-a-calculated-metric-is)). A formula over
two metrics that a Sparkplug or LwM2M device sends in separate entries is therefore backfilled
when they share an exact instant, just as it is computed live. It gets no value, live or
backfilled, when the device timestamps them apart, even by a millisecond, or when the message did
not carry every input, as with a device that reports only what changed.

### When the control is disabled

The **Backfill history** button is disabled, and says why beside it, when the profile has nothing
published, or when the active version has no calculated metrics. When the instance cannot run
backfills (for example, the operator has switched the backfill worker off), the console asks the
instance when the page opens, and the button and the dialog are disabled from the start, saying
why. If that question could not be answered and a start is refused instead, they become disabled
and say why from then on, until you leave the page. It is never hidden for those reasons.

## Storage and precision

- Measurements are stored with at most 12 digits before the decimal point and **8 decimal places**.
  A calculated value is rounded to 8 decimal places when it is stored, and a result of magnitude
  1e12 or more is skipped as `unstorable`. Setting a Min and a Max inside plus or minus
  999,999,999,999 tells the editor you know the range and removes that warning.
- A calculated value is stored like any measurement, so it follows your tenant's measurement
  retention.
- Units are not converted or checked. A formula reads its inputs as plain numbers, and the result
  carries only the unit you declare on the calculated metric.
- A calculated value is identified by its reading, its metric name and its instant, not by its
  value. If one message carries two samples for the same instant, they produce one calculated row
  per metric name, and the first value written is the one kept.

## Turning calculated metrics on

How many calculated metrics one profile may publish is set per **tier**, by the instance operator,
as **Calculated metrics per profile** under **Admin → Tiers**. The platform default is `0`, which
means calculated metrics are off: you can write formulas and test them, but publishing a profile
that has one is refused with:

> cannot publish device profile: it has N calculated metric(s), and calculated metrics are not
> enabled for this tenant's tier

When the ceiling is above zero but below the number the profile has, the message says how many the
tier allows per profile. The most any tier can grant is 32. Ask your operator to raise the ceiling
if you need calculated metrics.
