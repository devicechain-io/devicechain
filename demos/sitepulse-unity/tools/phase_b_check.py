# Copyright The DeviceChain Authors
# SPDX-License-Identifier: Apache-2.0
"""
The WSL half of the Sitepulse Phase B negative controls and the Phase C soak.

phase_a_check.py owns the platform client, the report and the shared judges; this module holds what only
the later phases need, and is reached from its main():

    control rule-disabled    disable the low-fuel rule on the equipment profile (draft + publish), let the
                             player prepare a low tank on SP-HL-0006, and judge that the platform did nothing
    control observer-outage  scale event-management to zero while the fleet runs, then put it back
    soak                     one long Live run: every platform-side check of the 18 machines over time
    rule-restore, scale-restore
                             put back what a control changed (idempotent; the script runs them again from a trap)

Everything that decides a verdict is a pure function above the I/O (tested by test_phase_b_check.py). Everything
that changes the platform writes its undo record FIRST (rule-state.json, scale-pending.tsv) and removes it only
once the restore is verified, so a crash anywhere in between still leaves the script a record to act on.
"""

import datetime
import hashlib
import json
import os
import subprocess
import time

import phase_a_check as a

PROFILE_TOKEN = "sp-equipment-profile"
RULE_TOKEN = "sp-rule-lowfuel"
OTHER_RULE_TOKENS = ("sp-rule-overheat", "sp-rule-tyre-low")
ALARM_KEY = "low-fuel"
LOW_FUEL_LINE_PCT = 15.0

GO = "phaseA-go"
RULE_STATE = "rule-state.json"
SCALE_PENDING = "scale-pending.tsv"
SOAK_JSON = "soak.json"
MEMORY_TSV = "memory.tsv"
HARDWARE_TXT = "hardware.txt"

OUTAGE_DEPLOY = "event-management"
OUTAGE_RECOVERY_BOUND_S = 120.0

# the sentence the redelivery control records instead of a run: the evidence is the SDK's own tests
REDELIVERY_INFO = (
    "NOT run live, and not faked. A redelivery over MQTT is the broker re-sending a QoS 1 command the device never PUBACKed "
    "before the broker's ack_wait. The stranded-SENT reconciler (command-delivery) acts on LwM2M devices only, and nothing "
    "else on the platform re-dispatches a SENT command. The Sitepulse sessions run MaxConcurrentCommands = 4, so the SDK "
    "acknowledges a command as it admits it and the machine's work happens after: a live run has no honest way to hold an "
    "acknowledgement back, and a test fault that did would be inventing the broker behaviour it claims to test. The property "
    "is proven where it can be driven without faking, in the SDK: sdks/csharp/tests/DeviceChain.Sdk.Tests/"
    "MqttRealBrokerTests.cs ARealRedeliveryIsAnsweredAgainWithoutRerunningTheHandler (a real nats-server MQTT gateway, a "
    "handler that holds the PUBACK past ack_wait, the broker's own redelivery, handler invoked once, answered twice) and "
    "MqttDeviceSessionTests.cs (ARedeliveredCommandIsAnsweredAgainWithoutRerunningTheHandler, "
    "ADuplicateArrivingWhileTheHandlerIsStillRunningDoesNotRunItAgain, ARedeliveryAfterCompletionIsNotRunAgainWhenConcurrent, "
    "ARedeliveryDuringTheReconnectSupersedesTheHeldResponse), inside the declared history window (CommandHistorySize = 256)."
)


# ---------------------------------------------------------------------------------------------
# pure judges
# ---------------------------------------------------------------------------------------------


def _queued_since(commands, since):
    return [c for c in commands if c.get("queuedTime") and a.parse_time(c["queuedTime"]) >= since]


def judge_no_commands(commands, since):
    """Every command created for the device since `since` is a command the platform decided to send: there must be none."""
    made = _queued_since(commands, since)
    if not made:
        return True, "no command was created for the device in the window"
    return False, "the platform created " + ", ".join(f"{c['name']} ({c['status']})" for c in made[:5])


def judge_no_alarm(alarms, key, since):
    """No `key` alarm for the device is active, or was raised at or after `since`."""
    hits = []
    for al in alarms:
        if al.get("alarmKey") != key:
            continue
        raised = al.get("raisedTime")
        if al.get("state") == "ACTIVE" or (raised and a.parse_time(raised) >= since):
            hits.append(al)
    if not hits:
        return True, f"no {key} alarm is active or was raised in the window"
    return False, f"{len(hits)} {key} alarm(s): " + ", ".join(f"{h.get('state')} raised {h.get('raisedTime')}" for h in hits[:3])


def fuel_series(events):
    return sorted(((a.parse_time(e["occurredTime"]), e["value"]) for e in events if e.get("value") is not None), key=lambda x: x[0])


def fuel_rises(events, epsilon=1e-9):
    """Every rise in a machine's stored fuel, as (time, from, to)."""
    s = fuel_series(events)
    return [(b[0], x[1], b[1]) for x, b in zip(s, s[1:]) if b[1] > x[1] + epsilon]


