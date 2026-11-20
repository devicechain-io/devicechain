# Copyright The DeviceChain Authors
# SPDX-License-Identifier: Apache-2.0
"""python3 check_director_log.py /path/to/director.ndjson"""
import json
import sys


def check(path):
    active, failures, sequence, missed = {}, [], [], []
    with open(path, encoding="utf-8-sig") as stream:
        for line_number, line in enumerate(stream, 1):
            try:
                row = json.loads(line)
                if row["kind"] != "director":
                    continue
                e = row["data"]
                kind, page, at = e["kind"], e["page"], e["showS"]
                end = kind in ("caption", "caption-end", "page-end") or kind == "state" and e["value"] == "Presented"
                if end and page in active:
                    previous = active.pop(page)
                    duration = at - previous["showS"]
                    if duration < 3 - 1e-9:
                        failures.append(f"page {page} caption at {previous['showS']}: visible {duration:g} s: {previous['value']}")
                if kind == "caption":
                    active[page] = e
                if kind in ("shot", "resume"):
                    sequence.append(f"{at:g}s page {page} / {e['value']}")
                if kind == "missed":
                    missed.append(f"{at:g}s page {page}: {e['value']}")
            except (ValueError, KeyError, TypeError) as error:
                failures.append(f"line {line_number}: {error}")
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
