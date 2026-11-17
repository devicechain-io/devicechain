#!/usr/bin/env python3
# Copyright The DeviceChain Authors
# SPDX-License-Identifier: Apache-2.0
"""Release-notes fragments: each pull request adds ONE new file instead of editing the
release-notes page, and the release-notes pull request folds them into the page.

    hack/assemble-release-notes.py              # fold every fragment into the page, delete them
    hack/assemble-release-notes.py --check      # validate every fragment; writes nothing
    hack/assemble-release-notes.py --self-test  # prove --check fails on planted bad fragments
    hack/assemble-release-notes.py --root DIR   # act on another tree (tests)

WHY. Nearly every pull request used to add a line to the in-development section of
docs/docs/deployment/releases-and-upgrades.md and both of its translations, under the same two
headings. Every merge then made every other open pull request conflict, and each refresh cost a
full CI run. A new file never conflicts with another new file.

THE FRAGMENT. docs/release-notes/unreleased/<slug>.md, where <slug> is lowercase letters,
digits and hyphens (README.md in that directory is the format's description, not a fragment):

    ---
    section: before-upgrade        # or: whats-new
    ---

    ## en

    - **Thing.** What changed and what to do.

    ## es

    - **Cosa.** ...

    ## zh-CN

    - **事项。** ...

The three bodies are inserted verbatim. A body may hold #### (or deeper) headings, each with an
explicit {#anchor}; the three locales must then agree on the headings' levels and anchors, on
the number of code fences and on the admonitions, because the docs structure-parity check
requires exactly that of the assembled page.

WHERE IT GOES. The current in-development version is the first `###` section under the page's
{#recent-releases} heading, `### v1.7.0 — ... {#v170-upgrade}`; its two targets are the headings
anchored {#v170-before-upgrade} and {#v170-whats-new}, which carry the same anchors in every
locale. A fragment without headings is a bullet (or paragraph) and goes at the end of the
target's lead content, before any subsection; a fragment with headings goes at the end of the
target's region. Fragments are taken in file-name order, bullets before subsections, so the
output depends only on the set of fragments.

REFUSED, all before anything is written: a missing, duplicated or unknown locale; an unknown or
missing section; front matter that does not parse; an ADR reference (the same pattern as
hack/check-docs-adr-refs.sh — the assembled text is published); a heading shallower than ####
or without an anchor; an anchor already on the page or in another fragment; locales whose
structure differs; a file in the directory that is not a well-named fragment; a page whose
in-development section cannot be found. Text is read and written as strict UTF-8 with LF line
endings, and a page's byte-order mark is kept exactly as found.

Standard library only.
"""
import argparse
import os
import re
import shutil
import subprocess
import sys
import tempfile

LOCALES = ("en", "es", "zh-CN")
SECTIONS = ("before-upgrade", "whats-new")
PAGE_REL = "deployment/releases-and-upgrades.md"
FRAGMENTS_REL = os.path.join("docs", "release-notes", "unreleased")
README = "README.md"

# Same pattern as PATTERN in hack/check-docs-adr-refs.sh; that guard also scans
# docs/release-notes, so a fragment is refused twice over.
ADR_RE = re.compile(r"ADR-[0-9]{3}")
NAME_RE = re.compile(r"^[a-z0-9][a-z0-9-]*\.md$")
FENCE_RE = re.compile(r"^\s*(```|~~~)")
HEADING_RE = re.compile(r"^(#{1,6})\s+(.*?)\s*$")
ANCHOR_RE = re.compile(r"\{#([^}\s]+)\}$")
ADMONITION_RE = re.compile(r"^\s*:::\s*([A-Za-z]+)")
LIST_ITEM_RE = re.compile(r"^\s*([-*+]|\d+\.)\s")
BOM = b"\xef\xbb\xbf"


class Refusal(Exception):
    """A fragment or page the assembler will not act on."""


def page_path(root, locale):
    if locale == "en":
        return os.path.join(root, "docs", "docs", PAGE_REL)
    return os.path.join(root, "docs", "i18n", locale, "docusaurus-plugin-content-docs",
                        "current", PAGE_REL)