def fuel_crossed(events, line=LOW_FUEL_LINE_PCT):
    """The first stored fuel reading below the line, or None."""
    for t, v in fuel_series(events):
        if v < line:
            return t
    return None


def judge_rule_gone(health):
    """ruleHealth after the disable: the low-fuel rule is not running and its siblings still are (the disable was aimed)."""
    tokens = {h["ruleToken"]: h.get("status") for h in health}
    if RULE_TOKEN in tokens:
        return False, f"{RULE_TOKEN} is still in ruleHealth ({tokens[RULE_TOKEN]})"
    gone = [t for t in OTHER_RULE_TOKENS if t not in tokens]
    if gone:
        return False, f"{RULE_TOKEN} is gone, but so are {gone}: the publish removed more than the one rule"
    return True, f"{RULE_TOKEN} is not running; {', '.join(OTHER_RULE_TOKENS)} still are"


def judge_rule_running(health):
    tokens = {h["ruleToken"]: h.get("status") for h in health}
    if tokens.get(RULE_TOKEN) == "ACTIVE":
        return True, f"{RULE_TOKEN} is ACTIVE in ruleHealth"
    return False, f"{RULE_TOKEN} is {tokens.get(RULE_TOKEN, 'absent')} in ruleHealth"


def definition_hash(definition):
    return hashlib.sha256((definition or "").encode("utf-8")).hexdigest()[:16]


def judge_rule_restored(state, profile, health):
    """
    The rule is as it was: the draft's enabled flag and definition, the profile's active version, and the engine
    running it again. `state` is what rule-state.json recorded BEFORE the control touched anything.
    """
    problems = []
    rules = {r["token"]: r for r in profile.get("detectionRules", [])}
    rule = rules.get(RULE_TOKEN)
    if rule is None:
        problems.append("the rule is gone from the draft")
    else:
        if rule["enabled"] != state["enabled"]:
            problems.append(f"the draft rule's enabled is {rule['enabled']}, was {state['enabled']}")
        if definition_hash(rule.get("definition")) != state["definitionHash"]:
            problems.append("the draft rule's definition changed")
    for tok, was in state.get("others", {}).items():
        now = rules.get(tok)
        if now is None or now["enabled"] != was:
            problems.append(f"{tok} is {None if now is None else now['enabled']}, was {was}")
    if profile.get("activeVersion") != state["activeVersion"]:
        problems.append(f"the active version is {profile.get('activeVersion')}, was {state['activeVersion']}")
    if state["enabled"]:
        ok, detail = judge_rule_running(health)
        if not ok:
            problems.append(detail)
    if problems:
        return False, "; ".join(problems)
    return True, (f"{RULE_TOKEN} enabled={state['enabled']} on the draft, active version {state['activeVersion']} "
                  f"again, definition unchanged, running in ruleHealth")


def judge_replicas(deploy, want, line):
    """`line` is one line of `live-env.sh replicas <deploy>`: name desired ready."""
    parts = line.split()
    if len(parts) < 2 or parts[0] != deploy:
        return False, f"cannot read the replicas of {deploy}: '{line.strip()}'"
    desired = int(parts[1])
    ready = int(parts[2]) if len(parts) > 2 else 0
    return desired == want and ready == want, f"{deploy}: desired {desired}, ready {ready}, wanted {want}"


def classify_commands(commands):
    """
    The soak's view of the commands the platform holds for the fleet: the REACT goto-refuels, whatever is still
    in flight and whatever ended otherwise than SUCCESSFUL (a command the run never asked to fail).
    """
    refuels = [c for c in commands if c["name"] == "goto-refuel"]
    non_terminal = [c for c in commands if c["status"] not in a.TERMINAL]
    failed = [c for c in commands if c["status"] in a.TERMINAL and c["status"] != "SUCCESSFUL"]
    return {"refuels": refuels, "non_terminal": non_terminal, "failed": failed}


def judge_soak_commands(commands, prepared_cycles):
    """Items for the platform's account of the soak's commands."""
    k = classify_commands(commands)
    items = []
    bad = [c for c in k["refuels"] if c["status"] != "SUCCESSFUL"]
    ok = len(k["refuels"]) >= prepared_cycles > 0 and not bad
    items.append(a.Item("soak-refuels-successful", "every goto-refuel the platform created reached SUCCESSFUL, one for each low-fuel cycle prepared", ok,
                        f"{len(k['refuels'])} goto-refuel(s) for {prepared_cycles} prepared cycle(s); "
                        + (f"not SUCCESSFUL: {[(c['deviceToken'][-6:], c['status']) for c in bad[:5]]}" if bad else "all SUCCESSFUL")))
    items.append(a.Item("soak-no-open-commands", "no command is left non-terminal at the end", not k["non_terminal"],
                        f"{len(k['non_terminal'])} non-terminal" + (f": {[(c['name'], c['status']) for c in k['non_terminal'][:5]]}" if k["non_terminal"] else "")))
    items.append(a.Item("soak-no-failed-commands", "no unexplained command failure (every command the platform created ended SUCCESSFUL)", not k["failed"],
                        f"{len(commands)} command(s), {len(k['failed'])} ended otherwise"
                        + (f": {[(c['name'], c['status'], a.redact(c.get('error') or '')[:60]) for c in k['failed'][:5]]}" if k["failed"] else "")))
    return items


