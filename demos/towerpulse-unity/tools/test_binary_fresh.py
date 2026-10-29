# Copyright The DeviceChain Authors
# SPDX-License-Identifier: Apache-2.0
"""A take never runs on a binary that predates the source it claims to show: binary_fresh.py refuses it."""

import os
import subprocess
import sys
import tempfile
import unittest

HERE = os.path.dirname(os.path.abspath(__file__))
sys.path.insert(0, HERE)
import binary_fresh as bf  # noqa: E402

NOW = 1_800_000_000.0


class Tree(unittest.TestCase):
    def setUp(self):
        self._tmp = tempfile.TemporaryDirectory()
        self.addCleanup(self._tmp.cleanup)
        self.src = os.path.join(self._tmp.name, "src")
        os.makedirs(self.src)
        self.binary = os.path.join(self._tmp.name, "tool.exe")

    def write(self, rel, mtime, body="x"):
        p = os.path.join(self.src, rel)
        os.makedirs(os.path.dirname(p), exist_ok=True)
        with open(p, "w") as f:
            f.write(body)
        os.utime(p, (mtime, mtime))
        return p

    def build(self, mtime):
        with open(self.binary, "w") as f:
            f.write("bin")
        os.utime(self.binary, (mtime, mtime))


class FreshnessTests(Tree):
    def test_a_binary_newer_than_every_source_is_fresh(self):
        self.write("main.go", NOW - 100)
        self.write("sim/board.go", NOW - 50)
        self.build(NOW)
        self.assertIsNone(bf.check(self.binary, [self.src]))

    def test_a_source_edited_after_the_build_makes_the_binary_stale_and_names_it(self):
        self.write("main.go", NOW - 100)
        edited = self.write("sim/towerpulse_annex_dashboard.go", NOW + 10)
        self.build(NOW)
        why = bf.check(self.binary, [self.src])
        self.assertIsNotNone(why)
        self.assertIn("towerpulse_annex_dashboard.go", why)

    def test_a_missing_binary_is_refused(self):
        self.write("main.go", NOW - 100)
        self.assertIn("does not exist", bf.check(self.binary, [self.src]))

    def test_a_missing_source_directory_is_refused_rather_than_taken_as_fresh(self):
        self.build(NOW)
        self.assertIn("cannot be shown", bf.check(self.binary, [os.path.join(self._tmp.name, "nope")]))

    def test_a_source_tree_with_no_files_is_refused_rather_than_taken_as_fresh(self):
        self.build(NOW)
        self.assertIn("cannot be shown", bf.check(self.binary, [self.src]))

    def test_editing_a_test_file_does_not_stale_the_binary(self):
        self.write("main.go", NOW - 100)
        self.write("main_test.go", NOW + 500)
        self.build(NOW)
        self.assertIsNone(bf.check(self.binary, [self.src]))

    def test_build_output_inside_a_source_directory_is_not_source(self):
        self.write("main.go", NOW - 100)
        self.write("build/dcctl.exe", NOW + 500)
        self.build(NOW)
        self.assertIsNone(bf.check(self.binary, [self.src]))

    def test_every_listed_directory_counts(self):
        other = os.path.join(self._tmp.name, "core")
        os.makedirs(other)
        p = os.path.join(other, "auth.go")
        with open(p, "w") as f:
            f.write("x")
        os.utime(p, (NOW + 5, NOW + 5))
        self.write("main.go", NOW - 100)
        self.build(NOW)
        self.assertIn("auth.go", bf.check(self.binary, [self.src, other]))


class CommandLineTests(Tree):
    def run_cli(self, *args):
        return subprocess.run([sys.executable, "-I", os.path.join(HERE, "binary_fresh.py"), *args], capture_output=True, text=True)

    def test_exit_status_is_the_verdict(self):
        self.write("main.go", NOW - 100)
        self.build(NOW)
        self.assertEqual(self.run_cli(self.binary, self.src).returncode, 0)
        self.write("main.go", NOW + 100)
        r = self.run_cli(self.binary, self.src)
        self.assertEqual(r.returncode, 1)
        self.assertIn("STALE", r.stderr)

    def test_no_arguments_is_a_usage_error(self):
        self.assertEqual(self.run_cli().returncode, 2)



class AcceptanceScriptTests(unittest.TestCase):
    """acceptance.sh must keep asking: a refactor that drops the check would let a stale simulator record a take again."""

    def setUp(self):
        with open(os.path.join(HERE, "acceptance.sh"), encoding="utf-8") as f:
            self.script = f.read()

    def test_it_builds_both_binaries_from_the_tree_by_default(self):
        self.assertIn('make -C "$REPO/backend/cli" build', self.script)
        self.assertIn('cd "$REPO/backend/sims/dc-simulator" && go build -o', self.script)

    def test_it_judges_both_binaries_against_their_sources_before_any_run(self):
        self.assertIn('binary_fresh.py" "$d"', self.script)
        self.assertIn('binary_fresh.py" "$s"', self.script)
        self.assertLess(self.script.index("prepare_binaries\n\tTP_KUBECONFIG"), self.script.index("run_mode()"))
        self.assertIn("\npreflight\n", self.script)

    def test_no_default_falls_back_to_a_binary_on_the_path(self):
        self.assertNotIn('DCCTL="${DCCTL:-dcctl}"', self.script)
        self.assertNotIn('SIMULATOR="${SIMULATOR:-dc-simulator}"', self.script)


if __name__ == "__main__":
    unittest.main()
