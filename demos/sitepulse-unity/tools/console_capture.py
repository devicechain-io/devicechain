#!/usr/bin/env python3
# Copyright The DeviceChain Authors
# SPDX-License-Identifier: Apache-2.0
"""
The console half of the Sitepulse feature video (script shots S17-S20): a headless browser signs in to the console as the sim's tenant user and
records the screens the operator uses, at 1920x1080, one mp4 per shot.

    S17  s17-board          the Site Pulse board (console, Dashboards, Site Pulse) once the player's log says the tyre alarm is ACTIVE
    S18  s18-alarm-drill    the originator drill: click SP-HL-0003 in the active-alarm table so the Machine gauges follow it
    S19  s19-command-form   console, Devices, SP-HL-0003, Commands: Go To Area, areaToken sp-zone-yard, up to (not including) Send
    S20  s20-command-sent   Send, then the command row as the platform moves it QUEUED, SENT, SUCCESSFUL

    console_capture.py take  --log <player log> --out <take out> [--shots s17,s18,s19,s20] [--no-send]   follow a take's log (video-take.sh)
    console_capture.py shot  --out <dir> --shots s17[,s18]                                               stand-alone, now, against the environment
    console_capture.py shot  --out <dir> --shots s19                                                       fills the form, never presses Send

Output: <out>/console/<shot>.mp4 (H.264, yuv420p, 30 fps) and <out>/console/console-shots.json (each shot's UTC start and end, and what was
clicked), so an editor can line the clips up with the Unity renders.

How a clip is made. Playwright's own recordVideo encodes a fixed low bitrate VP8; instead the page's frames are taken from the browser's
screencast (a frame whenever the page repaints, each with its timestamp) and rendered at a constant 30 fps by ffmpeg, so motion is as smooth
as the page was. A headless browser draws no cursor, so one is injected into the page (a pointer that glides to each target).

The sign-in. The tenant user's email and password are read from the sim's handshake record (SP_HOME/.devicechain/sims/sitepulse.json) in this
process, typed into the form, and forgotten. They are never printed, logged, put in the manifest or on a command line, and the sign-in is not
recorded. Every message that leaves this module passes through scrub(), which removes the two values wherever they appear.

Install (once; no sudo):  tools/console-recorder-setup.sh   (a venv under ~/.cache/sitepulse-recorder with the pinned Playwright + Chromium)

Everything that decides something is a pure function above the I/O (tested by test_console_capture.py).
"""

import argparse
import base64
import datetime
import json
import os
import re
import shutil
import subprocess
import sys
import tempfile
import time

import video_take as v

CONSOLE_URL = os.environ.get("SP_CONSOLE_URL", "http://localhost")
BOARD_TOKEN = "sp-dashboard"
DEVICE = v.DEVICE  # the device's external id, as the player's log and the board's cards name it
DEVICE_TOKEN = "sp-hauler-03"  # its token, which is what the console's device list and the alarm table's originator show
AREA = v.AREA
COMMAND_LABEL = "Go To Area"
WIDTH, HEIGHT = 1920, 1080
# The board is taller than a 1080p console window (its active-alarm table and Machine gauges sit below the fold), so for the board
# shots the page is laid out at 2400x1350 CSS pixels and rendered at 0.8 device scale: the whole board in a 1920x1080 frame.
BOARD_LAYOUT = (2400, 1350, 0.8)
FORM_LAYOUT = (1920, 1080, 1.0)
FPS = 30

SHOTS = ("s17", "s18", "s19", "s20")
SHOT_FILES = {
    "s17": "s17-board",
    "s18": "s18-alarm-drill",
    "s19": "s19-command-form",
    "s20": "s20-command-sent",
}
# how long each shot is held, in seconds (the script's rows are 5, 4, 6 and 2 s; the clips carry handles for the editor)
S17_SECONDS = 8.0
S18_SECONDS = 6.0
S19_HOLD_SECONDS = 2.0
S20_FINISH_SECONDS = 90.0
S20_MIN_SECONDS = 6.0