def cadence(samples):
    """Per device, per sample kind: how many were emitted and the rate actually achieved (first to last occurred time)."""
    by = {}
    for s in samples:
        by.setdefault(s["device"], {}).setdefault(s["kind"], []).append(s["_time"])
    out = {}
    for dev, kinds in by.items():
        out[dev] = {}
        for kind, ts in kinds.items():
            span = (max(ts) - min(ts)).total_seconds()
            out[dev][kind] = {"count": len(ts), "hz": round((len(ts) - 1) / span, 3) if len(ts) > 1 and span > 0 else None}
    return out


def parse_memory(text):
    """memory.tsv: epoch seconds, working set MiB. Returns [(epoch, mib)], skipping anything unreadable."""
    rows = []
    for line in text.splitlines():
        parts = line.strip().split("\t")
        if len(parts) < 2:
            continue
        try:
            rows.append((int(parts[0]), int(parts[1])))
        except ValueError:
            continue
    return rows


def memory_summary(rows):
    if not rows:
        return {"samples": 0}
    mibs = [m for _, m in rows]
    span_min = (rows[-1][0] - rows[0][0]) / 60.0
    return {"samples": len(rows), "firstMiB": mibs[0], "lastMiB": mibs[-1], "maxMiB": max(mibs), "minMiB": min(mibs),
            "growthMiBPerMin": round((mibs[-1] - mibs[0]) / span_min, 2) if span_min > 0 else None}


def soak_section(soak):
    """The SUMMARY section for soak.json: a table of what the run measured."""
    f, lag = soak.get("frame") or {}, soak.get("lag") or {}
    mem = soak.get("memory") or {}
    sessions = soak.get("sessions") or {}
    gaps = soak.get("gaps") or {}
    lines = ["### Soak measurements", "",
             f"- **Reference hardware**: {a.redact(soak.get('referenceHardware') or 'not recorded')}",
             f"- **Duration**: {soak.get('minutes')} minute(s) of Live, 19 devices", "",
             "| Measurement | Value |", "|---|---|"]

    def ms(v):
        return "n/a" if v is None else f"{v:.1f} ms"

    lines.append(f"| Frame time p50 / p95 / p99 / max | {ms(f.get('p50Ms'))} / {ms(f.get('p95Ms'))} / {ms(f.get('p99Ms'))} / {ms(f.get('maxMs'))} ({f.get('n', 0)} frames) |")
    lines.append(f"| Observation lag p50 / p95 / max (observed receivedAt - occurredTime) | {ms(lag.get('p50Ms'))} / {ms(lag.get('p95Ms'))} / {ms(lag.get('maxMs'))} ({lag.get('n', 0)} values) |")
    if mem.get("samples"):
        lines.append(f"| Player working set first / last / max | {mem['firstMiB']} / {mem['lastMiB']} / {mem['maxMiB']} MiB, {mem['growthMiBPerMin']} MiB/min over {mem['samples']} one-minute samples |")
    else:
        lines.append("| Player working set | no samples (powershell was not reachable) |")
    lines.append(f"| Device sessions min / max (expected {sessions.get('expected')}) | {sessions.get('min')} / {sessions.get('max')} |")
    lines.append(f"| Longest gap of any device out of Observed | {gaps.get('maxSeconds')} s ({gaps.get('device')}); {len(gaps.get('over15s') or [])} over 15 s |")
    lines.append(f"| Dropped samples / send errors / reconnect episodes | {soak.get('dropped')} / {soak.get('sendErrors')} / {soak.get('reconnectEpisodes')} |")
    cmds = soak.get("commands") or {}
    lines.append(f"| Commands the platform created / goto-refuel / not SUCCESSFUL | {cmds.get('total')} / {cmds.get('refuels')} / {cmds.get('failed')} |")
    lines += ["", "| Device | Measurement publishes (Hz achieved) | Location publishes (Hz achieved) |", "|---|---|---|"]
    for dev in sorted(soak.get("cadence") or {}):
        row = soak["cadence"][dev]

        def cell(kind, row=row):
            c = row.get(kind)
            return "none" if not c else f"{c['count']} ({c['hz']} Hz)"

        lines.append(f"| {dev} | {cell('measurement')} | {cell('location')} |")
    cycles = soak.get("cycles") or []
    if cycles:
        lines += ["", "| Cycle | Machine | Prepared at | Device log |", "|---|---|---|---|"]
        for c in cycles:
            lines.append(f"| {c.get('index')} | {c.get('machine')} | {c.get('preparedAt')} | {a.redact(str(c.get('outcome')))} |")
    per = soak.get("perMinute") or []
    if per:
        lines += ["", "| Minute | Sessions | Observed | Presence active | Reconnecting | Dropped | Frame p95 | Bay |", "|---|---|---|---|---|---|---|---|"]
        for m in per:
            lines.append(f"| {m.get('minute')} | {m.get('sessions')} | {m.get('observed')} | {m.get('presenceActive')} | {m.get('reconnecting')} | {m.get('dropped')} | "
                         f"{ms(m.get('frameP95Ms'))} | {m.get('bayHolder') or 'free'}{' +' + str(m.get('bayWaiting')) if m.get('bayWaiting') else ''} |")
    return lines


