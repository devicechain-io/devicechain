#!/usr/bin/env python3
# Copyright The DeviceChain Authors
# SPDX-License-Identifier: Apache-2.0
"""
video_capture.py -- records the console shots of a video's shot list, unattended, one 1080p mp4 per shot.

    video_capture.py check  <shotlist.yaml>                       validate the shot list (no browser, no credentials read)
    video_capture.py record <shotlist.yaml> --out <dir> [--shots a,b] [--base-url URL]

Schema and conventions: README.md next to this file. Output: <out>/<shot id>.mp4 (H.264, yuv420p, 1920x1080, 30 or 60 fps),
<out>/manifest.json (per-shot UTC start/end, trimmed duration, per-step marks in seconds from the clip start, so an edit can line a
clip up with its voice-over line) and, for a shot that could not complete, <out>/failures/<shot id>.png (a screenshot of the console as
it was, masks applied). Exit status: 0 all recorded, 1 a shot failed, 2 the shot list or the setup is wrong, 3 a secret reached the page.

How a clip is made: the page's frames come from the browser's screencast (a frame whenever the page repaints, with its timestamp) and
ffmpeg renders them at a constant frame rate, so motion is as smooth as the page was. A headless browser draws no cursor, so one is
injected (a pointer that glides to each target, with a click ring).

Secrets. The sign-in credential is read IN THIS PROCESS (a sim handshake record, the instance's superuser Secret through kubectl, or the
environment), typed into the form and forgotten. It is never printed, logged, written to disk (no profile directory, no trace, no
storage state file) or put in the manifest; every message that leaves this module passes through scrub(). The sign-in is not recorded.
Regions the shot list marks `mask` are blurred in the page itself, so the frames never hold the value; a mask that matches nothing fails
the shot (a mask that silently misses is a leak), and a recorded page whose text contains the password ends the run.

Everything that decides something is a pure function above the I/O (tested by test_video_capture.py).
"""

import argparse
import base64
import datetime
import json
import os
import random
import re
import shutil
import subprocess
import sys
import tempfile
import time
from urllib.parse import urlsplit

EXIT_OK, EXIT_SHOT_FAILED, EXIT_BAD_SETUP, EXIT_SECRET_LEAK = 0, 1, 2, 3

STEP_KINDS = ("goto", "click", "type", "press", "hover", "wait_for", "wait", "scroll", "mark", "signin")
SIGNIN_TARGETS = ("dash",)  # what a `signin` step can sign in to; anything else is refused at `check`
TARGET_KEYS = ("testid", "shot", "role", "name", "text", "label", "placeholder", "selector", "nth", "exact")
SHOT_KEYS = ("id", "title", "line", "start", "steps", "mask", "min_s", "max_s", "trim", "retries", "reduce_motion")
TOP_KEYS = ("version", "video", "base_url", "viewport", "fps", "login", "cursor", "reduce_motion", "locale", "timezone", "shots")
LOGIN_MODES = ("sim", "superuser-secret", "env", "none")
ID_RE = re.compile(r"^[a-z0-9][a-z0-9-]*$")
ANCHOR_RE = re.compile(r"^[A-Za-z0-9_.:-]+$")  # a data-shot / data-testid value: no quote, bracket or backslash can reach a selector


class CaptureError(Exception):
    """A shot (or the setup) could not complete. The message is already scrubbed."""

    def __init__(self, msg, exit_code=EXIT_SHOT_FAILED, unsafe=False):
        super().__init__(msg)
        self.exit_code = exit_code
        self.unsafe = unsafe  # a failure whose screenshot could show the secret the failure is about: none is taken


class SecretLeak(CaptureError):
    def __init__(self, msg):
        super().__init__(msg, EXIT_SECRET_LEAK, unsafe=True)


_SECRETS = []  # every secret read this run, so even the entry point's last message and the manifest can be scrubbed


def scrub_obj(o, secrets):
    """A JSON-able value with every string scrubbed (the manifest carries shot titles, marks and failure reasons)."""
    if isinstance(o, str):
        return scrub(o, secrets)
    if isinstance(o, list):
        return [scrub_obj(x, secrets) for x in o]
    if isinstance(o, dict):
        return {scrub(str(k), secrets): scrub_obj(x, secrets) for k, x in o.items()}
    return o


# ---------------------------------------------------------------------------------------------
# pure parts


def scrub(text, secrets):
    """<text> with every secret value removed. Messages from the browser can echo a typed value; nothing leaves this module unscrubbed."""
    text = str(text)
    for s in secrets:
        if s:
            text = text.replace(s, "***")
    return text


def utc(ts):
    return datetime.datetime.fromtimestamp(ts, datetime.timezone.utc).strftime("%Y-%m-%dT%H:%M:%S.%f")[:-3] + "Z"


def _is_num(x):
    return isinstance(x, (int, float)) and not isinstance(x, bool)


def _check_target(t, where, errs):
    if not isinstance(t, dict) or not t:
        errs.append(f"{where}: a target is a non-empty map (one of testid, shot, role, text, label, placeholder, selector)")
        return
    for k in t:
        if k not in TARGET_KEYS:
            errs.append(f"{where}: unknown target key '{k}'")
    locators = [k for k in ("testid", "shot", "role", "text", "label", "placeholder", "selector") if k in t]
    if len(locators) != 1:
        errs.append(f"{where}: a target names exactly one of testid, shot, role, text, label, placeholder, selector (got {locators or 'none'})")
    if "name" in t and "role" not in t:
        errs.append(f"{where}: 'name' belongs with 'role'")
    for k in ("shot", "testid"):
        if k in t and not (isinstance(t[k], str) and ANCHOR_RE.match(t[k])):
            errs.append(f"{where}: {k} is a plain token (letters, digits, _ . : -)")
    if "nth" in t and not (isinstance(t["nth"], int) and not isinstance(t["nth"], bool) and t["nth"] >= 0):
        errs.append(f"{where}: nth is a non-negative integer")


def validate_step(step, where):
    errs = []
    if not isinstance(step, dict) or len(step) < 1:
        return [f"{where}: a step is a map with one verb ({', '.join(STEP_KINDS)})"]
    verbs = [k for k in step if k in STEP_KINDS]
    if len(verbs) != 1:
        return [f"{where}: a step has exactly one verb of {', '.join(STEP_KINDS)} (got {verbs or list(step)})"]
    verb = verbs[0]
    body = step[verb]
    extra = {"timeout_s", "label", "pause_ms", "pace_ms", "secret", "settle_s", "on_camera"}
    for k in step:
        if k != verb and k not in extra:
            errs.append(f"{where}: unknown step key '{k}'")
    if verb in ("click", "hover", "wait_for"):
        _check_target(body, where + "." + verb, errs)
    elif verb == "goto":
        if not (isinstance(body, str) and body.startswith("/")):
            errs.append(f"{where}: goto takes a route starting with '/'")
    elif verb == "type":
        if not isinstance(body, dict) or "text" not in body or "into" not in body:
            errs.append(f"{where}: type takes {{into: <target>, text: <string>}}")
        else:
            _check_target(body["into"], where + ".type.into", errs)
            if not isinstance(body["text"], str):
                errs.append(f"{where}: type.text is a string")
            for k in body:
                if k not in ("into", "text"):
                    errs.append(f"{where}: unknown type key '{k}'")
    elif verb == "press":
        if not isinstance(body, str) or not body:
            errs.append(f"{where}: press takes a key name such as Escape or Enter")
    elif verb == "wait":
        if not _is_num(body) or body < 0:
            errs.append(f"{where}: wait takes seconds (a non-negative number)")
    elif verb == "scroll":
        if not isinstance(body, dict) or len({"by", "to"} & set(body)) != 1 or set(body) - {"by", "to"}:
            errs.append(f"{where}: scroll takes exactly one of {{by: <px>}} or {{to: <target>}}")
        else:
            if "by" in body and not _is_num(body["by"]):
                errs.append(f"{where}: scroll.by is pixels (a number)")
            if "to" in body:
                _check_target(body["to"], where + ".scroll.to", errs)
    elif verb == "mark":
        if not isinstance(body, str) or not body:
            errs.append(f"{where}: mark takes a name")
    elif verb == "signin":
        if body not in SIGNIN_TARGETS:
            errs.append(f"{where}: signin takes one of {', '.join(SIGNIN_TARGETS)} (got {body!r})")
    if "on_camera" in step:
        if verb != "signin":
            errs.append(f"{where}: on_camera belongs to a signin step")
        elif not isinstance(step["on_camera"], bool):
            errs.append(f"{where}: on_camera is true or false")
    for k in ("timeout_s", "pause_ms", "pace_ms", "settle_s"):
        if k in step and not (_is_num(step[k]) and step[k] >= 0):
            errs.append(f"{where}: {k} is a non-negative number")
    return errs


