#!/usr/bin/env python3
# Copyright The DeviceChain Authors
# SPDX-License-Identifier: Apache-2.0
"""
Console stills for the Towerpulse story: a headless browser signs in to the console through its real sign-in form and saves PNGs of the screens the
film and the docs need. Run it against a live sim (acceptance.sh keeps one up, or `dcctl sim create --manifest towerpulse` and the player).

    console_stills.py --sim NAME --out DIR [--as operator|owner|both] [--base http://localhost:18080]

    operator   the sim's tenant user:  operator-board  alarm-list  command-detail
    owner      the annex owner:        annex-owner-board  annex-owner-devices  annex-owner-alarms

The operator is the sim record's tenant admin; the owner is owner+<tenant>@halden-labs.example with the same password (a customer user: a read-only member
scoped to the annex). Both are read from the sim's record HERE, typed into the form, and forgotten: nothing prints, logs or stores them, no trace or video is
recorded, and every message that leaves this module passes scrub(). The sign-in itself is never shot.

Browser: Playwright's Chromium from the venv that tools/console-recorder-setup.sh makes (the Sitepulse recorder's). The shot layouts follow Sitepulse's
console_capture.py: a board is laid out at 2400x2900 CSS pixels and drawn at 0.8, so the whole of Towerpulse's tall board is in one 1920x2320 image.
"""

import argparse
import json
import os
import re
import sys

BASE = os.environ.get("TP_CONSOLE_URL", "http://localhost:18080")
OWNER_BASE = "owner@halden-labs.example"
BOARD = (2400, 2900, 0.8)
PAGE = (1920, 1080, 1.0)
HOT_DEVICE_TOKEN = "tp-vav-tower-f4-east"

_JWT = re.compile(r"eyJ[A-Za-z0-9_-]+\.[A-Za-z0-9_-]+\.[A-Za-z0-9_-]*")


class CaptureError(Exception):
    pass


def scrub(text, secrets=()):
    text = _JWT.sub("jwt:…", str(text))
    for s in secrets:
        if s:
            text = text.replace(s, "…")
    return text


def read_record(name):
    with open(os.path.join(os.path.expanduser("~"), ".devicechain", "sims", name + ".json"), encoding="utf-8") as f:
        return json.load(f)


def identities(record, who):
    """(label, email, password) for each user to shoot, from the sim's record."""
    tenant, pw = record["tenant"], record["simPassword"]
    out = []
    if who in ("operator", "both"):
        out.append(("operator", record["simEmail"], pw))
    if who in ("owner", "both"):
        out.append(("owner", OWNER_BASE.replace("@", "+" + tenant + "@"), pw))
    return out


def board_settled(page):
    """The board is drawn once the network is idle and its widgets have had a moment to subscribe and paint."""
    page.wait_for_load_state("networkidle")
    page.wait_for_timeout(5000)


def sign_in(page, base, email, password, secrets):
    try:
        page.goto(base + "/login")
        page.fill("#email", email)
        if not page.is_visible("#password"):
            page.click("button[type=submit]")
            page.wait_for_selector("#password", state="visible", timeout=15000)
        page.fill("#password", password)
        page.click("button[type=submit]")
        page.wait_for_url(lambda u: "/login" not in u, timeout=30000)
        page.wait_for_load_state("networkidle")
    except Exception as e:  # noqa: BLE001
        raise CaptureError("the console did not sign in: " + scrub(e, secrets).splitlines()[0])


def shoot(page, out, name, layout=None):
    path = os.path.join(out, name + ".png")
    page.screenshot(path=path)
    print("wrote " + path, file=sys.stderr)
    return path


def run_user(pw, base, out, label, email, password):
    secrets = (email, password)
    taken = []
    browser = pw.chromium.launch(headless=True, args=["--hide-scrollbars", "--force-color-profile=srgb"])
    try:
        for kind, (w, h, scale) in (("page", PAGE), ("board", BOARD)):
            ctx = browser.new_context(viewport={"width": w, "height": h}, device_scale_factor=scale, color_scheme="dark", timezone_id="UTC")
            page = ctx.new_page()
            try:
                sign_in(page, base, email, password, secrets)
                if kind == "board" and label == "operator":
                    page.goto(base + "/dashboards/tp-dashboard")
                    board_settled(page)
                    taken.append(shoot(page, out, "operator-board"))
                elif kind == "board":
                    page.goto(base + "/dashboards")
                    page.wait_for_load_state("networkidle")
                    page.goto(base + "/dashboards/tp-annex-dashboard")
                    board_settled(page)
                    taken.append(shoot(page, out, "annex-owner-board"))
                elif label == "operator":
                    page.goto(base + "/alarms")
                    page.wait_for_load_state("networkidle")
                    page.wait_for_timeout(2000)
                    taken.append(shoot(page, out, "alarm-list"))
                    page.goto(base + "/devices/" + HOT_DEVICE_TOKEN)
                    page.wait_for_load_state("networkidle")
                    tab = page.get_by_role("tab", name=re.compile(r"^commands$", re.I)).first
                    tab.wait_for(timeout=20000)
                    tab.click()
                    page.wait_for_timeout(3000)
                    taken.append(shoot(page, out, "command-detail"))
                else:
                    page.goto(base + "/devices")
                    page.wait_for_load_state("networkidle")
                    page.wait_for_timeout(2500)
                    taken.append(shoot(page, out, "annex-owner-devices"))
                    page.goto(base + "/alarms")
                    page.wait_for_load_state("networkidle")
                    page.wait_for_timeout(2500)
                    taken.append(shoot(page, out, "annex-owner-alarms"))
            except CaptureError:
                raise
            except Exception as e:  # noqa: BLE001
                raise CaptureError("%s %s view: %s" % (label, kind, scrub(e, secrets).splitlines()[0]))
            finally:
                ctx.close()
    finally:
        browser.close()
    return taken


def main(argv=None):
    ap = argparse.ArgumentParser(description=__doc__.split("\n")[1])
    ap.add_argument("--sim", required=True)
    ap.add_argument("--out", required=True)
    ap.add_argument("--as", dest="who", choices=["operator", "owner", "both"], default="both")
    ap.add_argument("--base", default=BASE)
    a = ap.parse_args(argv)
    from playwright.sync_api import sync_playwright

    os.makedirs(a.out, exist_ok=True)
    record = read_record(a.sim)
    with sync_playwright() as pw:
        for label, email, password in identities(record, a.who):
            try:
                run_user(pw, a.base.rstrip("/"), a.out, label, email, password)
            except CaptureError as e:
                print("capture failed: " + scrub(e, (email, password)), file=sys.stderr)
                return 1
    return 0


if __name__ == "__main__":
    sys.exit(main())