# ---------------------------------------------------------------------------------------------
# platform I/O for the controls
# ---------------------------------------------------------------------------------------------


def profile_state(platform, profile=PROFILE_TOKEN):
    rows = platform.gql("device-management",
                        "query($t:[String!]!){deviceProfilesByToken(tokens:$t){token activeVersion detectionRules{token enabled definition}}}",
                        {"t": [profile]})["deviceProfilesByToken"]
    if not rows:
        raise a.PlatformError(f"no device profile {profile}")
    return rows[0]


def rule_health(platform, profile=PROFILE_TOKEN):
    return platform.gql("event-processing", "query($t:String!){ruleHealth(profileToken:$t){ruleToken name status fireCount}}", {"t": profile})["ruleHealth"]


def set_rule_enabled(platform, token, enabled):
    return platform.gql("device-management", "mutation($t:String!,$r:DetectionRuleUpdateRequest!){updateDetectionRule(token:$t,request:$r){token enabled}}",
                        {"t": token, "r": {"enabled": enabled}})["updateDetectionRule"]


def publish_profile(platform, profile, label, description):
    return platform.gql("device-management", "mutation($t:String!,$l:String,$d:String){publishDeviceProfile(token:$t,label:$l,description:$d){version}}",
                        {"t": profile, "l": label, "d": description})["publishDeviceProfile"]


def rollback_profile(platform, profile, version):
    return platform.gql("device-management", "mutation($t:String!,$v:Int!){rollbackDeviceProfile(token:$t,version:$v){token activeVersion}}",
                        {"t": profile, "v": version})["rollbackDeviceProfile"]


def alarms_of(platform, device_token):
    return platform.gql("device-management",
                        "query($c:AlarmSearchCriteria!){alarms(criteria:$c){results{token alarmKey state raisedTime clearedTime}}}",
                        {"c": {"pageNumber": 1, "pageSize": 50, "originatorType": "device", "originator": device_token}})["alarms"]["results"]


def commands_since(platform, device_token, since, page_size=100):
    """Every command the platform created for the device at or after `since` (the search is newest first)."""
    out = []
    for page in range(1, 50):
        rows = platform.gql("command-delivery",
                            "query($c:CommandSearchCriteria!){commands(criteria:$c){results{%s}}}" % platform.CMD_FIELDS,
                            {"c": {"pageNumber": page, "pageSize": page_size, "deviceToken": device_token}})["commands"]["results"]
        out += [r for r in rows if r.get("queuedTime") and a.parse_time(r["queuedTime"]) >= since]
        if len(rows) < page_size or any(not r.get("queuedTime") or a.parse_time(r["queuedTime"]) < since for r in rows):
            break
    return out


def cancel_command(platform, token):
    return platform.gql("command-delivery", "mutation($t:String!){cancelCommand(token:$t){token status}}", {"t": token})["cancelCommand"]


def _write_json(path, doc):
    tmp = path + ".tmp"
    with open(tmp, "w", encoding="utf-8") as f:
        json.dump(doc, f, indent=2)
    os.replace(tmp, path)


def _read_json(path):
    try:
        with open(path, encoding="utf-8") as f:
            return json.load(f)
    except (OSError, ValueError):
        return None


def utc_now():
    return datetime.datetime.now(datetime.timezone.utc)


# ---------------------------------------------------------------------------------------------
# rule-disabled
# ---------------------------------------------------------------------------------------------


def record_rule_state(platform, state_path):
    """Read the profile and write the undo record BEFORE anything is changed. Returns (state, problem)."""
    profile = profile_state(platform)
    rules = {r["token"]: r for r in profile["detectionRules"]}
    rule = rules.get(RULE_TOKEN)
    if rule is None:
        return None, f"{RULE_TOKEN} is not a rule of {PROFILE_TOKEN}"
    if not rule["enabled"]:
        return None, f"{RULE_TOKEN} is already disabled: the control would have nothing to restore to"
    if profile["activeVersion"] is None:
        return None, f"{PROFILE_TOKEN} has never been published"
    state = {"profile": PROFILE_TOKEN, "rule": RULE_TOKEN, "enabled": rule["enabled"], "definitionHash": definition_hash(rule["definition"]),
             "others": {t: rules[t]["enabled"] for t in OTHER_RULE_TOKENS if t in rules}, "activeVersion": profile["activeVersion"],
             "published": None, "restored": False, "recordedAt": a.iso(utc_now())}
    _write_json(state_path, state)
    return state, None


