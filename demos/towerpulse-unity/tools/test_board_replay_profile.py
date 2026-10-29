# Copyright The DeviceChain Authors
# SPDX-License-Identifier: Apache-2.0
"""
The board-replay profile: Sitepulse's converter, told by a small JSON file what a different demo's recording looks like. The Sitepulse default is held by
its own suite (demos/sitepulse-unity/tools/test_recording_to_board.py); these tests hold the Towerpulse profile, and that the profile cannot loosen the
converter's refusals.
"""

import json
import os
import sys
import tempfile
import unittest

HERE = os.path.dirname(os.path.abspath(__file__))
sys.path.insert(0, HERE)
sys.path.insert(0, os.path.join(HERE, "..", "..", "sitepulse-unity", "tools"))
import recording_to_board as c  # noqa: E402
import towerpulse_fixture as fx  # noqa: E402

REPO = os.path.abspath(os.path.join(HERE, "..", "..", ".."))
COMMITTED = os.path.join(REPO, "frontend", "testdata", "board-replay", "towerpulse-fixture.json")


class Base(unittest.TestCase):
    def setUp(self):
        self._tmp = tempfile.TemporaryDirectory()
        self.addCleanup(self._tmp.cleanup)
        self.root = os.path.join(self._tmp.name, "run")
        self.profile = c.load_profile(fx.PROFILE)

    def convert(self, **patches):
        fx.make_run(self.root, **patches)
        return c.convert(self.root, fx.PLACEHOLDER_BOARD, None, self.profile)


class TowerpulseProfile(Base):
    def test_a_towerpulse_run_converts_and_carries_the_story(self):
        doc, counts = self.convert()
        self.assertEqual(31, len(doc["devices"]))
        self.assertEqual([], doc["chapters"], "no video side, so no chapters")
        self.assertIn("cop", doc["channels"]["measurements"])
        self.assertGreater(counts["measurementOnBoard"], 500)
        events = doc["alarms"]["events"]
        self.assertEqual(["zone-hot", "zone-hot", "chiller-silent"], [e["key"] for e in events])
        self.assertEqual(["ACTIVE", "CLEARED", "ACTIVE"], [e["state"] for e in events])
        self.assertEqual(["alarm-1", "alarm-1", "alarm-2"], [e["id"] for e in events], "platform alarm ids are re-minted")
        self.assertEqual(sorted(["run.json", "observed.ndjson", "device.ndjson"]), sorted(doc["sourceHashes"]))

    def test_the_anchors_the_board_is_bound_to_come_through_as_format_two(self):
        doc, _ = self.convert()
        self.assertEqual(2, doc["formatVersion"])
        by = {a["targetToken"]: a for a in doc["anchors"]}
        token = {d["id"]: d["token"] for d in doc["devices"]}
        self.assertEqual(["tp-vav-tower-f4-east", "tp-vav-tower-f4-north", "tp-vav-tower-f4-south", "tp-vav-tower-f4-west"], sorted(by["tp-tower-f4"]["devices"]))
        self.assertEqual(24, len(by["tp-tower"]["devices"]))
        self.assertEqual(7, len(by["tp-annex"]["devices"]))
        self.assertTrue(set(by["tp-annex"]["devices"]).isdisjoint(by["tp-tower"]["devices"]), "the buildings share no device")
        self.assertIn(token["TP-CH-A"], by["tp-annex-plant"]["devices"])
        self.assertEqual({"customer", "area"}, {a["targetType"] for a in doc["anchors"]})

    def test_the_profiles_anchors_are_the_scenarios_hierarchy(self):
        """Re-derived here from the device table, so a device added to one building and not to its anchors fails this."""
        import acceptance_check as ac

        anchors = {a["targetToken"]: set(a["devices"]) for a in self.profile.anchors}
        every = set(ac.DEVICES)
        self.assertEqual(every, anchors["tp-tower"] | anchors["tp-annex"])
        self.assertEqual(set(ac.ANNEX_DEVICES), anchors["tp-annex"])
        self.assertEqual(anchors["tp-tower"] | anchors["tp-annex"], anchors["tp-meridian"] | anchors["tp-halden"])
        for building in ("tower", "annex"):
            floors = [v for k, v in anchors.items() if k.startswith("tp-%s-f" % building)]
            plant = anchors["tp-%s-plant" % building]
            self.assertEqual(anchors["tp-" + building], set().union(*floors) | plant, building)
            self.assertTrue(all(ac.kind_of(d) == "vav" for f in floors for d in f), "a floor holds zones only")
            self.assertTrue(all(ac.kind_of(d) != "vav" for d in plant), "the plant holds no zone")

    def test_command_rows_are_dropped_not_copied(self):
        doc, counts = self.convert()
        self.assertEqual(1, counts["commandRowsDropped"])
        self.assertNotIn("boost-cooling", json.dumps(doc))

    def test_the_committed_fixture_is_what_the_converter_makes(self):
        doc, _ = self.convert()
        want = json.dumps(doc, separators=(",", ":"), ensure_ascii=True) + "\n"
        with open(COMMITTED, encoding="utf-8", newline="") as f:
            self.assertEqual(want, f.read(), "regenerate with: python demos/towerpulse-unity/tools/towerpulse_fixture.py --board-recording frontend/testdata/board-replay/towerpulse-fixture.json")