# the player says: "[sitepulse] video-run: 330 s · tyre alarm · the platform says tyre-pressure-low is ACTIVE; ..."
# (and, when it never happens, "tyre alarm NOT seen", which is a different line and must not start the shot)
TYRE_ALARM_STEP = re.compile(r"video-run: \d+ s \W+ tyre alarm \W+ the platform says tyre-pressure-low is ACTIVE")

EXIT_OK = 0
EXIT_FAILED_BEFORE_SEND = 3  # the console part failed before it sent the command: the caller may send it another way
EXIT_FAILED_AFTER_SEND = 4  # the command was sent, a clip was lost


# ---------------------------------------------------------------------------------------------
# pure parts


def is_tyre_alarm_step(line):
    return TYRE_ALARM_STEP.search(line) is not None


def scrub(text, secrets):
    """<text> with every secret value removed. Messages from the browser can echo a typed value; nothing leaves this module unscrubbed."""
    text = str(text)
    for s in secrets:
        if s:
            text = text.replace(s, "***")
    return text


def find_credentials(record):
    """(email, password) from the sim's handshake record (a parsed JSON value): the keys simEmail and simPassword, wherever they nest."""
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
        raise CaptureError("the sim record has no tenant user (simEmail / simPassword)")
    return found["simEmail"], found["simPassword"]


def sim_record_path(sp_home=None):
    home = sp_home or os.environ.get("SP_HOME") or os.path.join(os.path.expanduser("~"), "sitepulse-env")
    return os.path.join(home, ".devicechain", "sims", "sitepulse.json")


def load_credentials(path=None):
    try:
        with open(path or sim_record_path(), encoding="utf-8") as f:
            return find_credentials(json.load(f))
    except (OSError, ValueError):
        # the reason is the file's, never its content
        raise CaptureError("the sim record could not be read (is the environment up? tools/live-env.sh status)")


def utc(ts):
    return datetime.datetime.fromtimestamp(ts, datetime.timezone.utc).strftime("%Y-%m-%dT%H:%M:%S.%f")[:-3] + "Z"


def shot_entry(name, start, end, actions, status_trail=None, fps=FPS):
    """One shot's record in console-shots.json."""
    entry = {
        "shot": name,
        "file": SHOT_FILES[name] + ".mp4",
        "start_utc": utc(start),
        "end_utc": utc(end),
        "duration_s": round(end - start, 3),
        "fps": fps,
        "size": f"{WIDTH}x{HEIGHT}",
        "actions": [{"at_utc": utc(t), "what": what} for t, what in actions],
    }
    if status_trail is not None:
        entry["command_status"] = [{"at_utc": utc(t), "status": s} for t, s in status_trail]
    return entry


def manifest(entries, out_dir):
    return {"clips": entries, "dir": os.path.basename(out_dir.rstrip(os.sep)), "viewport": f"{WIDTH}x{HEIGHT}"}


def clip_timeline(frames, start, end):
    """[(frame path, duration)] holding each frame until the next arrives and the last until <end>. <frames> is [(timestamp, path)]."""
    frames = sorted(frames)
    out = []
    for i, (t, path) in enumerate(frames):
        t = max(t, start)
        nxt = frames[i + 1][0] if i + 1 < len(frames) else end
        out.append((path, max(nxt - t, 0.001)))
    return out


def concat_script(timeline):
    """ffmpeg concat-demuxer text for a timeline. The last file is named twice (the demuxer ignores the last duration otherwise)."""
    lines = []
    for path, dur in timeline:
        lines.append("file '" + path.replace("'", "'\\''") + "'")
        lines.append(f"duration {dur:.4f}")
    if timeline:
        lines.append("file '" + timeline[-1][0].replace("'", "'\\''") + "'")
    return "\n".join(lines) + "\n"


def status_changes(trail, status, now):
    """The trail with <status> appended when it is new."""
    if status and (not trail or trail[-1][1] != status):
        return trail + [(now, status)]
    return trail


class CaptureError(Exception):
    pass


# ---------------------------------------------------------------------------------------------
# the log


