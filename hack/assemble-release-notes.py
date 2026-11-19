#!/usr/bin/env python3
# Copyright The DeviceChain Authors
# SPDX-License-Identifier: Apache-2.0
"""Release-notes fragments: each pull request adds ONE new file instead of editing the
release-notes page, and the release-notes pull request folds them into the page.

    hack/assemble-release-notes.py              # fold every fragment into the page, delete them
    hack/assemble-release-notes.py --check      # validate every fragment; writes nothing
    hack/assemble-release-notes.py --self-test  # prove --check fails on planted bad fragments
    hack/assemble-release-notes.py --open-line v1.8.0   # open a release; stub the 4th line
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

WHERE IT GOES. The page lists its release LINES (a minor release and its patches) in an index
under {#release-lines}, documents the newest three lines in full under {#recent-releases}, newest
release first, and keeps a short stub for each older line under {#older-releases}. The version a
fragment lands in is the newest `###` section under {#recent-releases} that carries both target
headings, anchored {#vNNN-before-upgrade} and {#vNNN-whats-new} with the same anchors in every
locale; a patch section copied in without them is skipped, so it never captures the fragments of
the line in development. A fragment without headings is a bullet (or paragraph) and goes at the
end of the target's lead content, before any subsection; a fragment with headings goes at the end
of the target's region. Fragments are taken in file-name order, bullets before subsections, so the
output depends only on the set of fragments.

THE LAYOUT, which --check also holds: every `###` under {#recent-releases} is a version heading
`### vX.Y.Z ... {#vXYZ-upgrade}`, newest first; at most three lines are there, each with its
`.0` release; and the index has one item per line, in the same order, listing exactly that line's
releases:

    - **[v1.7](#v170-upgrade)**: [v1.7.1](#v171-upgrade) · [v1.7.0](#v170-upgrade)

OPENING A RELEASE. `--open-line vX.Y.Z` writes an in-development section for the version, with
both target headings, in all three locales. A minor (`vX.Y.0`) opens a new line at the top and
then turns every line beyond the newest three into a stub under {#older-releases}: the line's
`.0` heading kept verbatim as the stub's heading, every other anchor in the line kept as an
`<a id>`, a link to that version's frozen docs (docs/versions.json) and to each of its GitHub
releases. A patch (`vX.Y.Z`, Z > 0) goes at the top of its own line, which must still be in full.
It refuses, writing nothing, if any anchor on a page would be lost or the result breaks the layout.

REFUSED, all before anything is written: a missing, duplicated or unknown locale; an unknown or
missing section; front matter that does not parse; an ADR reference (the same pattern as
hack/check-docs-adr-refs.sh — the assembled text is published); a heading shallower than ####
or without an anchor; an anchor already on the page or in another fragment; locales whose
structure differs; a file in the directory that is not a well-named fragment; a page whose
in-development section cannot be found; a page whose index and body disagree. Text is read and written as strict UTF-8 with LF line
endings, and a page's byte-order mark is kept exactly as found.

Standard library only.
"""
import argparse
import json
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


MAX_FULL_LINES = 3
GITHUB_RELEASE = "https://github.com/devicechain-io/devicechain/releases/tag/{tag}"
GITHUB_RELEASES = "https://github.com/devicechain-io/devicechain/releases"
VERSION_HEAD_RE = re.compile(r"^###\s+v(\d+)\.(\d+)\.(\d+)\b.*\{#(v\d+-upgrade)\}\s*$")
INDEX_ITEM_RE = re.compile(r"^- \*\*\[v(\d+)\.(\d+)\]\(#(v\d+-upgrade)\)\*\*: (.+)$")
INDEX_LINK_RE = re.compile(r"\[v(\d+)\.(\d+)\.(\d+)\]\(#(v\d+-upgrade)\)")
ANCHOR_ANY_RE = re.compile(r"\{#([^}\s]+)\}|<a id=\"([^\"]+)\"></a>")

