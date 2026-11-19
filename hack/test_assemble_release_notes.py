# Copyright The DeviceChain Authors
# SPDX-License-Identifier: Apache-2.0
"""Tests for hack/assemble-release-notes.py.

    cd hack && python3 -m unittest -v test_assemble_release_notes

Golden in, golden out: testdata/release-notes/input is a tree with three locale pages and four
fragments; assembling it must reproduce testdata/release-notes/expected byte for byte (the es
input carries a byte-order mark, which must survive) and delete the fragments but not README.md.
Then one test per refusal, each planted alone on a copy of the same input.
"""
import importlib.util
import os
import shutil
import subprocess
import sys
import tempfile
import unittest

HERE = os.path.dirname(os.path.abspath(__file__))
SCRIPT = os.path.join(HERE, "assemble-release-notes.py")
INPUT = os.path.join(HERE, "testdata", "release-notes", "input")
EXPECTED = os.path.join(HERE, "testdata", "release-notes", "expected")

spec = importlib.util.spec_from_file_location("assemble_release_notes", SCRIPT)
arn = importlib.util.module_from_spec(spec)
spec.loader.exec_module(arn)

GOOD = """---
section: whats-new
---

## en

- Good.

## es

- Bueno.

## zh-CN

- 好。
"""


def read_bytes(path):
    with open(path, "rb") as f:
        return f.read()


class Base(unittest.TestCase):
    def setUp(self):
        self.tmp = tempfile.mkdtemp(prefix="arn-test-")
        self.root = os.path.join(self.tmp, "root")
        shutil.copytree(INPUT, self.root)
        self.frags = os.path.join(self.root, arn.FRAGMENTS_REL)

    def tearDown(self):
        shutil.rmtree(self.tmp, ignore_errors=True)

    def pages(self):
        return {l: read_bytes(arn.page_path(self.root, l)) for l in arn.LOCALES}

    def plant(self, name, content):
        with open(os.path.join(self.frags, name), "wb") as f:
            f.write(content.encode("utf-8"))

    def assertRefused(self, fragment_name, content, *needles):
        """The planted fragment is refused by --check AND by assembly, which writes nothing."""
        self.plant(fragment_name, content)
        before = self.pages()
        fragments_before = sorted(os.listdir(self.frags))
        with self.assertRaises(arn.Refusal) as ctx:
            arn.check(self.root)
        for needle in needles:
            self.assertIn(needle, str(ctx.exception))
        with self.assertRaises(arn.Refusal):
            arn.assemble(self.root)
        self.assertEqual(self.pages(), before, "a refused assembly must not write any page")
        self.assertEqual(sorted(os.listdir(self.frags)), fragments_before,
                         "a refused assembly must not delete any fragment")


class GoldenTest(Base):
    def test_golden(self):
        arn.assemble(self.root)
        for locale in arn.LOCALES:
            rel = os.path.relpath(arn.page_path(self.root, locale), self.root)
            got = read_bytes(os.path.join(self.root, rel))
            want = read_bytes(os.path.join(EXPECTED, rel))
            self.assertEqual(got, want, f"{locale} page differs from the golden")
            self.assertNotIn(b"\r\n", got)
        self.assertEqual(os.listdir(self.frags), ["README.md"])

    def test_bom_state_is_preserved(self):
        arn.assemble(self.root)
        self.assertTrue(read_bytes(arn.page_path(self.root, "es")).startswith(arn.BOM))
        self.assertFalse(read_bytes(arn.page_path(self.root, "en")).startswith(arn.BOM))
        self.assertFalse(read_bytes(arn.page_path(self.root, "zh-CN")).startswith(arn.BOM))

    def test_deterministic_regardless_of_listing_order(self):
        # Re-create the fragments in reverse order; the output must not change.
        names = sorted(n for n in os.listdir(self.frags) if n != "README.md")
        contents = {n: read_bytes(os.path.join(self.frags, n)) for n in names}
        for n in names:
            os.remove(os.path.join(self.frags, n))
        for n in reversed(names):
            with open(os.path.join(self.frags, n), "wb") as f:
                f.write(contents[n])
        self.test_golden()

    def test_deterministic_when_the_filesystem_lists_in_reverse(self):
        # NTFS lists names sorted already, so the test above cannot see a missing sort there.
        real = os.listdir
        arn.os.listdir = lambda p: sorted(real(p), reverse=True)
        try:
            self.test_golden()
        finally:
            arn.os.listdir = real

    def test_crlf_fragment_is_written_as_lf(self):
        path = os.path.join(self.frags, "a-whats-new-bullet.md")
        data = read_bytes(path).replace(b"\n", b"\r\n")
        with open(path, "wb") as f:
            f.write(data)
        self.test_golden()

    def test_no_fragments_changes_nothing(self):
        for n in os.listdir(self.frags):
            if n != "README.md":
                os.remove(os.path.join(self.frags, n))
        before = self.pages()
        arn.assemble(self.root)
        self.assertEqual(self.pages(), before)

    def test_check_writes_nothing(self):
        before = self.pages()
        listing = sorted(os.listdir(self.frags))
        fragments, _ = arn.check(self.root)
        self.assertEqual(len(fragments), 4)
        self.assertEqual(self.pages(), before)
        self.assertEqual(sorted(os.listdir(self.frags)), listing)