def restore_rule(platform, state_path, timeout=90.0, quiet_seconds=15.0, device="SP-HL-0006"):
    """
    Put the rule back exactly as the undo record says and verify it. Idempotent: a restored record does nothing.
    Returns the items (empty when there was nothing to do).
    """
    state = _read_json(state_path)
    if not state or state.get("restored"):
        return []
    items = []
    restored_at = utc_now()
    try:
        set_rule_enabled(platform, state["rule"], state["enabled"])
        profile = profile_state(platform, state["profile"])
        if profile["activeVersion"] != state["activeVersion"]:
            rollback_profile(platform, state["profile"], state["activeVersion"])
        end = time.time() + timeout
        ok, detail = False, "not checked"
        while True:
            profile = profile_state(platform, state["profile"])
            health = rule_health(platform, state["profile"])
            ok, detail = judge_rule_restored(state, profile, health)
            if ok or time.time() > end:
                break
            time.sleep(3)
        items.append(a.Item("rule-restored", "the low-fuel rule is back as it was (draft, active version, running) and verified", ok, detail))
    except a.PlatformError as e:
        items.append(a.Item("rule-restored", "the low-fuel rule is back as it was (draft, active version, running) and verified", False,
                            f"the restore could not be completed: {e}. RUN: phase_a_check.py rule-restore --state {state_path}"))
        return items
    if not items[-1].ok:
        return items
    state["restored"] = True
    _write_json(state_path, state)
    # the engine is running the rule again: if a sample is still arriving, the platform will react and a command appears
    try:
        tokens = platform.device_tokens([device])
        time.sleep(quiet_seconds)
        stray = _queued_since(commands_since(platform, tokens[device], restored_at - datetime.timedelta(seconds=2)), restored_at - datetime.timedelta(seconds=2))
        cancelled = []
        for c in stray:
            if c["status"] not in a.TERMINAL:
                try:
                    cancel_command(platform, c["token"])
                    cancelled.append(c["token"])
                except a.PlatformError:
                    pass
        items.append(a.Item("rule-restore-quiet", f"restoring the rule made the platform send {device} nothing", not stray,
                            "no command appeared after the restore" if not stray else f"{len(stray)} command(s) appeared after the restore ({len(cancelled)} cancelled): the player was still publishing"))
    except (a.PlatformError, KeyError) as e:
        items.append(a.Item("rule-restore-quiet", f"restoring the rule made the platform send {device} nothing", None, f"not checked: {e}"))
    return items