# The text --open-line writes. A heading's words may be reworded on the page later; only the
# anchors are load-bearing.
TEXT = {
    "en": {
        "title": "### {v} — in development {{#{a}}}",
        "lead": "These notes are for the release in development and grow until it ships.",
        "before": "#### Before you upgrade to {v} {{#{p}-before-upgrade}}",
        "new": "#### What is new in {v} {{#{p}-whats-new}}",
        "docs": "Full notes: [{line} documentation]({url}). GitHub release: {gh}.",
        "nodocs": "GitHub release: {gh}.",
    },
    "es": {
        "title": "### {v} — en desarrollo {{#{a}}}",
        "lead": "Estas notas son de la versión en desarrollo y crecen hasta su publicación.",
        "before": "#### Antes de actualizar a {v} {{#{p}-before-upgrade}}",
        "new": "#### Novedades de {v} {{#{p}-whats-new}}",
        "docs": "Notas completas: [documentación de {line}]({url}). Versión en GitHub: {gh}.",
        "nodocs": "Versión en GitHub: {gh}.",
    },
    "zh-CN": {
        "title": "### {v} — 开发中 {{#{a}}}",
        "lead": "这些说明针对开发中的版本，会持续补充直到发布。",
        "before": "#### 升级到 {v} 前 {{#{p}-before-upgrade}}",
        "new": "#### {v} 新增内容 {{#{p}-whats-new}}",
        "docs": "完整说明：[{line} 文档]({url})。GitHub 发布：{gh}。",
        "nodocs": "GitHub 发布：{gh}。",
    },
}


def vstr(v):
    return "v%d.%d.%d" % v


def anchor_of(v):
    """The upgrade anchor for a version, as hack/build-release-notes-header.sh derives it."""
    return "v%d%d%d-upgrade" % v


def index_item(line, releases):
    links = " · ".join(f"[{vstr(v)}](#{anchor_of(v)})" for v in releases)
    return f"- **[v{line[0]}.{line[1]}](#{anchor_of(line + (0,))})**: {links}"


def stub_links(locale, line, releases, docs_url):
    gh = ", ".join(f"[{vstr(v)}]({GITHUB_RELEASE.format(tag=vstr(v))})" for v in releases)
    t = TEXT[locale]
    if docs_url:
        return t["docs"].format(line=f"v{line[0]}.{line[1]}", url=docs_url, gh=gh)
    return t["nodocs"].format(gh=gh)


def all_anchors(text):
    """Every id a link can land on outside a code fence: explicit heading ids and <a id>s."""
    out, fenced = [], False
    for line in text.split("\n"):
        if FENCE_RE.match(line):
            fenced = not fenced
            continue
        if not fenced:
            out += [m.group(1) or m.group(2) for m in ANCHOR_ANY_RE.finditer(line)]
    return out


def anchors_lost(before, after):
    """The anchors present in `before` and missing from `after`, in page order."""
    kept = set(all_anchors(after))
    return [a for a in all_anchors(before) if a not in kept]