class RefusalTest(Base):
    def test_missing_locale(self):
        self.assertRefused("x.md", GOOD.split("## zh-CN")[0], "missing locale(s) zh-CN")

    def test_duplicate_locale(self):
        self.assertRefused("x.md", GOOD + "\n## es\n\n- Otra vez.\n", "locale es is given twice")

    def test_unknown_locale(self):
        self.assertRefused("x.md", GOOD + "\n## fr\n\n- Bon.\n", "not a locale marker")

    def test_empty_locale_body(self):
        self.assertRefused("x.md", GOOD.replace("- Bueno.", ""), "the es body is empty")

    def test_unknown_section(self):
        self.assertRefused("x.md", GOOD.replace("whats-new", "highlights"),
                           "unknown section 'highlights'")

    def test_missing_section(self):
        self.assertRefused("x.md", GOOD.replace("section: whats-new\n", ""), "has no 'section:'")

    def test_unknown_front_matter_key(self):
        self.assertRefused("x.md", GOOD.replace("---\n\n", "pr: 12\n---\n\n", 1),
                           "unknown front matter key 'pr'")

    def test_front_matter_does_not_parse(self):
        self.assertRefused("x.md", GOOD.replace("section: whats-new", "section whats-new"),
                           "does not parse")

    def test_no_front_matter(self):
        self.assertRefused("x.md", GOOD.split("---\n", 2)[2], "must start with '---'")

    def test_unclosed_front_matter(self):
        self.assertRefused("x.md", GOOD.replace("---\n\n## en", "\n## en", 1), "not closed")

    def test_adr_reference(self):
        self.assertRefused("x.md", GOOD.replace("- Bueno.", "- Bueno (ADR-" + "025)."),
                           "ADR reference", "line 11")

    def test_not_utf8(self):
        path = os.path.join(self.frags, "x.md")
        with open(path, "wb") as f:
            f.write(GOOD.replace("好", "").encode("utf-8") + b"\xff\xfe")
        with self.assertRaises(arn.Refusal) as ctx:
            arn.check(self.root)
        self.assertIn("not valid UTF-8", str(ctx.exception))

    def test_bad_file_name(self):
        self.assertRefused("Upgrade Note.md", GOOD, "not a fragment name")

    def test_non_markdown_file(self):
        self.assertRefused("x.txt", GOOD, "not a fragment name")

    def test_shallow_heading(self):
        body = GOOD.replace("- Good.", "### Too high {#v170-x}")
        self.assertRefused("x.md", body, "shallower than ####")

    def test_heading_without_anchor(self):
        body = (GOOD.replace("- Good.", "#### Title\n\n- Good.")
                    .replace("- Bueno.", "#### Título\n\n- Bueno.")
                    .replace("- 好。", "#### 标题\n\n- 好。"))
        self.assertRefused("x.md", body, "has no explicit {#anchor}")

    def test_locales_disagree_on_structure(self):
        body = (GOOD.replace("- Good.", "#### T {#v170-t}\n\n- Good.")
                    .replace("- Bueno.", "#### T {#v170-t}\n\n- Bueno."))
        self.assertRefused("x.md", body, "the zh-CN body's structure differs from en")

    def test_anchor_already_on_page(self):
        body = (GOOD.replace("- Good.", "#### T {#v170-earlier}\n\n- Good.")
                    .replace("- Bueno.", "#### T {#v170-earlier}\n\n- Bueno.")
                    .replace("- 好。", "#### T {#v170-earlier}\n\n- 好。"))
        self.assertRefused("x.md", body, "already on the en page")

    def test_anchor_in_two_fragments(self):
        self.assertRefused("zzz-copy.md", read_bytes(
            os.path.join(self.frags, "zz-subsection.md")).decode("utf-8"),
            "is also used by")

    def test_unclosed_fence(self):
        self.assertRefused("x.md", GOOD.replace("- Good.", "```\ncode"), "code fence is not closed")

    def test_page_without_in_development_section(self):
        path = arn.page_path(self.root, "zh-CN")
        text = read_bytes(path).decode("utf-8").replace("{#v170-whats-new}", "{#v170-other}")
        with open(path, "wb") as f:
            f.write(text.encode("utf-8"))
        with self.assertRaises(arn.Refusal) as ctx:
            arn.check(self.root)
        self.assertIn("no in-development section", str(ctx.exception))

    def test_missing_fragment_directory(self):
        shutil.rmtree(self.frags)
        with self.assertRaises(arn.Refusal) as ctx:
            arn.check(self.root)
        self.assertIn("fragment directory not found", str(ctx.exception))