def validate_signin_steps(steps, where, login):
    """A signin step reads the login source (so `mode: none` cannot carry one), a shot has at most one, and a trimmed (off-camera) one runs
    before the clip starts, so it must be the first step and leave another step to record."""
    errs = []
    idx = [j for j, st in enumerate(steps) if isinstance(st, dict) and "signin" in st]
    if not idx:
        return errs
    if not isinstance(login, dict) or login.get("mode") in (None, "none"):
        errs.append(f"{where}: a signin step needs a login source (login.mode sim, superuser-secret or env), not 'none': the credential has to come from somewhere")
    if len(idx) > 1:
        errs.append(f"{where}: a shot has at most one signin step")
    for j in idx:
        if not steps[j].get("on_camera", False) and (j != 0 or len(steps) < 2):
            errs.append(f"{where}.steps[{j}]: a signin that is not on_camera runs before recording starts, so it is the first step and another step must follow")
    return errs


def validate_mask(m, where):
    errs = []
    if not isinstance(m, dict):
        return [f"{where}: a mask is a map"]
    for k in m:
        if k not in ("selector", "shot", "testid", "text_regex", "required", "style", "name"):
            errs.append(f"{where}: unknown mask key '{k}'")
    locs = [k for k in ("selector", "shot", "testid", "text_regex") if k in m]
    if len(locs) != 1:
        errs.append(f"{where}: a mask names exactly one of selector, shot, testid, text_regex")
    for k in ("shot", "testid"):
        if k in m and not (isinstance(m[k], str) and ANCHOR_RE.match(m[k])):
            errs.append(f"{where}: {k} is a plain token (letters, digits, _ . : -)")
    if "text_regex" in m:
        try:
            re.compile(m["text_regex"])
        except (re.error, TypeError):
            errs.append(f"{where}: text_regex does not compile")
    if m.get("style", "blur") not in ("blur", "box"):
        errs.append(f"{where}: style is blur or box")
    if "required" in m and not isinstance(m["required"], bool):
        errs.append(f"{where}: required is true or false")
    return errs


def validate(doc):
    """[problem] for a parsed shot list; empty means valid. Unknown keys are problems: a typo must not silently do nothing."""
    errs = []
    if not isinstance(doc, dict):
        return ["the shot list is a map"]
    for k in doc:
        if k not in TOP_KEYS:
            errs.append(f"unknown top-level key '{k}'")
    if doc.get("version") != 1:
        errs.append("version must be 1")
    if not isinstance(doc.get("video"), str) or not doc.get("video"):
        errs.append("video (the video's id) is required")
    if doc.get("fps", 30) not in (30, 60):
        errs.append("fps is 30 or 60")
    vp = doc.get("viewport", {"width": 1920, "height": 1080})
    if not (isinstance(vp, dict) and vp.get("width") == 1920 and vp.get("height") == 1080):
        errs.append("viewport must be 1920x1080")
    login = doc.get("login")
    if not isinstance(login, dict) or login.get("mode") not in LOGIN_MODES:
        errs.append(f"login.mode is one of {', '.join(LOGIN_MODES)}")
    else:
        need = {"sim": ("sim",), "superuser-secret": ("instance", "email"), "env": ("email_env", "password_env"), "none": ()}[login["mode"]]
        for k in need:
            if not login.get(k):
                errs.append(f"login.{k} is required for mode {login['mode']}")
        if login["mode"] == "none":
            allowed = ("mode",)
        else:
            allowed = ("mode", "sim", "instance", "email", "email_env", "password_env", "namespace", "secret", "kube_context")
        for k in login:
            if k not in allowed:
                errs.append(f"unknown login key '{k}'" + (": a shot list never carries a secret; name where to read it from" if k in ("password", "pass", "token") else ""))
    shots = doc.get("shots")
    if not isinstance(shots, list) or not shots:
        errs.append("shots is a non-empty list")
        return errs
    seen = set()
    for i, s in enumerate(shots):
        w = f"shots[{i}]"
        if not isinstance(s, dict):
            errs.append(f"{w}: a shot is a map")
            continue
        sid = s.get("id")
        w = f"shots[{i}] ({sid})"
        if not isinstance(sid, str) or not ID_RE.match(sid):
            errs.append(f"{w}: id is lowercase kebab-case")
        elif sid in seen:
            errs.append(f"{w}: duplicate id")
        seen.add(sid)
        for k in s:
            if k not in SHOT_KEYS:
                errs.append(f"{w}: unknown shot key '{k}'")
        if not (isinstance(s.get("start"), str) and s["start"].startswith("/")):
            errs.append(f"{w}: start is a route starting with '/'")
        steps = s.get("steps")
        if not isinstance(steps, list) or not steps:
            errs.append(f"{w}: steps is a non-empty list (an empty shot records nothing worth keeping)")
        else:
            for j, st in enumerate(steps):
                errs += validate_step(st, f"{w}.steps[{j}]")
            errs += validate_signin_steps(steps, w, login)
        masks = s.get("mask", []) or []
        if not isinstance(masks, list):
            errs.append(f"{w}: mask is a list")
            masks = []
        for j, m in enumerate(masks):
            errs += validate_mask(m, f"{w}.mask[{j}]")
        tr = s.get("trim", {})
        if not isinstance(tr, dict) or any(k not in ("head_s", "tail_s") or not (_is_num(v) and v >= 0) for k, v in tr.items()):
            errs.append(f"{w}: trim is {{head_s, tail_s}} of non-negative numbers")
        for k in ("min_s", "max_s"):
            if k in s and not (_is_num(s[k]) and s[k] > 0):
                errs.append(f"{w}: {k} is a positive number of seconds")
        if _is_num(s.get("min_s")) and _is_num(s.get("max_s")) and s["min_s"] > s["max_s"]:
            errs.append(f"{w}: min_s exceeds max_s")
        if "retries" in s and not (isinstance(s["retries"], int) and not isinstance(s["retries"], bool) and 0 <= s["retries"] <= 5):
            errs.append(f"{w}: retries is 0 to 5")
    return errs


def target_spec(t):
    """(kind, arg, nth, exact) for a validated target, which the browser layer turns into a Playwright locator."""
    for k in ("testid", "shot", "text", "label", "placeholder", "selector"):
        if k in t:
            return k, t[k], t.get("nth", 0), t.get("exact", k == "text")
    return "role", (t["role"], t.get("name")), t.get("nth", 0), t.get("exact", False)


def regex_arg(s):
    """A name written 're:<pattern>' is a case-insensitive regex; anything else is a literal."""
    if isinstance(s, str) and s.startswith("re:"):
        return re.compile(s[3:], re.I)
    return s


def _css_of(m):
    if "selector" in m:
        return m["selector"]
    if "shot" in m:
        return f'[data-shot="{m["shot"]}"]'
    if "testid" in m:
        return f'[data-testid="{m["testid"]}"]'
    return None