def read_utf8(path):
    """Return (had_bom, text with LF line endings). Refuses anything not strict UTF-8."""
    with open(path, "rb") as f:
        data = f.read()
    had_bom = data.startswith(BOM)
    if had_bom:
        data = data[len(BOM):]
    try:
        text = data.decode("utf-8")
    except UnicodeDecodeError as e:
        raise Refusal(f"{path}: not valid UTF-8 ({e})")
    return had_bom, text.replace("\r\n", "\n")


def write_utf8(path, had_bom, text):
    data = (BOM if had_bom else b"") + text.encode("utf-8")
    tmp = path + ".assemble-tmp"
    with open(tmp, "wb") as f:
        f.write(data)
    os.replace(tmp, path)


def headings(lines):
    """Yield (index, level, anchor-or-None) for every heading outside a code fence."""
    fenced = False
    for i, line in enumerate(lines):
        if FENCE_RE.match(line):
            fenced = not fenced
            continue
        if fenced:
            continue
        m = HEADING_RE.match(line)
        if m:
            a = ANCHOR_RE.search(m.group(2))
            yield i, len(m.group(1)), (a.group(1) if a else None)


def structure(body):
    """What the docs structure-parity check compares, for one locale's body."""
    lines = body.split("\n")
    heads = [(lvl, anchor) for _, lvl, anchor in headings(lines)]
    fences = sum(1 for line in lines if FENCE_RE.match(line))
    admonitions = [m.group(1) for line in lines for m in [ADMONITION_RE.match(line)] if m]
    return heads, fences, admonitions


class Fragment:
    def __init__(self, path, name, section, bodies, anchors):
        self.path = path
        self.name = name
        self.section = section
        self.bodies = bodies          # locale -> body text, no leading/trailing blank lines
        self.anchors = anchors        # explicit heading anchors (same in every locale)

    @property
    def has_headings(self):
        return bool(self.anchors)


def parse_fragment(path):
    name = os.path.basename(path)
    _, text = read_utf8(path)

    adr = [f"line {n}: {line.strip()}" for n, line in enumerate(text.split("\n"), 1)
           if ADR_RE.search(line)]
    if adr:
        raise Refusal(f"{name}: ADR reference(s) — the docs site cannot link to them; say the "
                      f"thing itself: " + "; ".join(adr))

    lines = text.split("\n")
    if not lines or lines[0].strip() != "---":
        raise Refusal(f"{name}: must start with '---' front matter holding 'section:'")
    try:
        close = next(i for i in range(1, len(lines)) if lines[i].strip() == "---")
    except StopIteration:
        raise Refusal(f"{name}: front matter is not closed with '---'")

    meta = {}
    for raw in lines[1:close]:
        line = raw.strip()
        if not line or line.startswith("#"):
            continue
        if ":" not in line:
            raise Refusal(f"{name}: front matter line does not parse as 'key: value': {raw!r}")
        key, value = (s.strip() for s in line.split(":", 1))
        value = value.split(" #", 1)[0].strip().strip("'\"")
        if key != "section":
            raise Refusal(f"{name}: unknown front matter key {key!r} (only 'section' is allowed)")
        if key in meta:
            raise Refusal(f"{name}: front matter key {key!r} is given twice")
        meta[key] = value
    if "section" not in meta:
        raise Refusal(f"{name}: front matter has no 'section:' (one of {', '.join(SECTIONS)})")
    section = meta["section"]
    if section not in SECTIONS:
        raise Refusal(f"{name}: unknown section {section!r} (one of {', '.join(SECTIONS)})")

    bodies, current, fenced = {}, None, False
    for raw in lines[close + 1:]:
        if FENCE_RE.match(raw):
            fenced = not fenced
        elif not fenced and raw.startswith("## "):
            locale = raw[3:].strip()
            if locale not in LOCALES:
                raise Refusal(f"{name}: '{raw.strip()}' is not a locale marker (one of "
                              f"{', '.join('## ' + l for l in LOCALES)}); headings inside a "
                              f"body must be #### or deeper")
            if locale in bodies:
                raise Refusal(f"{name}: locale {locale} is given twice")
            bodies[locale], current = [], locale
            continue
        if current is None:
            if raw.strip():
                raise Refusal(f"{name}: text before the first locale marker ('## en'): {raw!r}")
            continue
        bodies[current].append(raw)
    if fenced:
        raise Refusal(f"{name}: a code fence is not closed")

    missing = [l for l in LOCALES if l not in bodies]
    if missing:
        raise Refusal(f"{name}: missing locale(s) {', '.join(missing)} — every fragment carries "
                      f"en, es and zh-CN")
    out = {}
    for locale in LOCALES:
        body = "\n".join(bodies[locale]).strip("\n")
        if not body.strip():
            raise Refusal(f"{name}: the {locale} body is empty")
        out[locale] = body

    shapes = {}
    for locale in LOCALES:
        lines_l = out[locale].split("\n")
        for i, lvl, anchor in headings(lines_l):
            if lvl < 4:
                raise Refusal(f"{name} [{locale}]: heading '{lines_l[i].strip()}' is shallower "
                              f"than ####; the version's own headings already exist")
            if anchor is None:
                raise Refusal(f"{name} [{locale}]: heading '{lines_l[i].strip()}' has no "
                              f"explicit {{#anchor}}; the translated pages need the same one")
        shapes[locale] = structure(out[locale])
    for locale in LOCALES[1:]:
        if shapes[locale] != shapes["en"]:
            raise Refusal(f"{name}: the {locale} body's structure differs from en (headings and "
                          f"anchors {shapes[locale][0]} vs {shapes['en'][0]}; code fences "
                          f"{shapes[locale][1]} vs {shapes['en'][1]}; admonitions "
                          f"{shapes[locale][2]} vs {shapes['en'][2]})")
    anchors = [a for _, a in shapes["en"][0]]
    if len(set(anchors)) != len(anchors):
        raise Refusal(f"{name}: an anchor is used twice: {anchors}")
    return Fragment(path, name, section, out, anchors)