def run_rule_disabled(args, platform, since_dt):
    items, report = [], {}
    d = args.dir
    state_path = os.path.join(d, RULE_STATE)
    state = None
    final = None
    try:
        a.say("control rule-disabled: waiting for the fleet to be observed and the probe to wait for the go")
        first = a.wait_result(d, lambda r: r.get("phase") == "awaiting-go", args.reach_timeout)
        if first is None or first.get("final"):
            items.append(a.Item("fleet-up", "the player's fleet came up and waits for the rule to be disabled", False,
                                "the player wrote no result" if first is None else f"the player finished early: {first.get('error') or 'fleet never observed'}"))
            return items, report
        tokens = platform.device_tokens([a.REFUEL_MACHINE])
        token = tokens.get(a.REFUEL_MACHINE)
        if not token:
            items.append(a.Item("platform-device", f"{a.REFUEL_MACHINE} exists on the platform", False, "not found"))
            return items, report

        state, problem = record_rule_state(platform, state_path)
        if problem:
            items.append(a.Item("rule-recorded", "the rule's present state was read and recorded before anything changed", False, problem))
            return items, report
        items.append(a.Item("rule-recorded", "the rule's present state was read and recorded before anything changed", True,
                            f"{RULE_TOKEN} enabled, {PROFILE_TOKEN} active version {state['activeVersion']}; undo record {RULE_STATE}"))
        ok, detail = judge_rule_running(rule_health(platform))
        items.append(a.Item("rule-running-before", "the engine runs the low-fuel rule before the control (the positive baseline)", ok, detail))

        a.say("disabling the rule on the draft and publishing")
        set_rule_enabled(platform, RULE_TOKEN, False)
        pub = publish_profile(platform, PROFILE_TOKEN, "acceptance rule-disabled control", "disables sp-rule-lowfuel for one acceptance control; rolled back after it")
        state["published"] = pub["version"]
        _write_json(state_path, state)
        end = time.time() + 90
        ok, detail = False, "not checked"
        while time.time() < end:
            ok, detail = judge_rule_gone(rule_health(platform))
            if ok:
                break
            time.sleep(3)
        items.append(a.Item("rule-disabled-effective", "the engine no longer runs the low-fuel rule (ruleHealth), and still runs the other two", ok, detail))
        if not ok:
            return items, report

        go_at = utc_now()
        with open(os.path.join(d, GO), "w") as f:
            f.write(a.iso(go_at) + "\n")
        a.say("the rule is off: telling the player to prepare the low tank")
        final = a.wait_result(d, lambda r: bool(r.get("final")), args.finish_timeout + 420)
        if final is None or not final.get("final"):
            items.append(a.Item("control-final", "the control run finished", False, "no final result"))
            return items, report
        report["probe"] = final
        items.append(a.Item("control-labelled", "the run is labelled as a control in its result file", final.get("control") is True and "CONTROL" in final.get("run", ""),
                            f"run = {final.get('run')}"))
        for pi in final.get("items", []):
            if pi["pass"] is not None:
                items.append(a.Item("probe:" + pi["id"], "player probe: " + pi["description"], pi["pass"], pi.get("detail", "")))

        # the platform's account of the window (the rule is still off here)
        time.sleep(args.ingest_grace)
        window_start = go_at
        ok, detail = judge_no_commands(commands_since(platform, token, window_start), window_start)
        items.append(a.Item("platform-no-command", f"the platform created no command for {a.REFUEL_MACHINE} while its tank crossed 15 %", ok, detail))
        ok, detail = judge_no_alarm(alarms_of(platform, token), ALARM_KEY, window_start)
        items.append(a.Item("platform-no-alarm", f"the platform raised no {ALARM_KEY} alarm for {a.REFUEL_MACHINE}", ok, detail))
        fuel = [e for e in platform.events("measurement", token, a.iso(window_start)) if e["name"] == "fuel_pct"]
        crossed = fuel_crossed(fuel)
        items.append(a.Item("platform-fuel-crossed", "the platform stored a fuel reading below 15 % (the condition the rule would have fired on was real)",
                            crossed is not None, f"{len(fuel)} fuel samples; first below 15 %: {a.iso(crossed) if crossed else 'never'}"))
        rises = fuel_rises(fuel)
        items.append(a.Item("platform-fuel-never-rose", f"{a.REFUEL_MACHINE}'s stored fuel never rises in the window", not rises,
                            f"{len(fuel)} fuel samples, {len(rises)} rise(s)" + (f"; first {a.iso(rises[0][0])} {rises[0][1]} -> {rises[0][2]}" if rises else "")))
    finally:
        # the player is finished (or never got the go); let its sessions go, then put the platform back
        if os.path.exists(state_path):
            a.wait_result(d, lambda r: bool(r.get("final")), 60)
            time.sleep(8)
            for it in restore_rule(platform, state_path):
                items.append(it)
    return items, report


# ---------------------------------------------------------------------------------------------
# observer-outage
# ---------------------------------------------------------------------------------------------


def _live_env(live_env, *argv):
    p = subprocess.run([live_env, *argv], check=False, capture_output=True, text=True)
    return p.returncode, (p.stdout or "").strip()


def read_replicas(live_env, deploy):
    rc, out = _live_env(live_env, "replicas", deploy)
    return out if rc == 0 else ""


def wait_replicas(live_env, deploy, want, timeout):
    end = time.time() + timeout
    ok, detail = False, "not read"
    while True:
        ok, detail = judge_replicas(deploy, want, read_replicas(live_env, deploy))
        if ok or time.time() > end:
            return ok, detail
        time.sleep(3)


def restore_scale(live_env, pending_path, timeout=180.0):
    """Put back every deployment the pending file names; remove the file only once each is verified. Returns items."""
    try:
        with open(pending_path, encoding="utf-8") as f:
            lines = [ln.rstrip("\n").split("\t") for ln in f if ln.strip()]
    except OSError:
        return []
    items, left = [], []
    for deploy, n in lines:
        _live_env(live_env, "scale", deploy, n)
        ok, detail = wait_replicas(live_env, deploy, int(n), timeout)
        items.append(a.Item("replicas-restored", f"{deploy} is back at its original replica count and ready", ok, detail))
        if not ok:
            left.append(f"{deploy}\t{n}")
    if left:
        with open(pending_path, "w", encoding="utf-8") as f:
            f.write("\n".join(left) + "\n")
    else:
        os.remove(pending_path)
    return items


def judge_outage_bound(t_up, back_live_text, bound=OUTAGE_RECOVERY_BOUND_S):
    """The probe's own clock for the stream coming back, against when this script put the deployment back."""
    if not back_live_text:
        return False, "the measurement stream never came back to Live"
    took = (a.parse_time(back_live_text) - t_up).total_seconds()
    return took <= bound, f"the stream was Live again {took:.1f} s after the deployment was restored (bound {bound:.0f} s)"