def mask_selectors(specs):
    """[(index, css selector)] of the selector-style masks, in spec order."""
    return [(i, _css_of(m)) for i, m in enumerate(specs) if _css_of(m)]


def mask_css(specs, blur_px=14):
    """The stylesheet that blurs (or boxes) every selector-style mask."""
    rules = []
    for i, m in enumerate(specs):
        sel = _css_of(m)
        if not sel:
            continue
        if m.get("style", "blur") == "box":
            rules.append(f"{sel}{{filter:brightness(0) !important;background:#000 !important;color:transparent !important;user-select:none !important}} {sel} *{{visibility:hidden !important}}")
        else:
            rules.append(f"{sel}{{filter:blur({blur_px}px) !important;user-select:none !important}}")
    return "\n".join(rules)


def typing_delays(text, seed, pace_ms=85):
    """Per-character delays (ms) that read as a person typing: a base pace, jitter, a longer beat after a space or punctuation. Seeded, so a
    re-run types the same way."""
    rng = random.Random(f"{seed}:{text}")
    out = []
    for ch in text:
        d = pace_ms * rng.uniform(0.6, 1.5)
        if ch in " .,-_/:":
            d += pace_ms * rng.uniform(0.5, 1.2)
        out.append(int(d))
    return out


def ease(t):
    return t * t * (3 - 2 * t)


def glide_points(x0, y0, x1, y1, steps=28):
    """Points of a smooth ease-in-out move, the last one exactly the target."""
    pts = [(x0 + (x1 - x0) * ease(i / steps), y0 + (y1 - y0) * ease(i / steps)) for i in range(1, steps + 1)]
    pts[-1] = (x1, y1)
    return pts


def clip_timeline(frames, start, end):
    """[(frame path, duration)] holding each frame until the next arrives and the last until <end>. <frames> is [(timestamp, path)]."""
    frames = sorted(frames)
    before = [f for f in frames if f[0] <= start]
    inside = [f for f in frames if start < f[0] < end]
    keep = ([(start, before[-1][1])] if before else []) + inside
    if not keep and frames:
        keep = [(start, frames[0][1])]  # nothing before the window: the first frame stands in until the next arrives
    out = []
    for i, (t, path) in enumerate(keep):
        nxt = keep[i + 1][0] if i + 1 < len(keep) else end
        out.append((path, max(min(nxt, end) - t, 0.001)))
    return out


def concat_script(timeline):
    """ffmpeg concat-demuxer text for a timeline. The last file is named twice (the demuxer ignores the last duration otherwise)."""

    def q(p):
        return "file '" + p.replace("\\", "/").replace("'", "'\\''") + "'"

    lines = []
    for path, dur in timeline:
        lines.append(q(path))
        lines.append(f"duration {dur:.4f}")
    if timeline:
        lines.append(q(timeline[-1][0]))
    return "\n".join(lines) + "\n"


def trim_window(start, end, trim, min_s=None):
    """(start, end) of the encoded clip after the head/tail trim; a clip shorter than min_s is held to min_s."""
    head, tail = trim.get("head_s", 0.0), trim.get("tail_s", 0.0)
    s, e = start + head, end - tail
    if e <= s:
        raise CaptureError(f"the trim ({head}s head, {tail}s tail) leaves nothing of a {end - start:.2f}s recording")
    if min_s and e - s < min_s:
        e = s + min_s
    return s, e


def shot_entry(shot, start, end, marks, trim, fps, attempts, win):
    ws, we = win
    return {
        "shot": shot["id"],
        "title": shot.get("title", ""),
        "line": shot.get("line", ""),
        "file": shot["id"] + ".mp4",
        "start_utc": utc(ws),
        "end_utc": utc(we),
        "duration_s": round(we - ws, 3),
        "recorded_s": round(end - start, 3),
        "trim": trim,
        "fps": fps,
        "size": "1920x1080",
        "attempts": attempts,
        "marks": [{"at_s": round(t - ws, 3), "what": w} for t, w in marks if ws <= t <= we],
    }


def password_leaked(page_text, secrets):
    """True when any non-trivial secret appears in the text. Values under 4 characters are skipped (a match on three characters proves nothing)."""
    return any(s and len(s) >= 4 and s in page_text for s in secrets)


# ---------------------------------------------------------------------------------------------
# credentials (read in-process; never printed)


def find_credentials(record):
    """(email, password) from a sim handshake record (a parsed JSON value): the keys simEmail and simPassword, wherever they nest."""
    found = {}

    def walk(node):
        if isinstance(node, dict):
            for k, val in node.items():
                if k in ("simEmail", "simPassword") and isinstance(val, str) and val:
                    found.setdefault(k, val)
                else:
                    walk(val)
        elif isinstance(node, list):
            for item in node:
                walk(item)

    walk(record)
    if "simEmail" not in found or "simPassword" not in found:
        raise CaptureError("the sim record has no tenant user (simEmail / simPassword)", EXIT_BAD_SETUP)
    return found["simEmail"], found["simPassword"]


def kubectl_secret_args(login):
    ns = login.get("namespace") or f"dci-{login['instance']}"
    name = login.get("secret") or f"dci-{login['instance']}-superuser"
    ctx = ["--context", login["kube_context"]] if login.get("kube_context") else []
    return ns, name, ["kubectl"] + ctx + ["-n", ns, "get", "secret", name, "-o", "jsonpath={.data.password}"]


def load_credentials(login, env=None, run=subprocess.run, home=None):
    env = os.environ if env is None else env
    mode = login["mode"]
    if mode == "none":
        return "", ""  # a shot list that starts signed out reads no credential at all
    if mode == "sim":
        path = os.path.join(home or os.path.expanduser("~"), ".devicechain", "sims", login["sim"] + ".json")
        try:
            with open(path, encoding="utf-8") as f:
                return find_credentials(json.load(f))
        except (OSError, ValueError):
            # the reason is the file's, never its content
            raise CaptureError(f"the sim record for '{login['sim']}' could not be read (is the sim created? dcctl sim create)", EXIT_BAD_SETUP)
    if mode == "env":
        em, pw = env.get(login["email_env"], ""), env.get(login["password_env"], "")
        if not em or not pw:
            raise CaptureError(f"${login['email_env']} and ${login['password_env']} must both be set", EXIT_BAD_SETUP)
        return em, pw
    # superuser-secret: the Secret dcctl writes at bootstrap, read through kubectl (honours KUBECONFIG), decoded here
    ns, name, cmd = kubectl_secret_args(login)
    try:
        r = run(cmd, capture_output=True, text=True, timeout=30)
    except (OSError, subprocess.SubprocessError) as e:
        raise CaptureError("kubectl could not run: " + type(e).__name__, EXIT_BAD_SETUP)
    if r.returncode != 0:
        # fixed text: neither stdout (the value) nor stderr (arbitrary) is quoted; run the same kubectl command by hand to see why
        raise CaptureError(f"kubectl could not read Secret {ns}/{name} (exit {r.returncode}); KUBECONFIG and the namespace are the usual causes", EXIT_BAD_SETUP)
    try:
        pw = base64.b64decode((r.stdout or "").strip(), validate=True).decode("utf-8")
    except (ValueError, UnicodeDecodeError):
        raise CaptureError(f"Secret {ns}/{name} is not a base64 password", EXIT_BAD_SETUP)
    if not pw:
        raise CaptureError(f"Secret {ns}/{name} has no password", EXIT_BAD_SETUP)
    return login["email"], pw


# ---------------------------------------------------------------------------------------------
# the browser (everything below needs Playwright)