def load_fragments(root):
    """Every fragment, parsed and sorted by name. All refusals are collected, not just the first."""
    directory = os.path.join(root, FRAGMENTS_REL)
    if not os.path.isdir(directory):
        # Fail closed: a check pointed at the wrong path would otherwise read as clean.
        raise Refusal(f"{directory}: fragment directory not found")
    fragments, problems = [], []
    for entry in sorted(os.listdir(directory)):
        path = os.path.join(directory, entry)
        if entry == README:
            continue
        if not os.path.isfile(path) or not NAME_RE.match(entry):
            problems.append(f"{entry}: not a fragment name — use <slug>.md with lowercase "
                            f"letters, digits and hyphens")
            continue
        try:
            fragments.append(parse_fragment(path))
        except Refusal as e:
            problems.append(str(e))
    seen = {}
    for frag in fragments:
        for a in frag.anchors:
            if a in seen:
                problems.append(f"{frag.name}: anchor {{#{a}}} is also used by {seen[a]}")
            seen[a] = frag.name
    if problems:
        raise Refusal("\n".join(problems))
    return fragments


class Page:
    """One locale's release-notes page and the in-development version's two target headings."""

    def __init__(self, root, locale):
        self.locale = locale
        self.path = page_path(root, locale)
        if not os.path.isfile(self.path):
            raise Refusal(f"{self.path}: release-notes page not found")
        self.had_bom, text = read_utf8(self.path)
        self.lines = text.split("\n")
        heads = list(headings(self.lines))
        self.anchors = {a for _, _, a in heads if a}

        recent = [i for i, _, a in heads if a == "recent-releases"]
        if len(recent) != 1:
            raise Refusal(f"{self.path}: expected one {{#recent-releases}} heading, found "
                          f"{len(recent)}")
        version = next(((i, a) for i, lvl, a in heads if i > recent[0] and lvl == 3), None)
        m = re.fullmatch(r"(v[0-9]+)-upgrade", version[1] or "") if version else None
        if not m:
            raise Refusal(f"{self.path}: the first ### under Recent releases is not an "
                          f"in-development version heading anchored {{#vNNN-upgrade}}")
        self.prefix = m.group(1)
        v_idx = version[0]
        self.version_end = next((i for i, lvl, _ in heads if i > v_idx and lvl <= 3),
                                len(self.lines))
        self.targets = {}
        for section in SECTIONS:
            anchor = f"{self.prefix}-{section}"
            found = [i for i, lvl, a in heads if a == anchor]
            if len(found) != 1 or not v_idx < found[0] < self.version_end:
                raise Refusal(f"{self.path}: expected one {{#{anchor}}} heading inside the "
                              f"{self.prefix} section, found {len(found)}")
            self.targets[section] = found[0]
        if not self.targets["before-upgrade"] < self.targets["whats-new"]:
            raise Refusal(f"{self.path}: {{#{self.prefix}-before-upgrade}} must come before "
                          f"{{#{self.prefix}-whats-new}}")
        self.heads = heads

    def region(self, section):
        """(heading index, end of lead content, end of region) for one target."""
        start = self.targets[section]
        end = self.targets["whats-new"] if section == "before-upgrade" else self.version_end
        lead_end = next((i for i, _, _ in self.heads if start < i < end), end)
        return start, lead_end, end

    def render(self, fragments):
        lines = list(self.lines)
        # Bottom region first, so the earlier region's indices still hold.
        for section in reversed(SECTIONS):
            mine = [f for f in fragments if f.section == section]
            if not mine:
                continue
            start, lead_end, end = self.region(section)
            plain = [f.bodies[self.locale] for f in mine if not f.has_headings]
            subsections = [f.bodies[self.locale] for f in mine if f.has_headings]
            lead = "\n".join(lines[start:lead_end]).strip("\n")
            tail = "\n".join(lines[lead_end:end]).strip("\n")
            blocks = [lead] + plain + ([tail] if tail else []) + subsections
            new = join_blocks(blocks).split("\n")
            new += [""] if end < len(lines) else []
            lines[start:end] = new
        text = "\n".join(lines)
        if self.lines[-1] == "" and not text.endswith("\n"):
            text += "\n"
        return text