def run_observer_outage(args, platform, since_dt):
    items, report = [], {}
    d = args.dir
    live_env = args.live_env
    deploy = args.outage_deploy
    pending = os.path.join(d, SCALE_PENDING)
    t_down = t_up = None
    try:
        a.say("control observer-outage: waiting for the fleet to be observed and steady")
        first = a.wait_result(d, lambda r: bool((r.get("observerOutage") or {}).get("steadyAt")), args.reach_timeout)
        if first is None or first.get("final"):
            items.append(a.Item("fleet-up", "the player's fleet came up and was steady before the outage", False,
                                "the player wrote no result" if first is None else f"the player finished early: {first.get('error') or 'fleet never steady'}"))
            return items, report
        line = read_replicas(live_env, deploy)
        parts = line.split()
        if len(parts) < 2 or int(parts[1]) < 1:
            items.append(a.Item("replicas-read", f"{deploy}'s replica count was read from the isolated cluster", False, f"'{line}'"))
            return items, report
        n0 = int(parts[1])
        items.append(a.Item("replicas-read", f"{deploy}'s replica count was read from the isolated cluster", True, f"{n0} replica(s)"))
        with open(pending, "w", encoding="utf-8") as f:
            f.write(f"{deploy}\t{n0}\n")
        a.say(f"scaling {deploy} to 0 for {args.outage_seconds:g} s")
        t_down = utc_now()
        rc, _ = _live_env(live_env, "scale", deploy, "0")
        ok, detail = wait_replicas(live_env, deploy, 0, 60) if rc == 0 else (False, "live-env.sh scale failed")
        items.append(a.Item("outage-started", f"{deploy} really is at zero replicas during the window", ok, detail))
        time.sleep(args.outage_seconds)
        a.say(f"restoring {deploy} to {n0}")
        t_up = utc_now()
        for it in restore_scale(live_env, pending, 180):
            items.append(it)
        final = a.wait_result(d, lambda r: bool(r.get("final")), args.finish_timeout + 240)
        if final is None or not final.get("final"):
            items.append(a.Item("control-final", "the control run finished", False, "no final result"))
            return items, report
        report["probe"] = final
        items.append(a.Item("control-labelled", "the run is labelled as a control in its result file", final.get("control") is True and "CONTROL" in final.get("run", ""),
                            f"run = {final.get('run')}"))
        for pi in final.get("items", []):
            if pi["pass"] is not None:
                items.append(a.Item("probe:" + pi["id"], "player probe: " + pi["description"], pi["pass"], pi.get("detail", "")))
        ok, detail = judge_outage_bound(t_up, (final.get("observerOutage") or {}).get("backLiveAt"))
        items.append(a.Item("outage-recovery-bound", "the stream returned to Live within the bound of the deployment coming back", ok, detail))
        report["window"] = {"down": a.iso(t_down), "up": a.iso(t_up)}
        # the devices kept publishing: what they sent while event-management was away is stored once it is back
        try:
            with open(os.path.join(d, a.SAMPLES), encoding="utf-8") as f:
                emitted, bad = a.parse_samples(f.read())
        except OSError as e:
            items.append(a.Item("outage-samples-stored", "samples published during the outage are stored once it ends", False, "no emitted-sample log: " + str(e)))
            return items, report
        inside = [s for s in emitted if t_down <= s["_time"] <= t_up]
        time.sleep(args.ingest_grace)
        tokens = platform.device_tokens()
        end = time.time() + args.storage_timeout
        result = None
        while True:
            stored_m, stored_l = [], []
            for ext, tok in tokens.items():
                stored_m += platform.events("measurement", tok, a.iso(t_down))
                if ext != "SP-PL-0001":
                    stored_l += platform.events("location", tok, a.iso(t_down))
            result = a.match_samples(inside, stored_m, stored_l)
            if (not result["missing"] and not result["wrong"]) or time.time() > end:
                break
            time.sleep(10)
        items.append(a.Item("outage-samples-stored", "every sample the devices published during the outage is stored once event-management is back",
                            bool(inside) and not result["missing"] and not result["wrong"],
                            f"{len(inside)} samples in the window, {result['checked']} entries checked, {result['matched']} matched, "
                            f"{len(result['missing'])} missing, {len(result['wrong'])} wrong" + (f"; first missing: {result['missing'][0]}" if result["missing"] else "")))
    finally:
        # a pending file means the deployment may still be down: put it back whatever happened above
        for it in restore_scale(live_env, pending, 180):
            items.append(it)
    return items, report


# ---------------------------------------------------------------------------------------------
# soak
# ---------------------------------------------------------------------------------------------


def all_commands(platform, tokens, since):
    out = []
    for tok in tokens.values():
        out += commands_since(platform, tok, since)
    return out


def read_text(path):
    try:
        with open(path, encoding="utf-8", errors="replace") as f:
            return f.read()
    except OSError:
        return ""