class LayoutTest(Base):
    """The page layout --check holds, and --open-line, which keeps it."""

    def text(self, locale="en"):
        return read_bytes(arn.page_path(self.root, locale)).decode("utf-8").lstrip("﻿")

    def edit(self, old, new, locales=arn.LOCALES):
        for locale in locales:
            path = arn.page_path(self.root, locale)
            data = read_bytes(path).decode("utf-8")
            self.assertIn(old, data)
            with open(path, "wb") as f:
                f.write(data.replace(old, new, 1).encode("utf-8"))

    def freeze(self, versions, anchors=None):
        with open(os.path.join(self.root, "docs", "versions.json"), "w", encoding="utf-8") as f:
            f.write("[" + ", ".join(f'"{v}"' for v in versions) + "]")
        for v in versions:
            anchor = (anchors or {}).get(v, "v%s0-upgrade" % v.replace(".", ""))
            for locale in arn.LOCALES:
                path = (os.path.join(self.root, "docs", "versioned_docs", f"version-{v}",
                                     arn.PAGE_REL) if locale == "en" else
                        os.path.join(self.root, "docs", "i18n", locale,
                                     "docusaurus-plugin-content-docs", f"version-{v}", arn.PAGE_REL))
                os.makedirs(os.path.dirname(path), exist_ok=True)
                with open(path, "w", encoding="utf-8") as f:
                    f.write(f"### v{v}.0 {{#{anchor}}}\n")

    def test_open_minor_adds_a_line_without_retiring_below_three(self):
        arn.open_line(self.root, "v1.8.0")
        en = self.text()
        self.assertIn("- **[v1.8](#v180-upgrade)**: [v1.8.0](#v180-upgrade)\n"
                      "- **[v1.7](#v170-upgrade)**", en)
        self.assertLess(en.index("{#v180-upgrade}"), en.index("{#v170-upgrade}"))
        fragments, pages = arn.check(self.root)
        self.assertEqual({p.prefix for p in pages}, {"v180"})
        self.assertIn("{#v180-whats-new}", self.text("zh-CN"))
        self.assertTrue(read_bytes(arn.page_path(self.root, "es")).startswith(arn.BOM))

    def test_open_minor_stubs_the_fourth_line_keeping_its_anchors(self):
        self.freeze(["1.7", "1.6"])
        arn.open_line(self.root, "v1.8.0")
        before = {l: self.text(l) for l in arn.LOCALES}
        arn.open_line(self.root, "v1.9.0")
        for locale in arn.LOCALES:
            text = self.text(locale)
            self.assertEqual(arn.anchors_lost(before[locale], text), [])
            recent = text[text.index("{#recent-releases}"):text.index("{#older-releases}")]
            older = text[text.index("{#older-releases}"):]
            self.assertNotIn("v160", recent)
            self.assertNotIn("v1.6", text[:text.index("{#recent-releases}")])
            self.assertIn('<a id="v160-before-upgrade"></a>', older)
            self.assertIn("(/1.6/deployment/releases-and-upgrades#v160-upgrade)", older)
            self.assertIn("[v1.6.0](https://github.com/devicechain-io/devicechain/releases/tag/"
                          "v1.6.0)", older)
            self.assertLess(older.index("{#v160-upgrade}"), older.index("{#v150-upgrade}"))
        self.assertIn("### v1.6.0 — publicada {#v160-upgrade}", self.text("es"))
        arn.check(self.root)

    def test_the_newest_snapshot_is_linked_at_the_site_root(self):
        self.freeze(["1.6"])
        arn.open_line(self.root, "v1.8.0")
        arn.open_line(self.root, "v1.9.0")
        self.assertIn("(/deployment/releases-and-upgrades#v160-upgrade)", self.text())

    def test_a_line_without_a_snapshot_links_github_only(self):
        arn.open_line(self.root, "v1.8.0")
        arn.open_line(self.root, "v1.9.0")
        self.assertIn("GitHub release: [v1.6.0](", self.text())
        self.assertNotIn("documentation](", self.text())

    def test_a_snapshot_without_the_anchor_is_refused(self):
        self.freeze(["1.6"], anchors={"1.6": "v160-other"})
        arn.open_line(self.root, "v1.8.0")
        before = self.pages()
        with self.assertRaises(arn.Refusal) as ctx:
            arn.open_line(self.root, "v1.9.0")
        self.assertIn("has no {#v160-upgrade}", str(ctx.exception))
        self.assertEqual(self.pages(), before)

    def test_open_patch_joins_its_line(self):
        arn.open_line(self.root, "v1.6.1")
        en = self.text()
        self.assertIn("- **[v1.6](#v160-upgrade)**: [v1.6.1](#v161-upgrade) · "
                      "[v1.6.0](#v160-upgrade)", en)
        self.assertLess(en.index("{#v170-upgrade}"), en.index("{#v161-upgrade}"))
        self.assertLess(en.index("{#v161-upgrade}"), en.index("{#v160-upgrade}"))
        # The newest release with both targets is still the one fragments land in.
        _, pages = arn.check(self.root)
        self.assertEqual({p.prefix for p in pages}, {"v170"})

    def test_a_copied_patch_without_targets_does_not_capture_fragments(self):
        # A v1.7.1 section pasted from a release branch, above the line in development.
        self.edit("[v1.7.0](#v170-upgrade)\n", "[v1.7.1](#v171-upgrade) · [v1.7.0](#v170-upgrade)\n")
        self.edit("### v1.7.0", "### v1.7.1 — a patch {#v171-upgrade}\n\n- Fixed.\n\n### v1.7.0")
        _, pages = arn.check(self.root)
        self.assertEqual({p.prefix for p in pages}, {"v170"})
        arn.assemble(self.root)
        en = self.text()
        at = en.index("**First bullet.**")
        self.assertLess(en.index("{#v170-whats-new}"), at)
        self.assertLess(en.index("{#v171-upgrade}"), en.index("{#v170-upgrade}"))

    def test_an_index_link_to_the_wrong_anchor_is_refused(self):
        self.edit("[v1.7.0](#v170-upgrade)\n", "[v1.7.0](#v999-upgrade)\n")
        with self.assertRaises(arn.Refusal) as ctx:
            arn.check(self.root)
        self.assertIn("links v1.7.0 to #v999-upgrade", str(ctx.exception))

    def test_a_fragment_reusing_a_stub_anchor_is_refused(self):
        # A retired line keeps its sub-anchors as <a id>s; a fragment may not take one.
        self.edit("### v1.5.0", "<a id=\"v150-kept\"></a>\n\n### v1.5.0", locales=arn.LOCALES)
        body = (GOOD.replace("- Good.", "#### T {#v150-kept}\n\n- Good.")
                    .replace("- Bueno.", "#### T {#v150-kept}\n\n- Bueno.")
                    .replace("- 好。", "#### T {#v150-kept}\n\n- 好。"))
        self.assertRefused("x.md", body, "already on the en page")

    def test_a_patch_missing_from_the_index_is_refused(self):
        self.edit("### v1.7.0", "### v1.7.1 — a patch {#v171-upgrade}\n\n### v1.7.0")
        with self.assertRaises(arn.Refusal) as ctx:
            arn.check(self.root)
        self.assertIn("the release-lines index lists", str(ctx.exception))

    def test_four_lines_in_full_are_refused(self):
        arn.open_line(self.root, "v1.8.0")
        self.edit("[v1.6.0](#v160-upgrade)\n",
                  "[v1.6.0](#v160-upgrade)\n- **[v1.4](#v140-upgrade)**: [v1.4.0](#v140-upgrade)\n",
                  locales=["en"])
        self.edit("\n## Older releases", "\n### v1.4.0 — x {#v140-upgrade}\n\n## Older releases",
                  locales=["en"])
        with self.assertRaises(arn.Refusal) as ctx:
            arn.check(self.root)
        self.assertIn("4 release lines are documented in full", str(ctx.exception))

    def test_open_refusals_write_nothing(self):
        for version in ("v1.7.0", "v1.6.5", "v1.5.1", "v1.7", "1.8.0", "v1.6.0"):
            before = self.pages()
            with self.assertRaises(arn.Refusal, msg=version):
                arn.open_line(self.root, version)
            self.assertEqual(self.pages(), before, version)

    def test_anchors_lost_sees_a_dropped_anchor(self):
        # The negative control for the guard --open-line refuses on.
        text = self.text()
        self.assertEqual(arn.anchors_lost(text, text), [])
        self.assertEqual(arn.anchors_lost(text, text.replace(" {#v160-before-upgrade}", "")),
                         ["v160-before-upgrade"])
        self.assertEqual(arn.anchors_lost("```\n{#in-a-fence}\n```\n", ""), [])

    def test_open_line_refuses_when_a_stub_would_lose_an_anchor(self):
        arn.open_line(self.root, "v1.8.0")
        real = arn.all_anchors
        # Mutate the anchor collection the stub is built from, not the guard's own view.
        calls = {"n": 0}

        def lossy(text):
            calls["n"] += 1
            out = real(text)
            return [a for a in out if a != "v160-before-upgrade"] if "### v1.6.0" in text and \
                "{#recent-releases}" not in text else out
        arn.all_anchors = lossy
        try:
            before = self.pages()
            with self.assertRaises(arn.Refusal) as ctx:
                arn.open_line(self.root, "v1.9.0")
            self.assertIn("would lose the anchor(s) ['v160-before-upgrade']", str(ctx.exception))
            self.assertEqual(self.pages(), before)
        finally:
            arn.all_anchors = real


class CommandLineTest(Base):
    """The CI entry point: --check exits 1 on a planted bad fragment (the negative control)."""

    def run_script(self, *args):
        return subprocess.run([sys.executable, SCRIPT, *args, "--root", self.root],
                              capture_output=True, text=True, encoding="utf-8")

    def test_check_passes_on_good_input(self):
        r = self.run_script("--check")
        self.assertEqual(r.returncode, 0, r.stderr)

    def test_check_fails_on_planted_bad_fragment(self):
        self.plant("bad.md", GOOD.split("## es")[0])
        r = self.run_script("--check")
        self.assertEqual(r.returncode, 1)
        self.assertIn("bad.md: missing locale(s) es, zh-CN", r.stderr)

    def test_self_test_passes(self):
        r = subprocess.run([sys.executable, SCRIPT, "--self-test"], capture_output=True,
                           text=True, encoding="utf-8")
        self.assertEqual(r.returncode, 0, r.stdout + r.stderr)


if __name__ == "__main__":
    unittest.main()