class ProfileDoesNotLoosenTheRefusals(Base):
    def test_a_calculated_metric_needs_a_reading_the_device_published_at_that_instant(self):
        def drop_device_reading(device):
            device[:] = [d for d in device if not (d["dev"] == "TP-CH-T" and d["occ"] == fx.stamp(3))]

        with self.assertRaises(c.Refusal) as e:
            self.convert(patch_device=drop_device_reading)
        self.assertIn("fabricated", str(e.exception))

    def test_a_measurement_the_device_never_sent_is_refused_even_under_a_profile(self):
        def forge(observed):
            observed.append({"t": 50.0, "k": "measurement", "dev": "tp-vav-tower-f4-east", "n": "temp_c", "v": 99.0, "occ": fx.stamp(5), "obs": fx.stamp(5)})

        with self.assertRaises(c.Refusal):
            self.convert(patch_observed=forge)

    def test_an_absence_alarm_that_coincides_with_a_published_reading_is_refused(self):
        def contradict(observed):
            for r in observed:
                if r.get("key") == "chiller-silent":
                    r["occ"] = fx.stamp(3)  # the chiller published at step 3

        with self.assertRaises(c.Refusal) as e:
            self.convert(patch_observed=contradict)
        self.assertIn("absence alarm", str(e.exception))

    def test_only_the_named_keys_are_exempt_from_needing_a_triggering_reading(self):
        def rename(observed):
            for r in observed:
                if r.get("key") == "chiller-silent":
                    r["key"] = "chiller-offline"

        with self.assertRaises(c.Refusal):
            self.convert(patch_observed=rename)

    def test_an_unclean_run_is_refused(self):
        def dirty(run):
            run["build"]["trackedTreeClean"] = False

        with self.assertRaises(c.Refusal):
            self.convert(patch_run=dirty)

    def test_a_run_that_did_not_end_cleanly_is_refused(self):
        def crashed(run):
            run["end"]["cleanly"] = False

        with self.assertRaises(c.Refusal):
            self.convert(patch_run=crashed)

    def test_a_device_kind_outside_the_profiles_grammar_is_refused(self):
        def bad(run):
            run["devices"][0]["kind"] = "Vav Box!"

        with self.assertRaises(c.Refusal):
            self.convert(patch_run=bad)

    def test_the_sitepulse_default_still_refuses_towerpulses_lowercase_kinds(self):
        fx.make_run(self.root)
        with self.assertRaises((c.Refusal, FileNotFoundError, OSError, KeyError)):
            c.convert(self.root, fx.PLACEHOLDER_BOARD)


class AnchorRefusals(Base):
    def test_an_anchor_naming_a_device_the_run_lacks_is_refused(self):
        def drop(run):
            run["devices"] = [d for d in run["devices"] if d["id"] != "TP-VAV-T-F4-W"]

        with self.assertRaises(c.Refusal):
            self.convert(patch_run=drop)


class ProfileFile(unittest.TestCase):
    def write(self, doc):
        d = tempfile.mkdtemp()
        self.addCleanup(lambda: __import__("shutil").rmtree(d, ignore_errors=True))
        p = os.path.join(d, "p.json")
        with open(p, "w", encoding="utf-8") as f:
            json.dump(doc, f)
        return p

    def good(self):
        with open(fx.PROFILE, encoding="utf-8") as f:
            return json.load(f)

    def test_the_shipped_profile_loads(self):
        p = c.load_profile(fx.PROFILE)
        self.assertEqual("towerpulse", p.demo)
        self.assertFalse(p.chapters)
        self.assertEqual(("chiller-silent",), p.unsampled_alarm_keys)

    def test_an_unknown_key_is_refused_so_a_typo_cannot_select_a_default(self):
        d = self.good()
        d["unsampledAlarmKey"] = ["chiller-silent"]
        with self.assertRaises(c.Refusal):
            c.load_profile(self.write(d))

    def test_a_profile_cannot_name_a_file_the_converter_never_reads(self):
        d = self.good()
        d["inputs"] = d["inputs"] + ["operator-token.txt"]
        with self.assertRaises(c.Refusal):
            c.load_profile(self.write(d))

    def test_the_three_required_inputs_cannot_be_dropped(self):
        d = self.good()
        d["inputs"] = ["run.json", "observed.ndjson"]
        with self.assertRaises(c.Refusal):
            c.load_profile(self.write(d))

    def test_chapters_need_their_files(self):
        d = self.good()
        d["chapters"] = True
        with self.assertRaises(c.Refusal):
            c.load_profile(self.write(d))

    def test_a_malformed_anchor_is_refused(self):
        for mutate in (
            lambda d: d["anchors"].append(dict(d["anchors"][0])),  # twice
            lambda d: d["anchors"][0].update(targetType="planet"),
            lambda d: d["anchors"][0].update(devices=[]),
            lambda d: d["anchors"][0].update(devices=["TP-CH-A", "TP-CH-A"]),
            lambda d: d["anchors"][0].update(extra=1),
            lambda d: d["anchors"][0].update(targetToken="Not A Token"),
        ):
            d = self.good()
            mutate(d)
            with self.assertRaises(c.Refusal):
                c.load_profile(self.write(d))

    def test_a_metric_name_must_fit_the_grammar(self):
        d = self.good()
        d["measurements"] = d["measurements"] + ["Bad Name"]
        with self.assertRaises(c.Refusal):
            c.load_profile(self.write(d))


if __name__ == "__main__":
    unittest.main()