def run_soak(args, platform, since_dt):
    items, report = [], {}
    d = args.dir
    minutes = args.soak_minutes
    a.say(f"soak: waiting for the fleet to be observed, then {minutes} minute(s) of Live")
    first = a.wait_result(d, lambda r: (r.get("timing") or {}).get("observedSeconds") is not None, args.reach_timeout)
    if first is None or first.get("final") and (first.get("timing") or {}).get("observedSeconds") is None:
        items.append(a.Item("fleet-up", "the player's fleet came up", False,
                            "the player wrote no result" if first is None else f"the player finished early: {first.get('error') or 'fleet never observed'}"))
        return items, report
    tokens = platform.device_tokens()
    missing = [i for i in a.DEVICE_IDS if i not in tokens]
    items.append(a.Item("platform-devices", "all 19 scene devices exist on the platform", not missing, f"{len(tokens)}/19" + (f"; missing {missing}" if missing else "")))
    if missing:
        return items, report

    ended = a.wait_result(d, lambda r: bool((r.get("soak") or {}).get("ended")), minutes * 60 + 420, step=5.0)
    if ended is None or not (ended.get("soak") or {}).get("ended"):
        items.append(a.Item("soak-ended", "the soak ran its time and reached its end state", False, "the probe never reported the end of the soak"))
        return items, report
    probe_soak = ended["soak"]
    prepared = sum(1 for c in probe_soak.get("cycles", []) if c.get("prepared"))
    a.say("the soak is over: asking the platform about every command (the player is still up)")
    commands = all_commands(platform, tokens, since_dt)
    items += judge_soak_commands(commands, prepared)
    alarm_left = [ext for ext in a.MACHINE_IDS
                  if any(x.get("alarmKey") == ALARM_KEY and x.get("state") == "ACTIVE" for x in alarms_of(platform, tokens[ext]))]
    items.append(a.Item("soak-no-active-lowfuel", "no low-fuel alarm is still ACTIVE at the end (every cycle's alarm cleared)", not alarm_left,
                        "none active" if not alarm_left else f"still ACTIVE: {alarm_left}"))
    presence = platform.gql("device-state", "query($t:[String!]!){deviceStatesByDeviceToken(deviceTokens:$t){deviceToken active}}", {"t": list(tokens.values())})["deviceStatesByDeviceToken"]
    active = sum(1 for p in presence if p.get("active"))
    items.append(a.Item("soak-presence-19", "the platform's own presence shows all 19 devices active (no leaked or missing session)", active == 19, f"{active}/19 active"))

    with open(os.path.join(d, a.FINISH), "w") as f:
        f.write("done\n")
    final = a.wait_result(d, lambda r: bool(r.get("final")), args.finish_timeout + 120)
    if final is None or not final.get("final"):
        items.append(a.Item("probe-final", "the player's probe finished", False, "no final result within the timeout"))
        return items, report
    for pi in final.get("items", []):
        items.append(a.Item("probe:" + pi["id"], "player probe: " + pi["description"], pi["pass"], pi.get("detail", "")))
    report["probe"] = {k: v for k, v in final.items() if k != "deviceLog"}

    emitted, bad = a.parse_samples(read_text(os.path.join(d, a.SAMPLES)))
    cad = cadence(emitted)
    mem = memory_summary(parse_memory(read_text(os.path.join(d, MEMORY_TSV))))
    soak = dict(final.get("soak") or {})
    soak.update({"minutes": minutes, "referenceHardware": read_text(os.path.join(d, HARDWARE_TXT)).strip(), "cadence": cad,
                 "memory": mem, "commands": {"total": len(commands), "refuels": len(classify_commands(commands)["refuels"]), "failed": len(classify_commands(commands)["failed"])}})
    _write_json(os.path.join(d, SOAK_JSON), soak)
    silent = [i for i in a.DEVICE_IDS if not cad.get(i, {}).get("measurement", {}).get("count")]
    items.append(a.Item("soak-cadence", "every device published measurements through the run (the rate achieved is in the soak table)", not silent,
                        f"{len(emitted)} samples logged, {bad} unreadable; devices with none: {silent or 'none'}"))

    a.say("checking that every emitted sample is stored")
    time.sleep(args.ingest_grace)
    deadline = time.time() + args.storage_timeout + 120
    result = None
    while True:
        stored_m, stored_l = [], []
        for ext, tok in tokens.items():
            stored_m += platform.events("measurement", tok, a.iso(since_dt))
            if ext != "SP-PL-0001":
                stored_l += platform.events("location", tok, a.iso(since_dt))
        result = a.match_samples(emitted, stored_m, stored_l)
        if (not result["missing"] and not result["wrong"]) or time.time() > deadline:
            break
        time.sleep(10)
    items.append(a.Item("storage-every-sample", "every emitted sample is stored, matched by device and occurredTime (and key and value)",
                        bool(emitted) and not result["missing"] and not result["wrong"] and bad == 0,
                        f"{len(emitted)} samples, {result['checked']} entries checked, {result['matched']} matched, {len(result['missing'])} missing, "
                        f"{len(result['wrong'])} wrong value, {bad} unreadable lines" + (f"; first missing: {result['missing'][0]}" if result["missing"] else "")))
    return items, report