def join_blocks(blocks):
    """Blank line between blocks, except that consecutive list items stay one tight list."""
    out = blocks[0]
    for block in blocks[1:]:
        prev_last = out.rsplit("\n", 1)[-1]
        first = block.split("\n", 1)[0]
        tight = LIST_ITEM_RE.match(first) and (LIST_ITEM_RE.match(prev_last)
                                               or prev_last.startswith((" ", "\t")))
        out += ("\n" if tight else "\n\n") + block
    return out


def load_pages(root):
    pages = [Page(root, l) for l in LOCALES]
    prefixes = {p.prefix for p in pages}
    if len(prefixes) != 1:
        raise Refusal("the locales disagree on the in-development version: " +
                      ", ".join(f"{p.locale}={p.prefix}" for p in pages))
    return pages


def check_against_pages(fragments, pages):
    problems = []
    for frag in fragments:
        for page in pages:
            clash = [a for a in frag.anchors if a in page.anchors]
            if clash:
                problems.append(f"{frag.name}: anchor(s) {clash} already on the {page.locale} page")
    if problems:
        raise Refusal("\n".join(sorted(set(problems))))


def check(root):
    fragments = load_fragments(root)
    pages = load_pages(root)
    check_against_pages(fragments, pages)
    return fragments, pages


def assemble(root):
    fragments, pages = check(root)
    if not fragments:
        print("No release-notes fragments to assemble.")
        return
    rendered = [(p, p.render(fragments)) for p in pages]   # everything is computed first
    for page, text in rendered:
        write_utf8(page.path, page.had_bom, text)
    for frag in fragments:
        os.remove(frag.path)
    print(f"Assembled {len(fragments)} fragment(s) into the {pages[0].prefix} section of "
          f"{len(pages)} page(s):")
    for frag in fragments:
        print(f"  {frag.section:<15} {frag.name}")