CURSOR_JS = """
(() => {
  if (window.__dcCursor) return;
  const make = () => {
    if (!document.body) { return false; }
    const c = document.createElement('div');
    c.id = '__dc_cursor';
    c.style.cssText = 'position:fixed;left:0;top:0;width:28px;height:28px;margin:-4px 0 0 -4px;z-index:2147483647;pointer-events:none;'
      + 'transform:translate(-100px,-100px);will-change:transform;';
    c.innerHTML = '<svg width="28" height="28" viewBox="0 0 28 28"><path d="M4 2 L4 22 L9.5 17.2 L13.2 25 L16.6 23.5 L12.9 15.8 L20.5 15.8 Z"'
      + ' fill="#ffffff" stroke="#111111" stroke-width="1.6" stroke-linejoin="round"/></svg>';
    const ring = document.createElement('div');
    ring.id = '__dc_ring';
    ring.style.cssText = 'position:fixed;left:0;top:0;width:44px;height:44px;margin:-22px 0 0 -22px;border-radius:50%;z-index:2147483646;'
      + 'pointer-events:none;border:3px solid rgba(80,160,255,0.95);opacity:0;transform:translate(-100px,-100px) scale(0.4);';
    document.body.appendChild(ring);
    document.body.appendChild(c);
    let last = [-100, -100];
    try { const s = sessionStorage.getItem('__dc_cursor'); if (s) last = JSON.parse(s); } catch (e) {}
    c.style.transform = 'translate(' + last[0] + 'px,' + last[1] + 'px)';
    window.__dcCursor = {
      move: (x, y) => { c.style.transform = 'translate(' + x + 'px,' + y + 'px)'; try { sessionStorage.setItem('__dc_cursor', JSON.stringify([x, y])); } catch (e) {} },
      click: (x, y) => {
        ring.style.transition = 'none';
        ring.style.transform = 'translate(' + x + 'px,' + y + 'px) scale(0.4)';
        ring.style.opacity = '1';
        requestAnimationFrame(() => requestAnimationFrame(() => {
          ring.style.transition = 'transform 380ms ease-out, opacity 380ms ease-out';
          ring.style.transform = 'translate(' + x + 'px,' + y + 'px) scale(1.25)';
          ring.style.opacity = '0';
        }));
      },
    };
    document.addEventListener('mousemove', (e) => window.__dcCursor.move(e.clientX, e.clientY), true);
    document.addEventListener('mousedown', (e) => window.__dcCursor.click(e.clientX, e.clientY), true);
    return true;
  };
  if (!make()) { document.addEventListener('DOMContentLoaded', make); }
})();
"""

# Masks and the per-change secret report. __SPEC__ is {css, selectors:[[i,sel]], regexes:[[i,pattern]], blur}.
#  - Element masks are a stylesheet (applied before paint, so frames never hold the value).
#  - Text masks (text_regex) are OPAQUE overlay boxes (a backdrop blur left the text legible in headless Chromium) placed from Range.getClientRects() on every animation frame: the page's own text nodes are
#    never rewritten (a framework that owns them keeps working), and a box exists before the frame that shows the text is painted.
#  - EVERY changed text node, attribute value and input value, and the whole document at load, is sent to the exposed __dcLeak binding, where
#    the recorder compares it with the password. The password is never put in the page; only page text goes out.
#  - Each mask reports (once per document) that it matched something, through the exposed __dcMaskSeen binding.
MASK_JS = """
(() => {
  const spec = __SPEC__;
  const style = document.createElement('style');
  style.id = '__dc_mask_css';
  style.textContent = spec.css + ' input[type=password]{filter:blur(8px) !important}';
  const inject = () => { const r = document.head || document.documentElement; if (r && !style.isConnected) r.appendChild(style); return !!r; };
  if (!inject()) new MutationObserver((_, o) => { if (inject()) o.disconnect(); }).observe(document, {childList: true});

  const seen = new Set();
  // the bindings may not exist yet when the init script first runs: a mask is "seen" only once the report got through, so it is retried
  const report = (i) => { if (!seen.has(i) && typeof window.__dcMaskSeen === 'function') { try { window.__dcMaskSeen(i); seen.add(i); } catch (e) {} } };
  const send = (t) => { if (t && typeof window.__dcLeak === 'function') { try { window.__dcLeak(String(t)); } catch (e) {} } };

  const scan = (root) => {
    for (const [i, sel] of spec.selectors) {
      try { if ((root.querySelector && root.querySelector(sel)) || (root.matches && root.matches(sel))) report(i); } catch (e) {}
    }
  };

  // ---- text masks: one overlay box per match rectangle, repositioned every frame
  const rx = spec.regexes.map(([i, p]) => [i, new RegExp(p, 'g')]);
  let layer = null, pool = [];
  const ensureLayer = () => {
    if (layer && layer.isConnected) return true;
    const host = document.body || document.documentElement;
    if (!host) return false;
    layer = document.createElement('div');
    layer.id = '__dc_mask_layer';
    layer.style.cssText = 'position:fixed;left:0;top:0;width:0;height:0;z-index:2147483645;pointer-events:none;';
    host.appendChild(layer);
    pool = [];
    return true;
  };
  const box = (k, r) => {
    let el = pool[k];
    if (!el) {
      el = document.createElement('div');
      el.style.cssText = 'position:fixed;pointer-events:none;background:#1b3246;border-radius:3px;';
      layer.appendChild(el);
      pool[k] = el;
    }
    el.style.display = 'block';
    el.style.left = (r.left - 2) + 'px'; el.style.top = (r.top - 1) + 'px'; el.style.width = (r.width + 4) + 'px'; el.style.height = (r.height + 2) + 'px';
  };
  const paint = () => {
    if (!rx.length || !ensureLayer()) return;
    let used = 0;
    const root = document.body || document.documentElement;
    const w = document.createTreeWalker(root, NodeFilter.SHOW_TEXT);
    const range = document.createRange();
    while (w.nextNode()) {
      const node = w.currentNode, v = node.nodeValue;
      if (!v || v.length < 2 || node.parentElement && (node.parentElement.closest('#__dc_mask_layer,style,script') )) continue;
      for (const [i, re] of rx) {
        re.lastIndex = 0;
        let m;
        while ((m = re.exec(v)) !== null) {
          if (m[0] === '') { re.lastIndex++; continue; }
          report(i);
          range.setStart(node, m.index); range.setEnd(node, m.index + m[0].length);
          for (const r of range.getClientRects()) box(used++, r);
        }
      }
    }
    for (let k = used; k < pool.length; k++) pool[k].style.display = 'none';
  };

  // ---- the per-change secret report
  // A controlled React password field mirrors what is typed into its `value` attribute. A password-type input draws bullets, never that
  // value, and its blur is enforced separately (the probe), so the mirror is not reported; if the field ever stops being type=password the
  // sweep below reads its value like any other input and the run ends.
  const pwValueAttr = (el, name) => name === 'value' && el.matches && el.matches('input[type=password]');
  const attrs = (el) => { let o = []; for (const a of el.attributes || []) if (!pwValueAttr(el, a.name)) o.push(a.value); return o.join(' '); };
  const inLayer = (n) => layer && (n === layer || (n.nodeType === 1 || n.parentElement) && layer.contains(n.nodeType === 1 ? n : n.parentElement));
  new MutationObserver((muts) => {
    const out = [];
    for (const m of muts) {
      if (inLayer(m.target)) continue;
      if (m.type === 'childList') {
        m.addedNodes.forEach((n) => {
          if (n.nodeType === 1) { out.push(n.textContent || '', attrs(n)); n.querySelectorAll('*').forEach((c) => out.push(attrs(c))); scan(n); }
          else out.push(n.nodeValue || '');
        });
      } else if (m.type === 'characterData') out.push(m.target.nodeValue || '');
      else if (m.type === 'attributes') { if (!pwValueAttr(m.target, m.attributeName)) out.push(m.target.getAttribute(m.attributeName) || ''); }
    }
    send(out.join('\\n'));
    scan(document);
  }).observe(document, {childList: true, subtree: true, characterData: true, attributes: true});
  const values = new WeakMap();
  const sweepValues = () => {
    for (const e of document.querySelectorAll('input:not([type=password]),textarea,select')) {
      const v = e.value || '';
      if (v && values.get(e) !== v) { values.set(e, v); send(v); }
    }
  };
  for (const ev of ['input', 'change', 'focusin']) document.addEventListener(ev, (e) => { if (e.target && e.target.type !== 'password') send(e.target.value); }, true);
  const full = () => { scan(document); send((document.body ? document.body.innerText : '') + '\\n' + document.title); sweepValues(); };
  document.addEventListener('DOMContentLoaded', full);
  window.addEventListener('load', full);
  const frame = () => { paint(); sweepValues(); requestAnimationFrame(frame); };
  requestAnimationFrame(frame);
  full();
})();
"""