class Page:
    """One locale's release-notes page: its layout and the in-development version's targets."""

    def __init__(self, root, locale, text=None):
        self.locale = locale
        self.path = page_path(root, locale)
        if text is None:
            if not os.path.isfile(self.path):
                raise Refusal(f"{self.path}: release-notes page not found")
            self.had_bom, text = read_utf8(self.path)
        else:
            self.had_bom = False
        self.text = text
        self.lines = text.split("\n")
        self.heads = list(headings(self.lines))
        # Every id on the page, including the <a id>s a stub keeps for a retired line.
        self.anchors = set(all_anchors(text))
        self.parse_layout()

        version = None
        for sec in self.sections:
            p = sec["anchor"][:-len("-upgrade")]
            found = {s: [i for i, _, a in self.heads if a == f"{p}-{s}"] for s in SECTIONS}
            if all(len(found[s]) == 1 and sec["start"] < found[s][0] < sec["end"]
                   for s in SECTIONS):
                version = sec
                break
        if version is None:
            raise Refusal(f"{self.path}: no release under Recent releases carries both "
                          f"{{#vNNN-before-upgrade}} and {{#vNNN-whats-new}} headings, so there "
                          f"is no in-development section to fold fragments into")
        self.prefix = version["anchor"][:-len("-upgrade")]
        self.version_end = version["end"]
        self.targets = {s: next(i for i, _, a in self.heads if a == f"{self.prefix}-{s}")
                        for s in SECTIONS}
        if not self.targets["before-upgrade"] < self.targets["whats-new"]:
            raise Refusal(f"{self.path}: {{#{self.prefix}-before-upgrade}} must come before "
                          f"{{#{self.prefix}-whats-new}}")

    def h2(self, anchor):
        found = [i for i, lvl, a in self.heads if a == anchor and lvl == 2]
        if len(found) != 1:
            raise Refusal(f"{self.path}: expected one ## heading anchored {{#{anchor}}}, found "
                          f"{len(found)}")
        start = found[0]
        end = next((i for i, lvl, _ in self.heads if i > start and lvl <= 2), len(self.lines))
        return start, end

    def parse_layout(self):
        """The index, the lines in full and the stubs; refuses a page where they disagree."""
        where = self.path
        self.index_region = self.h2("release-lines")
        self.recent_region = self.h2("recent-releases")
        self.older_region = self.h2("older-releases")
        if not self.index_region[0] < self.recent_region[0] < self.older_region[0]:
            raise Refusal(f"{where}: the sections must come in the order {{#release-lines}}, "
                          f"{{#recent-releases}}, {{#older-releases}}")

        rs, rend = self.recent_region
        h3 = [i for i, lvl, _ in self.heads if rs < i < rend and lvl <= 3]
        self.sections = []
        for n, i in enumerate(h3):
            m = VERSION_HEAD_RE.match(self.lines[i])
            if not m:
                raise Refusal(f"{where}: '{self.lines[i].strip()}' under Recent releases is not "
                              f"a version heading '### vX.Y.Z ... {{#vXYZ-upgrade}}'")
            v = tuple(int(g) for g in m.group(1, 2, 3))
            if m.group(4) != anchor_of(v):
                raise Refusal(f"{where}: {vstr(v)} is anchored {{#{m.group(4)}}}, not "
                              f"{{#{anchor_of(v)}}}")
            end = h3[n + 1] if n + 1 < len(h3) else rend
            self.sections.append({"v": v, "anchor": m.group(4), "start": i, "end": end})
        versions = [s["v"] for s in self.sections]
        if not versions:
            raise Refusal(f"{where}: no release under Recent releases")
        if versions != sorted(set(versions), reverse=True):
            raise Refusal(f"{where}: the releases under Recent releases are not newest first: "
                          + ", ".join(vstr(v) for v in versions))
        self.lines_in_full = []
        for v in versions:
            if not self.lines_in_full or self.lines_in_full[-1][0] != v[:2]:
                self.lines_in_full.append((v[:2], []))
            self.lines_in_full[-1][1].append(v)
        for line, releases in self.lines_in_full:
            if releases[-1][2] != 0:
                raise Refusal(f"{where}: the v{line[0]}.{line[1]} line has no "
                              f"v{line[0]}.{line[1]}.0 section under Recent releases")
        if len(self.lines_in_full) > MAX_FULL_LINES:
            raise Refusal(f"{where}: {len(self.lines_in_full)} release lines are documented in "
                          f"full; keep {MAX_FULL_LINES} (--open-line stubs the older ones)")

        s, e = self.index_region
        self.index_items = []
        for i in range(s + 1, e):
            m = INDEX_ITEM_RE.match(self.lines[i])
            if m:
                line = (int(m.group(1)), int(m.group(2)))
                links = [tuple(int(g) for g in l.group(1, 2, 3))
                         for l in INDEX_LINK_RE.finditer(m.group(4))]
                for l in INDEX_LINK_RE.finditer(m.group(4)):
                    v = tuple(int(g) for g in l.group(1, 2, 3))
                    if l.group(4) != anchor_of(v):
                        raise Refusal(f"{where}: the release-lines index links {vstr(v)} to "
                                      f"#{l.group(4)}, not #{anchor_of(v)}")
                self.index_items.append((i, line, m.group(3), links))
        rows = [i for i, _, _, _ in self.index_items]
        if rows and rows != list(range(rows[0], rows[0] + len(rows))):
            raise Refusal(f"{where}: the release-lines index items must be one list, with no "
                          f"line between them")
        want = [(line, anchor_of(line + (0,)), releases) for line, releases in self.lines_in_full]
        got = [(line, a, links) for _, line, a, links in self.index_items]
        if got != want:
            def show(items):
                return "; ".join(f"v{l[0]}.{l[1]}: " + ", ".join(vstr(v) for v in r)
                                 for l, _, r in items) or "(none)"
            raise Refusal(f"{where}: the release-lines index lists {show(got)}, but Recent "
                          f"releases holds {show(want)}. Each line is one item, newest first: "
                          f"{index_item((1, 7), [(1, 7, 1), (1, 7, 0)])} "
                          f"(--open-line keeps them in step)")

        os_, oend = self.older_region
        self.stub_versions = []
        for i, lvl, _ in self.heads:
            if os_ < i < oend and lvl == 3:
                m = VERSION_HEAD_RE.match(self.lines[i])
                if m:
                    self.stub_versions.append(tuple(int(g) for g in m.group(1, 2, 3)))
        oldest_full = self.lines_in_full[-1][0]
        newer = [v for v in self.stub_versions if v[:2] >= oldest_full]
        if newer:
            raise Refusal(f"{where}: Older releases has a stub for "
                          + ", ".join(vstr(v) for v in newer)
                          + ", a line still documented in full or newer")

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

    def opened(self, v, docs_url_for):
        """This page's text with release `v` opened (and a fourth line stubbed), or a Refusal."""
        where = self.path
        line = v[:2]
        every = [s["v"] for s in self.sections] + self.stub_versions
        if anchor_of(v) in set(all_anchors(self.text)):
            raise Refusal(f"{where}: {{#{anchor_of(v)}}} is already on the page")
        t = TEXT[self.locale]
        p = anchor_of(v)[:-len("-upgrade")]
        skeleton = [t["title"].format(v=vstr(v), a=anchor_of(v)), "", t["lead"], "",
                    t["before"].format(v=vstr(v), p=p), "", t["new"].format(v=vstr(v), p=p), ""]
        lines_in_full = [(l, list(r)) for l, r in self.lines_in_full]
        if v[2] == 0:
            if every and v <= max(every):
                raise Refusal(f"{where}: {vstr(v)} is not newer than {vstr(max(every))}, the "
                              f"newest release on the page")
            at = self.sections[0]["start"]
            lines_in_full.insert(0, (line, [v]))
        else:
            mine = [s for s in self.sections if s["v"][:2] == line]
            if not mine:
                raise Refusal(f"{where}: the v{line[0]}.{line[1]} line is not documented in full, "
                              f"so it takes no patch release here")
            if v != mine[0]["v"][:2] + (mine[0]["v"][2] + 1,):
                raise Refusal(f"{where}: {vstr(v)} does not follow {vstr(mine[0]['v'])}, the "
                              f"newest release of its line")
            at = mine[0]["start"]
            for l, r in lines_in_full:
                if l == line:
                    r.insert(0, v)

        lines = list(self.lines)
        retire = lines_in_full[MAX_FULL_LINES:]
        stubs = []
        for l, releases in retire:
            secs = [s for s in self.sections if s["v"][:2] == l]
            start, end = secs[0]["start"], secs[-1]["end"]
            block = "\n".join(lines[start:end])
            head = lines[secs[-1]["start"]].rstrip()
            keep = [a for a in all_anchors(block) if a != anchor_of(l + (0,))]
            stub = [head, ""]
            if keep:
                stub += ["".join(f'<a id="{a}"></a>' for a in keep), ""]
            stub += [stub_links(self.locale, l, releases, docs_url_for(self.locale, l)), ""]
            stubs += stub
        # Edit bottom-up so earlier indices hold: stubs, then the retired sections, then the new
        # section, then the index.
        if stubs:
            os_, oend = self.older_region
            first_stub = next((i for i, lvl, _ in self.heads if os_ < i < oend and lvl == 3),
                              None)
            if first_stub is None:
                raise Refusal(f"{where}: Older releases has no stub to place the retired line "
                              f"before")
            lines[first_stub:first_stub] = stubs
            secs = [s for s in self.sections if s["v"][:2] in {l for l, _ in retire}]
            del lines[secs[0]["start"]:secs[-1]["end"]]
        lines[at:at] = skeleton
        items = [i for i, _, _, _ in self.index_items]
        lines[items[0]:items[-1] + 1] = [index_item(l, r)
                                         for l, r in lines_in_full[:MAX_FULL_LINES]]
        text = "\n".join(lines)
        lost = anchors_lost(self.text, text)
        if lost:
            raise Refusal(f"{where}: opening {vstr(v)} would lose the anchor(s) {lost}")
        return text