# --------------------------------------------------------------------------------------------
# --self-test: the negative control. It runs THIS script's --check, as CI does, against a
# planted tree, and requires a pass on a good fragment and a failure on each planted defect.
# --------------------------------------------------------------------------------------------

SELF_TEST_PAGE = """---
title: Releases
---

# Releases

## Recent releases {#recent-releases}

### v9.1.0 — in development {#v910-upgrade}

#### Before {#v910-before-upgrade}

#### New {#v910-whats-new}

### v9.0.0 {#v900-upgrade}
"""

GOOD = """---
section: whats-new
---

## en

- Good.

## es

- Bueno, también.

## zh-CN

- 好。
"""

BAD = {
    "missing-locale": GOOD.split("## zh-CN")[0],
    "unknown-section": GOOD.replace("section: whats-new", "section: highlights"),
    "adr-reference": GOOD.replace("- Good.", "- Good (ADR-" + "025)."),
    "unparsed-front-matter": GOOD.replace("section: whats-new", "section whats-new"),
    "no-front-matter": GOOD.split("---\n", 2)[2],
    "bad-name": None,  # a well-formed fragment under a name that is not <slug>.md
}


def self_test():
    script = os.path.abspath(__file__)

    def plant(tmp, name, content):
        for locale in LOCALES:
            p = page_path(tmp, locale)
            os.makedirs(os.path.dirname(p), exist_ok=True)
            with open(p, "wb") as f:
                f.write(SELF_TEST_PAGE.encode("utf-8"))
        d = os.path.join(tmp, FRAGMENTS_REL)
        os.makedirs(d, exist_ok=True)
        with open(os.path.join(d, name), "wb") as f:
            f.write(content.encode("utf-8"))

    def run_check(tmp):
        return subprocess.run([sys.executable, script, "--check", "--root", tmp],
                              capture_output=True, text=True, encoding="utf-8").returncode

    failures = []
    tmp = tempfile.mkdtemp(prefix="notes-selftest-")
    try:
        plant(tmp, "good.md", GOOD)
        if run_check(tmp) != 0:
            failures.append("a good fragment did not pass --check")
        for case, content in BAD.items():
            shutil.rmtree(tmp)
            name = "Bad Name.md" if content is None else f"{case}.md"
            plant(tmp, name, GOOD if content is None else content)
            rc = run_check(tmp)
            print(f"  planted {case}: --check exited {rc}")
            if rc != 1:
                failures.append(f"--check did not refuse a planted {case} fragment (exit {rc})")
        shutil.rmtree(tmp)
        os.makedirs(tmp)
        if run_check(tmp) == 0:
            failures.append("--check passed with no fragment directory and no pages")
    finally:
        shutil.rmtree(tmp, ignore_errors=True)
    if failures:
        for f in failures:
            print(f"  FAIL: {f}", file=sys.stderr)
        return 1
    print("==> Self-test passed: --check refuses every planted bad fragment.")
    return 0


def main(argv=None):
    ap = argparse.ArgumentParser(description=__doc__.split("\n")[0])
    mode = ap.add_mutually_exclusive_group()
    mode.add_argument("--check", action="store_true", help="validate fragments; write nothing")
    mode.add_argument("--self-test", action="store_true", help="prove --check can fail")
    ap.add_argument("--root", default=os.path.dirname(os.path.dirname(os.path.abspath(__file__))),
                    help="repository root (default: this checkout)")
    args = ap.parse_args(argv)
    # Messages quote es and zh-CN text; a Windows console codepage must not crash on them.
    for stream in (sys.stdout, sys.stderr):
        if hasattr(stream, "reconfigure"):
            stream.reconfigure(encoding="utf-8", errors="replace")
    if args.self_test:
        return self_test()
    try:
        if args.check:
            fragments, pages = check(args.root)
            print(f"==> {len(fragments)} release-notes fragment(s) valid for the "
                  f"{pages[0].prefix} section.")
        else:
            assemble(args.root)
    except Refusal as e:
        print(f"==> Release-notes fragments refused:\n{e}", file=sys.stderr)
        return 1
    return 0


if __name__ == "__main__":
    sys.exit(main())
