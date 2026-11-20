# Copyright The DeviceChain Authors
# SPDX-License-Identifier: Apache-2.0
"""python3 check_director_log.py /path/to/director.ndjson"""
import json
import sys


def check(path):
    failures, sequence, missed, events = [], [], [], []
    eof = 0
    with open(path, encoding="utf-8-sig") as stream:
        for line_number, line in enumerate(stream, 1):
            try:
                row = json.loads(line)
                eof = max(eof, row.get("wallS", 0))
                if row["kind"] != "director":
                    continue
                e = row["data"]
                kind, page, at = e["kind"], e["page"], e["showS"]
                events.append(e)
                eof = max(eof, at)
                if kind in ("shot", "resume"):
                    sequence.append(f"{at:g}s page {page} / {e['value']}")
                if kind == "missed":
                    missed.append(f"{at:g}s page {page}: {e['value']}")
            except (ValueError, KeyError, TypeError) as error:
                failures.append(f"line {line_number}: {error}")
    rendered = any(e["kind"] == "visible-caption" for e in events)
    active, captions = None, 0

    def close(at):
        nonlocal active
        if active is not None:
            duration = at - active["showS"]
            if duration < 3 - 1e-9:
                failures.append(f"page {active['page']} caption at {active['showS']}: visible {duration:g} s: {active['value']}")
            active = None

    for e in events:
        kind, page, at = e["kind"], e["page"], e["showS"]
        caption = kind == "visible-caption" if rendered else kind in ("caption", "title")
        end = kind == "visible-caption-end" if rendered else kind in ("caption-end", "page-end") or kind == "state" and e["value"] == "Presented"
        if caption:
            close(at)
            active, captions = e, captions + 1
        elif kind == "recording-end" or end and active is not None and active["page"] == page:
            close(at)
    close(eof)
    if captions == 0:
        failures.append("empty acceptance log: no captions")
    return failures, sequence, missed


if __name__ == "__main__":
    if len(sys.argv) != 2:
        sys.exit("usage: python3 check_director_log.py director.ndjson")
    errors, shots, skipped = check(sys.argv[1])
    print("Page/shot sequence:")
    print("\n".join(shots))
    print("\n".join(skipped))
    for error in errors:
        print(error, file=sys.stderr)
    print(f"caption floor: {'FAIL' if errors else 'PASS'} ({len(errors)} failures)")
    sys.exit(bool(errors))