def docs_url_finder(root):
    """(locale, line) -> the URL of that line's notes in its frozen docs, or None."""
    path = os.path.join(root, "docs", "versions.json")
    versions = []
    if os.path.isfile(path):
        with open(path, encoding="utf-8") as f:
            versions = json.load(f)

    def find(locale, line):
        name = f"{line[0]}.{line[1]}"
        if name not in versions:
            return None
        anchor = anchor_of(line + (0,))
        if locale == "en":
            frozen = os.path.join(root, "docs", "versioned_docs", f"version-{name}", PAGE_REL)
        else:
            frozen = os.path.join(root, "docs", "i18n", locale, "docusaurus-plugin-content-docs",
                                  f"version-{name}", PAGE_REL)
        if not os.path.isfile(frozen) or f"{{#{anchor}}}" not in read_utf8(frozen)[1]:
            raise Refusal(f"{frozen}: docs/versions.json lists {name}, but its frozen "
                          f"release-notes page has no {{#{anchor}}} to link the stub to")
        # The newest snapshot is served at the site root, the others under their name.
        base = "" if versions[0] == name else f"/{name}"
        return f"{base}/deployment/releases-and-upgrades#{anchor}"
    return find


def open_line(root, version):
    m = re.fullmatch(r"v(\d+)\.(\d+)\.(\d+)", version or "")
    if not m:
        raise Refusal(f"{version!r} is not a release version vX.Y.Z")
    v = tuple(int(g) for g in m.groups())
    pages = load_pages(root)
    if len({tuple((l, tuple(r)) for l, r in p.lines_in_full) for p in pages}) != 1:
        raise Refusal("the locales disagree on the releases under Recent releases")
    find = docs_url_finder(root)
    rendered = []
    for page in pages:
        text = page.opened(v, find)
        after = Page(root, page.locale, text)   # the result must hold the layout it was built for
        rendered.append((page, text))
    for page, text in rendered:
        write_utf8(page.path, page.had_bom, text)
    print(f"Opened {vstr(v)} in {len(pages)} page(s).")
    kept = {l for l, _ in after.lines_in_full}
    for l, _ in pages[0].lines_in_full:
        if l not in kept:
            print(f"  v{l[0]}.{l[1]} moved to Older releases as a stub.")


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
# planted tree, and requires a pass on a good fragment and a failure on each planted defect;
# then --open-line on the same tree, for a minor, a patch and four versions it must refuse.
# --------------------------------------------------------------------------------------------