QUIET_CSS = ("*,*::before,*::after{animation-duration:0.001s !important;animation-delay:0s !important;animation-iteration-count:1 !important;"
             "transition-duration:0.001s !important;transition-delay:0s !important;scroll-behavior:auto !important;caret-color:transparent !important}")
QUIET_JS = ("(() => { const s = document.createElement('style'); s.id='__dc_quiet'; s.textContent = %s;"
            " const put = () => { const r = document.head || document.documentElement; if (r && !s.isConnected) r.appendChild(s); return !!r; };"
            " if (!put()) new MutationObserver((_, o) => { if (put()) o.disconnect(); }).observe(document, {childList: true}); })();")


class Clip:
    """A recording in progress: the screencast's frames on disk with their timestamps."""

    def __init__(self, session, workdir, fps):
        self.session, self.dir, self.fps = session, workdir, fps
        self.frames, self.n, self.start, self.marks = [], 0, None, []

    def _store(self, ts, data):
        self.n += 1
        path = os.path.join(self.dir, f"f{self.n:06d}.jpg")
        with open(path, "wb") as f:
            f.write(data)
        self.frames.append((ts, path))

    def on_frame(self, params):
        self._store(params["metadata"]["timestamp"], base64.b64decode(params["data"]))
        self.session.send("Page.screencastFrameAck", {"sessionId": params["sessionId"]})

    def grab(self):
        return base64.b64decode(self.session.send("Page.captureScreenshot", {"format": "jpeg", "quality": 95})["data"])

    def begin(self):
        self.session.on("Page.screencastFrame", self.on_frame)
        self.start = time.time()
        self.session.send("Page.startScreencast", {"format": "jpeg", "quality": 95, "maxWidth": 1920, "maxHeight": 1080, "everyNthFrame": 1})
        self._store(self.start, self.grab())  # a still page sends no frame until it repaints

    def note(self, what):
        self.marks.append((time.time(), what))

    def finish(self):
        end = time.time()
        try:
            self.session.send("Page.stopScreencast")
            self._store(end, self.grab())
        finally:
            try:
                self.session.remove_listener("Page.screencastFrame", self.on_frame)
            except Exception:  # noqa: BLE001 - a listener already gone is the state we want
                pass
        return self.start, end


def encode(frames, start, end, dest, fps, threads=4):
    """Renders a clip to <dest> (H.264, yuv420p, constant <fps>, 1920x1080)."""
    timeline = clip_timeline(frames, start, end)
    with tempfile.TemporaryDirectory() as td:
        lst = os.path.join(td, "frames.txt")
        with open(lst, "w", encoding="utf-8") as f:
            f.write(concat_script(timeline))
        cmd = ["ffmpeg", "-y", "-loglevel", "error", "-f", "concat", "-safe", "0", "-i", lst,
               "-vf", f"fps={fps},scale=1920:1080:flags=lanczos,format=yuv420p",
               "-c:v", "libx264", "-preset", "medium", "-crf", "15", "-threads", str(threads), "-r", str(fps), "-movflags", "+faststart", dest]
        subprocess.run(cmd, check=True)


def encode_atomic(frames, start, end, dest, fps):
    """encode() to a temporary name and rename on success: a failed encode leaves no plausible-looking clip behind."""
    tmp = dest + ".part.mp4"
    try:
        encode(frames, start, end, tmp, fps)
        if os.path.getsize(tmp) == 0:
            raise CaptureError("the encoder wrote an empty clip")
        os.replace(tmp, dest)
    finally:
        if os.path.exists(tmp):
            os.remove(tmp)