class LogWatcher:
    """Reads a growing log once, line by line, and answers 'has a line like this arrived'. Every registered predicate sees every line, so a
    line that arrives while the caller waits for another one is not lost to the later wait."""

    def __init__(self, path, predicates=None):
        self.path = path
        self.offset = 0
        self.carry = ""
        self.seen = set()
        self.predicates = dict(predicates or {})

    def poll(self, predicates=None):
        """Names of the predicates (the registered ones and any given here) that matched a line so far."""
        preds = dict(self.predicates)
        preds.update(predicates or {})
        text, self.offset = v.new_text(self.path, self.offset)
        self.carry += text
        *lines, self.carry = self.carry.split("\n")
        for line in lines:
            for name, fn in preds.items():
                if fn(line):
                    self.seen.add(name)
        return set(self.seen)

    def wait(self, name, fn=None, timeout=0, poll=1.0, sleep=time.sleep, clock=time.monotonic):
        if fn is not None:
            self.predicates[name] = fn
        deadline = clock() + timeout
        while True:
            if name in self.poll():
                return True
            if clock() >= deadline:
                return False
            sleep(poll)


# ---------------------------------------------------------------------------------------------
# the browser (everything below needs Playwright)

CURSOR_JS = """
(() => {
  if (window.__spCursor) return;
  const make = () => {
    if (!document.body) { return false; }
    const c = document.createElement('div');
    c.id = '__sp_cursor';
    c.style.cssText = 'position:fixed;left:0;top:0;width:28px;height:28px;margin:-4px 0 0 -4px;z-index:2147483647;pointer-events:none;'
      + 'transform:translate(-100px,-100px);will-change:transform;';
    c.innerHTML = '<svg width="28" height="28" viewBox="0 0 28 28"><path d="M4 2 L4 22 L9.5 17.2 L13.2 25 L16.6 23.5 L12.9 15.8 L20.5 15.8 Z"'
      + ' fill="#ffffff" stroke="#111111" stroke-width="1.6" stroke-linejoin="round"/></svg>';
    const ring = document.createElement('div');
    ring.id = '__sp_ring';
    ring.style.cssText = 'position:fixed;left:0;top:0;width:44px;height:44px;margin:-22px 0 0 -22px;border-radius:50%;z-index:2147483646;'
      + 'pointer-events:none;border:3px solid rgba(80,160,255,0.95);opacity:0;transform:translate(-100px,-100px) scale(0.4);';
    document.body.appendChild(ring);
    document.body.appendChild(c);
    window.__spCursor = {
      move: (x, y) => { c.style.transform = 'translate(' + x + 'px,' + y + 'px)'; },
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
    document.addEventListener('mousemove', (e) => window.__spCursor.move(e.clientX, e.clientY), true);
    document.addEventListener('mousedown', (e) => window.__spCursor.click(e.clientX, e.clientY), true);
    return true;
  };
  if (!make()) { document.addEventListener('DOMContentLoaded', make); }
})();
"""


class Clip:
    """A recording in progress: the screencast's frames on disk with their timestamps."""

    def __init__(self, session, page, workdir):
        self.session, self.page, self.dir = session, page, workdir
        self.frames = []
        self.n = 0
        self.start = None
        self.actions = []

    def _store(self, ts, data):
        self.n += 1
        path = os.path.join(self.dir, f"f{self.n:06d}.jpg")
        with open(path, "wb") as f:
            f.write(data)
        self.frames.append((ts, path))

    def on_frame(self, params):
        self._store(params["metadata"]["timestamp"], base64.b64decode(params["data"]))
        self.session.send("Page.screencastFrameAck", {"sessionId": params["sessionId"]})

    def begin(self):
        self.session.on("Page.screencastFrame", self.on_frame)
        self.start = time.time()
        self.session.send("Page.startScreencast", {"format": "jpeg", "quality": 95, "maxWidth": WIDTH, "maxHeight": HEIGHT, "everyNthFrame": 1})
        # the screencast sends a frame only when the page repaints: a still page needs its first frame taken by hand
        self._store(self.start, self.grab())

    def grab(self):
        return base64.b64decode(self.session.send("Page.captureScreenshot", {"format": "jpeg", "quality": 95})["data"])

    def note(self, what):
        self.actions.append((time.time(), what))

    def finish(self):
        end = time.time()
        self.session.send("Page.stopScreencast")
        self._store(end, self.grab())
        try:
            self.session.remove_listener("Page.screencastFrame", self.on_frame)
        except Exception:  # noqa: BLE001 - a listener that is already gone is the state we want
            pass
        return self.start, end