SELF_TEST_PAGE = """---
title: Releases
---

# Releases

## Release lines {#release-lines}

- **[v9.2](#v920-upgrade)**: [v9.2.0](#v920-upgrade)
- **[v9.1](#v910-upgrade)**: [v9.1.1](#v911-upgrade) · [v9.1.0](#v910-upgrade)
- **[v9.0](#v900-upgrade)**: [v9.0.0](#v900-upgrade)

## Recent releases {#recent-releases}

### v9.2.0 — in development {#v920-upgrade}

#### Before {#v920-before-upgrade}

#### New {#v920-whats-new}

### v9.1.1 — a patch {#v911-upgrade}

### v9.1.0 — shipped {#v910-upgrade}

### v9.0.0 — the oldest line in full {#v900-upgrade}

#### Before you upgrade to v9.0.0 {#v900-before-upgrade}

<a id="v900-linked-from-outside"></a>

```bash
# v9.0.0 {#not-an-anchor}
```

## Older releases {#older-releases}

### v8.0.0 — a stub {#v800-upgrade}

Releases before v8.0.0 are not listed here.
"""

# Planted page defects --check must refuse, each a one-line edit of SELF_TEST_PAGE.
BAD_PAGES = {
    "index-misses-a-patch": ("[v9.1.1](#v911-upgrade) · ", ""),
    "out-of-order": ("### v9.1.1 — a patch {#v911-upgrade}",
                     "### v9.0.1 — a patch {#v901-upgrade}"),
    "no-older-releases": ("## Older releases {#older-releases}", "## Older {#older}"),
    "stub-of-a-full-line": ("### v8.0.0 — a stub {#v800-upgrade}",
                            "### v9.0.0 — a stub {#v900-upgrade}"),
}

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

    def run(tmp, *args):
        return subprocess.run([sys.executable, script, *args, "--root", tmp],
                              capture_output=True, text=True, encoding="utf-8")

    def page_text(tmp, locale="en"):
        return read_utf8(page_path(tmp, locale))[1]

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
        for case, (old, new) in BAD_PAGES.items():
            shutil.rmtree(tmp)
            plant(tmp, "good.md", GOOD)
            if old not in SELF_TEST_PAGE:
                failures.append(f"the planted page defect {case} edits nothing")
            for locale in LOCALES:
                with open(page_path(tmp, locale), "wb") as f:
                    f.write(SELF_TEST_PAGE.replace(old, new).encode("utf-8"))
            rc = run_check(tmp)
            print(f"  planted page {case}: --check exited {rc}")
            if rc != 1:
                failures.append(f"--check did not refuse a planted {case} page (exit {rc})")

        # --open-line: a minor stubs the fourth line, keeping every anchor; a patch joins its line.
        shutil.rmtree(tmp)
        plant(tmp, "good.md", GOOD)
        with open(os.path.join(tmp, "docs", "versions.json"), "w", encoding="utf-8") as f:
            json.dump(["9.1", "9.0"], f)
        for locale in LOCALES:
            frozen = (os.path.join(tmp, "docs", "versioned_docs", "version-9.0", PAGE_REL)
                      if locale == "en" else
                      os.path.join(tmp, "docs", "i18n", locale, "docusaurus-plugin-content-docs",
                                   "version-9.0", PAGE_REL))
            os.makedirs(os.path.dirname(frozen), exist_ok=True)
            with open(frozen, "w", encoding="utf-8") as f:
                f.write("### v9.0.0 {#v900-upgrade}\n")
        before = page_text(tmp)
        r = run(tmp, "--open-line", "v9.3.0")
        after = page_text(tmp)
        print(f"  --open-line v9.3.0: exited {r.returncode}")
        if r.returncode != 0:
            failures.append(f"--open-line v9.3.0 failed: {r.stderr.strip()}")
        else:
            older = after[after.index("{#older-releases}"):]
            recent = after[after.index("{#recent-releases}"):after.index("{#older-releases}")]
            if anchors_lost(before, after):
                failures.append(f"--open-line lost anchors {anchors_lost(before, after)}")
            if "### v9.0.0 — the oldest line in full {#v900-upgrade}" not in older:
                failures.append("--open-line did not move v9.0 into Older releases as a stub")
            if '<a id="v900-linked-from-outside"></a>' not in older:
                failures.append("the v9.0 stub does not keep the line's <a id> anchors")
            if "(/9.0/deployment/releases-and-upgrades#v900-upgrade)" not in older:
                failures.append("the v9.0 stub does not link the 9.0 frozen docs")
            if "v900" in recent or "{#v930-whats-new}" not in recent:
                failures.append("Recent releases is not v9.3 (in development), v9.2 and v9.1")
            if "{#not-an-anchor}" in after:
                failures.append("the retired line's code fence was carried into the stub")
            if run_check(tmp) != 0:
                failures.append("--check refused the page --open-line wrote")
        r = run(tmp, "--open-line", "v9.1.2")
        print(f"  --open-line v9.1.2: exited {r.returncode}")
        want = "- **[v9.1](#v910-upgrade)**: [v9.1.2](#v912-upgrade) · [v9.1.1](#v911-upgrade)"
        if r.returncode != 0 or want not in page_text(tmp, "zh-CN"):
            failures.append(f"--open-line v9.1.2 did not join the v9.1 line: {r.stderr.strip()}")
        # Negative controls: each must be refused, and must write nothing.
        for version in ("v9.2.5", "v9.0.1", "v9.3.0", "9.4.0"):
            before = page_text(tmp)
            r = run(tmp, "--open-line", version)
            print(f"  --open-line {version}: exited {r.returncode} (must refuse)")
            if r.returncode != 1 or page_text(tmp) != before:
                failures.append(f"--open-line {version} was not refused cleanly")
        # The anchor guard itself: a stub that dropped one anchor must be seen.
        dropped = SELF_TEST_PAGE.replace('<a id="v900-linked-from-outside"></a>', "")
        if anchors_lost(SELF_TEST_PAGE, dropped) != ["v900-linked-from-outside"]:
            failures.append("anchors_lost did not see a dropped <a id>")

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
    print("==> Self-test passed: --check refuses every planted bad fragment and page, and "
          "--open-line keeps every anchor.")
    return 0


def main(argv=None):
    ap = argparse.ArgumentParser(description=__doc__.split("\n")[0])
    mode = ap.add_mutually_exclusive_group()
    mode.add_argument("--check", action="store_true", help="validate fragments; write nothing")
    mode.add_argument("--self-test", action="store_true", help="prove --check can fail")
    mode.add_argument("--open-line", metavar="vX.Y.Z",
                      help="open a release in development; a minor stubs the fourth line")
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
        if args.open_line:
            open_line(args.root, args.open_line)
        elif args.check:
            fragments, pages = check(args.root)
            print(f"==> {len(fragments)} release-notes fragment(s) valid for the "
                  f"{pages[0].prefix} section.")
        else:
            assemble(args.root)
    except Refusal as e:
        print(f"==> Release notes refused:\n{e}", file=sys.stderr)
        return 1
    return 0


if __name__ == "__main__":
    sys.exit(main())