class Recorder:
    def __init__(self, doc, out_dir, base_url=None, say=None):
        self.doc, self.out = doc, out_dir
        self.base = (base_url or os.environ.get("DC_CONSOLE_URL") or doc.get("base_url") or "http://localhost").rstrip("/")
        self.fps = doc.get("fps", 30)
        self.say = say or (lambda m: print(m, file=sys.stderr, flush=True))
        self.secrets = ()
        self.password = ""
        self.signed_out = False   # login mode none: no credential, no sign-in, a start page on /login is allowed
        self.typed_secrets = []   # `type` steps marked secret: guarded from the moment they are typed (the only credential a signed-out shot has)
        self.pos = (960, 540)
        self.state = None
        self.leaked = False       # set by the page's per-change report (see MASK_JS) the moment the password appears in any text or value
        self.pending = []         # responses seen by the page, checked at each step
        os.makedirs(out_dir, exist_ok=True)
        for d in os.listdir(out_dir):  # frames of a run that was killed
            if d.startswith(".frames-"):
                shutil.rmtree(os.path.join(out_dir, d), ignore_errors=True)
        self.frames_root = os.path.join(out_dir, f".frames-{os.getpid()}")

    def _say(self, m):
        self.say(scrub(m, self.secrets))

    # -- browser lifecycle

    def __enter__(self):
        from playwright.sync_api import sync_playwright

        self.email, self.password = load_credentials(self.doc["login"])
        self.secrets = (self.email, self.password)
        _SECRETS[:] = [x for x in self.secrets if x]
        self.signed_out = self.doc["login"]["mode"] == "none"
        if not self.signed_out and len(self.password) < 4:
            raise CaptureError("the sign-in password is shorter than 4 characters, so the password guard cannot be trusted; refusing to record", EXIT_BAD_SETUP)
        os.makedirs(self.frames_root, exist_ok=True)
        self._pw = sync_playwright().start()
        self.browser = self._pw.chromium.launch(headless=True, args=[
            "--hide-scrollbars", "--force-color-profile=srgb", "--font-render-hinting=none", "--disable-lcd-text", "--renderer-process-limit=2"])
        return self

    def __exit__(self, *exc):
        try:
            self.browser.close()
        finally:
            self._pw.stop()
            shutil.rmtree(self.frames_root, ignore_errors=True)

    def new_context(self, state=None, reduce=True):
        ctx = self.browser.new_context(
            viewport={"width": 1920, "height": 1080}, device_scale_factor=1.0, color_scheme="dark",
            locale=self.doc.get("locale", "en-US"), timezone_id=self.doc.get("timezone", "UTC"),
            reduced_motion="reduce" if reduce else "no-preference", storage_state=state)
        if reduce:
            ctx.add_init_script(QUIET_JS % json.dumps(QUIET_CSS))
        if self.doc.get("cursor", {}).get("highlight", True):
            ctx.add_init_script(CURSOR_JS)
        return ctx

    # -- sign-in (not recorded; the typed values live in the page's inputs only)

    def sign_in(self):
        if self.signed_out:
            self._say("login mode none: not signing in")
            return
        ctx = self.new_context()
        p = ctx.new_page()
        try:
            p.goto(self.base + "/login")
            p.fill("#email", self.email)
            if not p.is_visible("#password"):  # the sign-in asks for the email first, then the password
                p.click("button[type=submit]")
                p.wait_for_selector("#password", state="visible", timeout=15000)
            p.fill("#password", self.password)
            p.click("button[type=submit]")
            p.wait_for_url(lambda u: "/login" not in u, timeout=30000)
            p.wait_for_load_state("networkidle")
        except Exception as e:  # noqa: BLE001
            raise CaptureError("the console did not sign in: " + (scrub(e, self.secrets).splitlines() or ["?"])[0], EXIT_BAD_SETUP)
        self.state = ctx.storage_state()  # in memory only: never written to a file
        ctx.close()
        self._say("signed in (not recorded)")

    # -- locators and actions

    def locator(self, page, t):
        kind, arg, nth, exact = target_spec(t)
        if kind == "testid":
            loc = page.get_by_test_id(arg)
        elif kind == "shot":
            loc = page.locator(f'[data-shot="{arg}"]')
        elif kind == "text":
            loc = page.get_by_text(regex_arg(arg), exact=exact)
        elif kind == "label":
            loc = page.get_by_label(regex_arg(arg), exact=exact)
        elif kind == "placeholder":
            loc = page.get_by_placeholder(regex_arg(arg), exact=exact)
        elif kind == "selector":
            loc = page.locator(arg)
        else:
            loc = page.get_by_role(arg[0], name=regex_arg(arg[1]), exact=exact) if arg[1] else page.get_by_role(arg[0])
        return loc.nth(nth)

    def glide(self, page, x, y):
        x0, y0 = self.pos
        for px, py in glide_points(x0, y0, x, y):
            page.mouse.move(px, py)
            page.wait_for_timeout(16)
        self.pos = (x, y)

    def center(self, loc, timeout):
        loc.wait_for(state="visible", timeout=timeout)
        loc.scroll_into_view_if_needed(timeout=timeout)
        box = loc.bounding_box()
        if not box:
            raise CaptureError("a target is not on screen")
        return box["x"] + box["width"] / 2, box["y"] + box["height"] / 2

    def point(self, page, loc, timeout, pause):
        x, y = self.center(loc, timeout)
        self.glide(page, x, y)
        page.wait_for_timeout(pause)  # rest, so the viewer sees where it is going

    @staticmethod
    def describe(body):
        if isinstance(body, dict):
            return ", ".join(f"{k}={v}" for k, v in body.items() if k in TARGET_KEYS and k not in ("nth", "exact")) or "target"
        return str(body)

    def add_typed_secret(self, text):
        if len(text) < 4:
            raise CaptureError("a secret typed by a step is shorter than 4 characters, so the guard cannot be trusted with it; refusing to record", EXIT_BAD_SETUP)
        if text not in self.typed_secrets:
            self.typed_secrets.append(text)
            self.secrets = (*self.secrets, text)
            _SECRETS.append(text)

    def click_here(self, page):
        page.mouse.down()
        page.wait_for_timeout(70)
        page.mouse.up()

    def signin_dash(self, page, clip, step, shot, masking):
        """Signs in to /dash through its own form (data-shot dash-email, dash-password, dash-signin-submit). /dash keeps its session in the
        page's memory, not in cookies, so the console's sign-in does not carry over and this runs in the shot's own page, every attempt.
        The password field is checked masked BEFORE the first character goes in, and the whole guard runs after every character. Hidden
        (the default) it runs before the clip starts, so no frame of it exists; on_camera it is recorded with human pacing."""
        timeout = int(step.get("timeout_s", 30) * 1000)
        card = page.locator('[data-shot="dash-signin"]')
        email, pw = page.locator('[data-shot="dash-email"]'), page.locator('[data-shot="dash-password"]')
        submit = page.locator('[data-shot="dash-signin-submit"]')
        pause = int(step.get("pause_ms", 450))
        pace = int(step.get("pace_ms", 85))
        try:
            card.wait_for(state="visible", timeout=timeout)
            pw.wait_for(state="visible", timeout=timeout)
        except Exception:  # noqa: BLE001
            raise CaptureError("the /dash sign-in form is not on the page (is the start route /dash/ and the user signed out?)")
        if pw.get_attribute("type") != "password":
            raise SecretLeak("the /dash password field is not a password input, so what is typed into it would be drawn; no clip is kept")
        self.guard(page, masking)  # a password field with no blur on it ends the shot here, before a character is typed
        if step.get("on_camera"):
            clip.note(step.get("label") or "sign in to /dash")
            self.point(page, email, timeout, pause)
            self.click_here(page)
            for ch, d in zip(self.email, typing_delays(self.email, shot["id"] + ":email", pace)):
                page.keyboard.type(ch)
                page.wait_for_timeout(d)
            self.point(page, pw, timeout, pause)
            self.click_here(page)
            for ch, d in zip(self.password, typing_delays(self.password, shot["id"] + ":password", pace)):
                self.guard(page, masking)
                page.keyboard.type(ch)
                page.wait_for_timeout(d)
            self.guard(page, masking)
            self.point(page, submit, timeout, pause)
            self.click_here(page)
        else:
            email.fill(self.email)
            pw.fill(self.password)
            self.guard(page, masking)
            submit.click()
        try:
            card.wait_for(state="detached", timeout=timeout)
        except Exception:  # noqa: BLE001 - the form is still there: the credential was refused, there is a second factor or a tenant to pick
            self.guard(page, masking)
            raise CaptureError("the /dash sign-in did not complete (the form is still showing: the credential was refused, or the user needs a second factor)")
        page.wait_for_load_state("networkidle", timeout=timeout)
        self.guard(page, masking)
        page.wait_for_timeout(int(step.get("settle_s", 0.6) * 1000))

    def do_step(self, page, clip, step, shot):
        verb = next(k for k in STEP_KINDS if k in step)
        body = step[verb]
        timeout = int(step.get("timeout_s", 20) * 1000)
        pause = int(step.get("pause_ms", 450))
        label = step.get("label") or f"{verb} {self.describe(body)}"
        if verb == "goto":
            clip.note(label)
            resp = page.goto(self.base + body)
            if resp is not None and resp.status >= 400:
                raise CaptureError(f"{body} answered HTTP {resp.status}")
            page.wait_for_load_state("networkidle", timeout=timeout)
            page.wait_for_timeout(int(step.get("settle_s", 0.8) * 1000))
        elif verb in ("click", "hover"):
            self.point(page, self.locator(page, body), timeout, pause)
            clip.note(label)
            if verb == "click":
                page.mouse.down()
                page.wait_for_timeout(70)
                page.mouse.up()
                page.wait_for_timeout(int(step.get("settle_s", 0.5) * 1000))
        elif verb == "type":
            if step.get("secret"):
                self.add_typed_secret(body["text"])
            self.point(page, self.locator(page, body["into"]), timeout, pause)
            page.mouse.down()
            page.wait_for_timeout(70)
            page.mouse.up()
            clip.note(step.get("label") or (f"type into {self.describe(body['into'])}" + ("" if step.get("secret") else f": {body['text']}")))
            for ch, d in zip(body["text"], typing_delays(body["text"], shot["id"], int(step.get("pace_ms", 85)))):
                page.keyboard.type(ch)
                page.wait_for_timeout(d)
            page.wait_for_timeout(int(step.get("settle_s", 0.4) * 1000))
        elif verb == "press":
            clip.note(label)
            page.keyboard.press(body)
            page.wait_for_timeout(int(step.get("settle_s", 0.5) * 1000))
        elif verb == "wait_for":
            self.locator(page, body).wait_for(state="visible", timeout=timeout)
            clip.note(label)
        elif verb == "wait":
            clip.note(label)
            end = time.time() + body
            while time.time() < end:
                page.wait_for_timeout(int(min(500, max(1, (end - time.time()) * 1000))))
                self.guard(page, bool(shot.get("mask")))
        elif verb == "scroll":
            clip.note(label)
            if "by" in body:
                remaining, sign = abs(body["by"]), (1 if body["by"] >= 0 else -1)
                while remaining > 0:
                    d = min(40, remaining)
                    page.mouse.wheel(0, sign * d)
                    page.wait_for_timeout(16)
                    remaining -= d
            else:
                loc = self.locator(page, body["to"])
                loc.wait_for(state="attached", timeout=timeout)
                box = loc.bounding_box()
                if box and not (0 <= box["y"] <= 1080 - box["height"]):
                    dy = box["y"] - 300
                    for _ in range(max(1, int(abs(dy) // 40))):
                        page.mouse.wheel(0, 40 if dy > 0 else -40)
                        page.wait_for_timeout(16)
            page.wait_for_timeout(300)
        elif verb == "mark":
            clip.note(body)
        elif verb == "signin":
            self.signin_dash(page, clip, step, shot, bool(shot.get("mask")))

    # -- one shot

    def run_shot(self, shot):
        retries = shot.get("retries", 1)
        last = None
        for stale in (os.path.join(self.out, shot["id"] + ".mp4"), os.path.join(self.out, "failures", shot["id"] + ".png")):
            if os.path.exists(stale):
                os.remove(stale)  # an earlier run's clip must never sit beside this run's failure
        for attempt in range(1, retries + 2):
            try:
                entry = self.attempt(shot, attempt)
                png = os.path.join(self.out, "failures", shot["id"] + ".png")
                if os.path.exists(png):
                    os.remove(png)  # an earlier attempt's screenshot does not belong beside a clip that succeeded
                return entry
            except SecretLeak:
                raise
            except CaptureError as e:
                last = e
            except Exception as e:  # noqa: BLE001 - a Playwright timeout etc.: the shot failed, the run goes on
                last = CaptureError((scrub(e, self.secrets).splitlines() or [type(e).__name__])[0])
            self._say(f"  attempt {attempt} of {retries + 1} failed: {last}")
        raise last

    @staticmethod
    def mask_name(m):
        return m.get("name") or next((f"{k}={m[k]}" for k in ("selector", "shot", "testid", "text_regex") if k in m), "?")

    def _on_text(self, text):
        """called by the page for every changed text, attribute and input value: the password in any of them marks the run as leaked"""
        if password_leaked(text, self.guarded()):
            self.leaked = True

    def guarded(self):
        """every value the per-change report and the page probe compare with: the sign-in password and any secret a step has typed"""
        return (self.password, *self.typed_secrets)

    def _on_response(self, resp):
        self.pending.append(resp)

    def drain(self):
        """Fails the shot on a same-origin XHR/fetch/document response of 400 or more, or a GraphQL body that carries errors."""
        pend, self.pending = self.pending, []
        for r in pend:
            try:
                if not r.url.startswith(self.base):
                    continue
                kind = r.request.resource_type
                path = urlsplit(r.url).path
                if kind in ("xhr", "fetch", "document") and r.status >= 400:
                    raise CaptureError(f"{r.request.method} {path} answered HTTP {r.status}")
                if kind in ("xhr", "fetch") and "graphql" in path:
                    body = r.json()
                    for item in (body if isinstance(body, list) else [body]):
                        if isinstance(item, dict) and item.get("errors"):
                            first = item["errors"][0]
                            msg = first.get("message", "") if isinstance(first, dict) else str(first)
                            raise CaptureError(f"GraphQL {path} returned errors: {msg[:120]}")
            except CaptureError:
                raise
            except Exception:  # noqa: BLE001 - a body that is gone (the page navigated) cannot be read; the status check above already ran
                continue

    def attempt(self, shot, attempt):
        specs = shot.get("mask", []) or []
        reduce = shot.get("reduce_motion", self.doc.get("reduce_motion", True))
        self.leaked, self.pending = False, []
        steps = shot["steps"]
        hidden = None
        if "signin" in steps[0] and not steps[0].get("on_camera"):
            hidden, steps = steps[0], steps[1:]  # runs before the clip starts: no frame of it is ever kept
        ctx = page = td = None
        failed_at = "start"
        masking = bool(specs)
        try:
            ctx = self.new_context(self.state, reduce=reduce)
            seen = set()
            ctx.expose_function("__dcMaskSeen", lambda i: seen.add(i))
            ctx.expose_function("__dcLeak", self._on_text)
            spec = {"css": mask_css(specs), "selectors": mask_selectors(specs),
                    "regexes": [[i, m["text_regex"]] for i, m in enumerate(specs) if "text_regex" in m], "blur": 14}
            ctx.add_init_script(MASK_JS.replace("__SPEC__", json.dumps(spec)))
            page = ctx.new_page()
            page.on("response", self._on_response)
            session = ctx.new_cdp_session(page)
            td = tempfile.mkdtemp(prefix="clip-", dir=self.frames_root)
            clip = Clip(session, td, self.fps)
            self.pos = (960, 540)
            resp = page.goto(self.base + shot["start"])
            if resp is not None and resp.status >= 400:
                raise CaptureError(f"{shot['start']} answered HTTP {resp.status}")
            page.wait_for_load_state("networkidle", timeout=30000)
            page.wait_for_timeout(800)
            self.guard(page, masking)
            if hidden:
                failed_at = "sign-in to /dash (not recorded)"
                self.signin_dash(page, None, hidden, shot, masking)
            clip.begin()
            for j, step in enumerate(steps):
                failed_at = f"step {j + 1} ({next(k for k in STEP_KINDS if k in step)})"
                self.do_step(page, clip, step, shot)
                self.guard(page, masking)
            failed_at = "end"
            missing = [i for i, m in enumerate(specs) if m.get("required", True) and i not in seen]
            if missing:
                raise CaptureError("mask " + ", ".join(self.mask_name(specs[i]) for i in missing) +
                                   " matched nothing: the region it hides is not on the page, so the shot is not safe to keep", unsafe=True)
            if shot.get("min_s"):
                need = shot["min_s"] + shot.get("trim", {}).get("head_s", 0) + shot.get("trim", {}).get("tail_s", 0)
                while time.time() - clip.start < need:
                    page.wait_for_timeout(int(min(500, max(1, (need - (time.time() - clip.start)) * 1000))))
                    self.guard(page, masking)
            self.guard(page, masking)
            start, end = clip.finish()
            self.guard(page, masking)  # the last frame, and anything reported while the screencast stopped, is checked before anything is encoded
            if shot.get("max_s") and end - start > shot["max_s"]:
                raise CaptureError(f"the shot took {end - start:.1f}s, over its max_s of {shot['max_s']}")
            win = trim_window(start, end, shot.get("trim", {}), shot.get("min_s"))
            encode_atomic(clip.frames, win[0], win[1], os.path.join(self.out, shot["id"] + ".mp4"), self.fps)
            return shot_entry(shot, start, end, clip.marks, shot.get("trim", {}), self.fps, attempt, win)
        except Exception as e:
            if page is not None and not (isinstance(e, CaptureError) and e.unsafe):
                self.fail_shot(page, shot, failed_at, masking)  # runs the guard first; raises SecretLeak instead of photographing a leak
            elif page is not None:
                self._say("  no failure screenshot: this failure is about what the page shows")
            if isinstance(e, CaptureError):
                raise type(e)(f"{failed_at}: {e}") if isinstance(e, SecretLeak) else CaptureError(f"{failed_at}: {e}", e.exit_code, e.unsafe)
            raise
        finally:
            if td:
                shutil.rmtree(td, ignore_errors=True)
            if ctx is not None:
                try:
                    ctx.close()
                except Exception:  # noqa: BLE001
                    pass

    PROBE = """(masking) => {
      const texts = [document.body ? document.body.innerText : ''];
      for (const e of document.querySelectorAll('input:not([type=password]),textarea,select')) texts.push(e.value || '');
      const walk = (root) => { for (const el of root.querySelectorAll('*')) { if (el.shadowRoot) { texts.push(el.shadowRoot.textContent || ''); walk(el.shadowRoot); } } };
      walk(document);
      const css = document.getElementById('__dc_mask_css');
      // a password field the viewer can see must carry a blur (the stylesheet puts one on every input[type=password]); a page that defeats
      // it, or a mask that never loaded, would draw whatever the field shows
      let bare = false;
      const shown = (e) => { const cs = getComputedStyle(e); return e.getClientRects().length > 0 && cs.visibility !== 'hidden' && cs.display !== 'none'; };
      const scan = (root) => {
        for (const e of root.querySelectorAll('input[type=password]')) if (shown(e) && !/blur\\(\\s*[1-9]/.test(getComputedStyle(e).filter)) bare = true;
        for (const el of root.querySelectorAll('*')) if (el.shadowRoot) scan(el.shadowRoot);
      };
      scan(document);
      return {text: texts.join('\\n'), masked: !masking || !!(css && css.isConnected), bare: bare, title: document.title};
    }"""

    def guard(self, page, masking=False):
        """Ends the shot when the page is unsafe to record: the sign-in password in any frame's text or form values (SecretLeak, exit 3), the
        mask stylesheet gone from a document that should have it, the sign-in page, or an unreadable page (fail closed, after one retry)."""
        if self.leaked:
            raise SecretLeak("the sign-in password appeared in the page (a text change, an attribute or an input value); no clip is kept")
        if "/login" in page.url and not self.signed_out:
            raise CaptureError("the console sent the shot to the sign-in (the session was not accepted)", unsafe=True)
        self.drain()
        for attempt in (1, 2):
            try:
                probes = [f.evaluate(self.PROBE, masking) for f in page.frames]
                break
            except Exception:  # noqa: BLE001 - a frame mid-navigation: wait for it to settle once, then fail closed
                if attempt == 2:
                    raise CaptureError("the page could not be inspected for secrets, so it is not safe to keep", unsafe=True)
                page.wait_for_timeout(500)
        for k, pr in enumerate(probes):
            if password_leaked(pr["text"] + "\n" + pr["title"], self.guarded()):
                raise SecretLeak("the sign-in password is visible in a recorded page; no clip is kept")
            if pr["bare"]:
                raise SecretLeak("a password field is on screen without its blur mask (the page defeated it, or the stylesheet is missing); no clip is kept")
            if k == 0 and not pr["masked"]:  # the main document carries the init script; a child frame may be another origin
                raise CaptureError("the mask stylesheet is missing from a recorded page, so it is not safe to keep", unsafe=True)
        if self.leaked:
            raise SecretLeak("the sign-in password appeared in the page (a text change, an attribute or an input value); no clip is kept")

    def fail_shot(self, page, shot, where, masking=False):
        """A screenshot of the failed page, taken only when the page passes the guard: a SecretLeak propagates, and any other guard failure
        (or an unreadable page) means no screenshot."""
        try:
            self.guard(page, masking)
        except SecretLeak:
            raise
        except Exception:  # noqa: BLE001
            self._say("  no failure screenshot: the page could not be shown to be safe")
            return
        d = os.path.join(self.out, "failures")
        path = os.path.join(d, shot["id"] + ".png")
        try:
            os.makedirs(d, exist_ok=True)
            page.screenshot(path=path)
            self._say(f"  failure screenshot: {path} (at {where})")
        except Exception:  # noqa: BLE001
            self._say("  (no failure screenshot: the page is gone)")

    def write_manifest(self, entries, failures):
        doc = {"video": self.doc["video"], "fps": self.fps, "viewport": "1920x1080", "base_url": self.base,
               "generated_utc": utc(time.time()), "clips": entries, "failed": failures}
        path = os.path.join(self.out, "manifest.json")
        with open(path, "w", encoding="utf-8") as f:
            json.dump(scrub_obj(doc, self.secrets), f, indent=2)
            f.write("\n")
        return path


# ---------------------------------------------------------------------------------------------
# entry points


def load_shotlist(path):
    try:
        import yaml
    except ImportError:
        raise CaptureError("PyYAML is not installed (hack/video-capture/setup.sh installs it)", EXIT_BAD_SETUP)

    try:
        with open(path, encoding="utf-8") as f:
            doc = yaml.safe_load(f)
    except (OSError, yaml.YAMLError) as e:
        raise CaptureError(f"{path}: {type(e).__name__}", EXIT_BAD_SETUP)
    errs = validate(doc)
    if errs:
        raise CaptureError(f"{path} is not a valid shot list:\n  " + "\n  ".join(errs), EXIT_BAD_SETUP)
    return doc


def select(doc, only):
    if not only:
        return doc["shots"]
    names = [s for s in only.split(",") if s]
    have = {s["id"] for s in doc["shots"]}
    bad = [n for n in names if n not in have]
    if bad:
        raise CaptureError("unknown shot " + ",".join(bad), EXIT_BAD_SETUP)
    return [s for s in doc["shots"] if s["id"] in names]


def posix_drive_path(p):
    """True for '/c/Users/...' on Windows: Git Bash hands such a path to a native Python verbatim (MSYS_NO_PATHCONV), where it means C:/c/Users,
    a stray tree on the drive. Refused rather than recorded into."""
    return os.name == "nt" and re.match(r"^/([A-Za-z](/|$)|cygdrive/)", p.replace("\\", "/")) is not None


def record(args):
    if posix_drive_path(args.out):
        raise CaptureError(f"--out {args.out} is a POSIX-style drive path, which Windows Python reads as {os.sep}{args.out.lstrip('/')}; give a Windows path (C:/...)", EXIT_BAD_SETUP)
    doc = load_shotlist(args.shotlist)
    shots = select(doc, args.shots)
    if not shutil.which("ffmpeg"):
        raise CaptureError("ffmpeg is not on PATH", EXIT_BAD_SETUP)
    entries, failures = [], []
    rc = EXIT_OK
    rec = Recorder(doc, args.out, args.base_url)
    with rec:
        rec.sign_in()
        for shot in shots:
            rec._say(f"shot {shot['id']}")
            try:
                entries.append(rec.run_shot(shot))
                rec._say(f"  recorded {shot['id']}.mp4 ({entries[-1]['duration_s']}s)")
            except SecretLeak as e:
                failures.append({"shot": shot["id"], "error": str(e)})
                rec._say(f"  SECRET: {e}")
                rc = EXIT_SECRET_LEAK
                break
            except CaptureError as e:
                failures.append({"shot": shot["id"], "error": str(e)})
                rec._say(f"  FAILED {shot['id']}: {e}")
                rc = rc or EXIT_SHOT_FAILED
            rec.write_manifest(entries, failures)  # after every shot: a run that dies later still leaves the index of what it has
        path = rec.write_manifest(entries, failures)
        rec._say(f"manifest: {path}  ({len(entries)} recorded, {len(failures)} failed)")
    return rc


def main(argv=None):
    ap = argparse.ArgumentParser(description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
    sub = ap.add_subparsers(dest="mode", required=True)
    c = sub.add_parser("check")
    c.add_argument("shotlist")
    r = sub.add_parser("record")
    r.add_argument("shotlist")
    r.add_argument("--out", required=True)
    r.add_argument("--shots", default="", help="comma list of shot ids (default: all)")
    r.add_argument("--base-url", default=None, help="the console's address (default: $DC_CONSOLE_URL, else the shot list's base_url)")
    args = ap.parse_args(argv)
    try:
        if args.mode == "check":
            doc = load_shotlist(args.shotlist)
            print(f"{args.shotlist}: valid ({len(doc['shots'])} shots)")
            return EXIT_OK
        return record(args)
    except CaptureError as e:
        print("video-capture: " + scrub(e, _SECRETS), file=sys.stderr)
        return e.exit_code


if __name__ == "__main__":
    sys.exit(main())