def encode(clip_frames, start, end, dest, fps=FPS):
    """Renders a clip to <dest> (H.264, yuv420p, constant <fps>)."""
    timeline = clip_timeline(clip_frames, start, end)
    with tempfile.TemporaryDirectory() as td:
        lst = os.path.join(td, "frames.txt")
        with open(lst, "w", encoding="utf-8") as f:
            f.write(concat_script(timeline))
        cmd = [
            "ffmpeg", "-y", "-loglevel", "error", "-f", "concat", "-safe", "0", "-i", lst,
            "-vf", f"fps={fps},scale={WIDTH}:{HEIGHT}:flags=lanczos,format=yuv420p",
            "-c:v", "libx264", "-preset", "medium", "-crf", "14", "-r", str(fps), "-movflags", "+faststart", dest,
        ]
        subprocess.run(cmd, check=True)


class Console:
    """One signed-in browser. Use as a context manager."""

    def __init__(self, base=CONSOLE_URL, out_dir=".", creds=None, headless=True):
        self.base = base.rstrip("/")
        self.out = out_dir
        self.email, self.password = creds if creds else load_credentials()
        self.secrets = (self.email, self.password)
        self.headless = headless
        self.entries = []
        self.pos = (WIDTH // 2, HEIGHT // 2)
        self.signed_in = False
        self.command_sent = False  # set the moment Send is pressed: whatever fails after that, the command exists
        os.makedirs(self.out, exist_ok=True)

    def __enter__(self):
        from playwright.sync_api import sync_playwright

        self._pw = sync_playwright().start()
        self.browser = self._pw.chromium.launch(headless=self.headless, args=["--hide-scrollbars", "--force-color-profile=srgb"])
        self.views = {}
        self.use("form")
        return self

    def _new_view(self, kind, state=None):
        w, h, scale = BOARD_LAYOUT if kind == "board" else FORM_LAYOUT
        ctx = self.browser.new_context(viewport={"width": w, "height": h}, device_scale_factor=scale, color_scheme="dark", storage_state=state)
        ctx.add_init_script(CURSOR_JS)
        page = ctx.new_page()
        self.views[kind] = (ctx, page, ctx.new_cdp_session(page))
        return self.views[kind]

    def use(self, kind):
        """Switches to the 'form' view (1920x1080) or the 'board' view (2400x1350 CSS px at 0.8, so the whole board fits a 1080p frame).
        The board view shares the form view's sign-in (held in memory only)."""
        if kind not in self.views:
            state = self.views["form"][0].storage_state() if kind == "board" and self.signed_in else None
            self._new_view(kind, state)
        self.ctx, self.page, self.session = self.views[kind]
        self.pos = (WIDTH // 2, HEIGHT // 2)

    def __exit__(self, *exc):
        try:
            self.browser.close()
        finally:
            self._pw.stop()

    # -- helpers

    def url(self, path):
        return self.base + path

    def sign_in(self):
        """Signs in as the tenant user. Not recorded; the typed values live in the page's inputs only, and a failure's message is scrubbed."""
        p = self.page
        try:
            p.goto(self.url("/login"))
            p.fill("#email", self.email)
            p.fill("#password", self.password)
            p.click("button[type=submit]")
            p.wait_for_url(lambda u: "/login" not in u, timeout=30000)
            p.wait_for_load_state("networkidle")
        except Exception as e:  # noqa: BLE001
            raise CaptureError("the console did not sign in: " + scrub(e, self.secrets).splitlines()[0])
        self.signed_in = True

    def glide(self, x, y, steps=28):
        """Moves the cursor to (x, y) in a smooth ease-in-out, 16 ms a step."""
        x0, y0 = self.pos
        for i in range(1, steps + 1):
            t = i / steps
            e = t * t * (3 - 2 * t)
            self.page.mouse.move(x0 + (x - x0) * e, y0 + (y - y0) * e)
            self.page.wait_for_timeout(16)
        self.pos = (x, y)

    def center(self, locator):
        locator.scroll_into_view_if_needed()
        box = locator.bounding_box()
        if not box:
            raise CaptureError("a target is not on screen")
        return box["x"] + box["width"] / 2, box["y"] + box["height"] / 2

    def point_click(self, locator, clip=None, what=None, pause=450):
        """Glides to the element, rests a moment so the viewer sees where it is going, and clicks."""
        x, y = self.center(locator)
        self.glide(x, y)
        self.page.wait_for_timeout(pause)
        if clip is not None and what:
            clip.note(what)
        self.page.mouse.down()
        self.page.wait_for_timeout(70)
        self.page.mouse.up()

    def hold(self, seconds):
        self.page.wait_for_timeout(int(seconds * 1000))

    # -- the clips

    def record(self, name):
        return _ClipScope(self, name)

    def write_manifest(self):
        path = os.path.join(self.out, "console-shots.json")
        with open(path, "w", encoding="utf-8") as f:
            json.dump(manifest(self.entries, self.out), f, indent=2)
            f.write("\n")
        return path

    # -- the screens

    def open_board(self):
        self.use("board")
        p = self.page
        p.goto(self.url("/dashboards"))
        p.wait_for_load_state("networkidle")
        # the list is the route the script names: Dashboards, then Site Pulse
        link = p.get_by_text("Site Pulse", exact=True).first
        link.wait_for(timeout=20000)
        link.click()
        p.wait_for_url(re.compile(r"/dashboards/[^/]+"), timeout=20000)
        self.wait_board_ready()

    def wait_board_ready(self):
        p = self.page
        p.wait_for_load_state("networkidle")
        p.wait_for_timeout(2500)

    def open_device_commands(self):
        self.use("form")
        p = self.page
        p.goto(self.url("/devices"))
        p.wait_for_load_state("networkidle")
        row = p.get_by_text(DEVICE_TOKEN, exact=True).first
        try:
            row.wait_for(timeout=15000)
        except Exception:  # noqa: BLE001
            search = p.get_by_placeholder(re.compile("search", re.I)).first
            search.fill(DEVICE_TOKEN)
            row = p.get_by_text(DEVICE_TOKEN, exact=True).first
            row.wait_for(timeout=15000)
        row.click()
        p.wait_for_url(re.compile(r"/devices/[^/]+"), timeout=20000)
        p.wait_for_load_state("networkidle")


class _ClipScope:
    def __init__(self, console, name):
        self.console, self.name = console, name
        self.status_trail = None

    def __enter__(self):
        self.td = tempfile.mkdtemp(prefix="spclip-")
        self.clip = Clip(self.console.session, self.console.page, self.td)
        self.clip.begin()
        return self.clip

    def __exit__(self, etype, exc, tb):
        try:
            start, end = self.clip.finish()
            if etype is None:
                dest = os.path.join(self.console.out, SHOT_FILES[self.name] + ".mp4")
                encode(self.clip.frames, start, end, dest)
                self.console.entries.append(shot_entry(self.name, start, end, self.clip.actions, self.status_trail))
                self.console.write_manifest()  # after every clip: a take that dies later still leaves the index of what it has
        finally:
            shutil.rmtree(self.td, ignore_errors=True)
        return False


def shoot_board(con):
    """S17: the whole board, held."""
    con.open_board()
    with con.record("s17") as clip:
        clip.note("the Site Pulse board (console, Dashboards, Site Pulse), whole board")
        con.hold(S17_SECONDS)


def shoot_drill(con):
    """S18: click the tyre alarm's originator so the Machine gauges follow it."""
    p = con.page
    row = p.locator("tr", has_text="tyre-pressure-low").filter(has_text=DEVICE).first
    try:
        row.wait_for(timeout=15000)
    except Exception:  # noqa: BLE001
        raise CaptureError("the board shows no tyre-pressure-low alarm row for " + DEVICE_TOKEN)
    link = row.get_by_role("button", name=DEVICE)
    with con.record("s18") as clip:
        con.hold(1.5)
        con.point_click(link, clip, f"clicked {DEVICE} (the originator) in the tyre-pressure-low alarm row of the active-alarm table")
        con.hold(S18_SECONDS - 1.5)


def fill_command_form(con):
    """Opens the device page's Commands panel (console, Devices, the truck, Commands) and returns the tab."""
    con.open_device_commands()
    p = con.page
    tab = p.get_by_role("tab", name="Commands")
    tab.click()
    p.get_by_text("Select a command").first.wait_for(timeout=15000)
    p.wait_for_timeout(600)
    return tab


def top_row(page):
    """(queued, name, status) of the newest row of the command history, or None."""
    row = page.locator("tbody tr").first
    try:
        cells = [row.locator("td").nth(i).inner_text(timeout=1500).strip() for i in range(3)]
    except Exception:  # noqa: BLE001
        return None
    return tuple(cells)


def open_watch(con):
    """A second page on the same device's Commands panel, never recorded, that says when the history's top row changes."""
    w = con.ctx.new_page()
    w.goto(con.url("/devices/" + DEVICE_TOKEN))
    w.get_by_role("tab", name="Commands").click()
    w.locator("tbody tr").first.wait_for(timeout=15000)
    return w


def poll_watch(w):
    w.reload()
    w.get_by_role("tab", name="Commands").click()
    try:
        w.locator("tbody tr").first.wait_for(timeout=8000)
    except Exception:  # noqa: BLE001
        return None
    return top_row(w)


def refresh_commands(page, tab):
    """The history reloads when the panel remounts: leave the tab and come straight back (a blink, once per status change)."""
    page.get_by_role("tab").first.dispatch_event("mousedown", {"button": 0})
    page.wait_for_timeout(60)
    tab.dispatch_event("mousedown", {"button": 0})
    page.wait_for_timeout(250)


def shoot_command(con, send=True, clock=time.time):
    """S19 (the form, up to Send) and S20 (Send, then the row's lifecycle). Returns True when the command was sent."""
    tab = fill_command_form(con)
    p = con.page
    with con.record("s19") as clip:
        con.hold(1.0)
        con.point_click(p.get_by_text("Select a command").first, clip, "opened the Command picker")
        con.hold(0.6)
        con.point_click(p.get_by_text(COMMAND_LABEL + " (goto-area)", exact=True), clip, f"chose {COMMAND_LABEL}")
        con.hold(0.8)
        con.point_click(p.get_by_text("Select a value").first, clip, "opened the areaToken picker")
        con.hold(0.6)
        con.point_click(p.get_by_text(AREA, exact=True).first, clip, f"chose areaToken {AREA}")
        con.hold(S19_HOLD_SECONDS)
    if not send:
        return False
    watch = open_watch(con)
    try:
        base = (top_row(p) or ("",))[:2]
        trail, shown = [], None
        scope = con.record("s20")
        with scope as clip:
            con.point_click(p.get_by_role("button", name="Issue command"), clip, "pressed Issue command (Send)")
            con.command_sent = True
            started = clock()
            while True:
                now = clock()
                try:
                    row = poll_watch(watch)
                except Exception:  # noqa: BLE001 - a missed poll is a later poll, not a lost shot
                    row = None
                status = row[2] if row and row[:2] != base else None
                trail = status_changes(trail, status, clock())
                if status and status != shown:
                    refresh_commands(p, tab)
                    shown = status
                if status in v.TERMINAL and now - started >= S20_MIN_SECONDS:
                    break
                if now - started >= S20_FINISH_SECONDS:
                    break
                con.hold(0.8)
            con.hold(1.5)
            scope.status_trail = trail
    finally:
        watch.close()
    return True


# ---------------------------------------------------------------------------------------------
# entry points


def run_shots(con, shots, send=False):
    sent = False
    if "s17" in shots or "s18" in shots:
        shoot_board(con) if "s17" in shots else con.open_board()
        if "s18" in shots:
            shoot_drill(con)
    if "s19" in shots or "s20" in shots:
        sent = shoot_command(con, send=send and "s20" in shots)
    return sent


def wait_alarm_row(con, seconds=20):
    """True when the board's alarm table shows the tyre alarm (a reload takes a moment to fetch it)."""
    try:
        con.page.locator("tr", has_text="tyre-pressure-low").first.wait_for(timeout=int(seconds * 1000))
        return True
    except Exception:  # noqa: BLE001
        return False


def run_take(args, say=lambda m: print(m, file=sys.stderr, flush=True)):
    """Follows a take's log: S17 and S18 at the tyre alarm, then S19 and S20 when it is time for the operator. Returns an exit status."""
    out = os.path.join(args.out, "console")
    watcher = LogWatcher(args.log, {"tyre": is_tyre_alarm_step, "operator": v.is_operator_step})
    shots = set(args.shots.split(","))
    con = None
    secrets = ()
    try:
        con = Console(out_dir=out)
        secrets = con.secrets
        with con:
            say("console: signing in")
            con.sign_in()
            con.open_board()
            say("console: signed in, the board is open; waiting for the tyre alarm")
            if shots & {"s17", "s18"}:
                if not watcher.wait("tyre", timeout=args.wait_timeout):
                    say("console: the player never said the tyre alarm was active: no S17/S18")
                else:
                    con.page.reload()
                    con.wait_board_ready()
                    if not wait_alarm_row(con):
                        say("console: the board does not show the tyre alarm yet; recording the board anyway")
                    if "s17" in shots:
                        shoot_board_open(con)
                    if "s18" in shots:
                        try:
                            shoot_drill(con)
                        except CaptureError as e:
                            say("console: S18 not recorded: " + scrub(e, secrets))
            if shots & {"s19", "s20"}:
                say("console: waiting for the operator step")
                if not watcher.wait("operator", timeout=args.wait_timeout):
                    say("console: the player never said it was time for the operator: nothing sent")
                    return EXIT_FAILED_BEFORE_SEND
                shoot_command(con, send=not args.no_send)
                if con.command_sent:
                    say("console: the command was sent from the console")
    except Exception as e:  # noqa: BLE001
        say("console: failed: " + (scrub(e, secrets).splitlines() or [type(e).__name__])[0])
        return EXIT_FAILED_AFTER_SEND if con is not None and con.command_sent else EXIT_FAILED_BEFORE_SEND
    return EXIT_OK


def shoot_board_open(con):
    """S17 with the board already open."""
    with con.record("s17") as clip:
        clip.note("the Site Pulse board (console, Dashboards, Site Pulse), whole board")
        con.hold(S17_SECONDS)


def run_shot_cmd(args, say=lambda m: print(m, file=sys.stderr, flush=True)):
    out = os.path.join(args.out, "console")
    shots = set(args.shots.split(","))
    with Console(out_dir=out) as con:
        con.sign_in()
        say("console: signed in")
        run_shots(con, shots, send=args.send)
        path = con.write_manifest()
    say("console: wrote " + path)
    return EXIT_OK


def main(argv=None):
    p = argparse.ArgumentParser(description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
    sub = p.add_subparsers(dest="mode", required=True)
    for name in ("take", "shot"):
        s = sub.add_parser(name)
        s.add_argument("--out", required=True, help="the take's out directory; clips go to <out>/console/")
        s.add_argument("--shots", default=",".join(SHOTS), help="comma list of s17,s18,s19,s20")
    t = sub.choices["take"]
    t.add_argument("--log", required=True)
    t.add_argument("--wait-timeout", type=float, default=2700)
    t.add_argument("--no-send", action="store_true", help="fill the form but never press Send")
    sub.choices["shot"].add_argument("--send", action="store_true", help="s20 only: really press Send (default: the form is filled, never sent)")
    args = p.parse_args(argv)
    bad = set(args.shots.split(",")) - set(SHOTS)
    if bad:
        p.error("unknown shot " + ",".join(sorted(bad)))
    try:
        return run_take(args) if args.mode == "take" else run_shot_cmd(args)
    except CaptureError as e:
        print("console: " + str(e), file=sys.stderr)
        return EXIT_FAILED_BEFORE_SEND


if __name__ == "__main__":
    sys.exit(main())
