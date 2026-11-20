# Copyright The DeviceChain Authors
# SPDX-License-Identifier: Apache-2.0

import json
from pathlib import Path
import tempfile
import unittest

from check_director_log import check


class CaptionLogTests(unittest.TestCase):
    def errors(self, events):
        with tempfile.TemporaryDirectory() as directory:
            path = Path(directory) / "director.ndjson"
            path.write_text("".join(json.dumps({"kind": "director", "wallS": at,
                "data": {"kind": kind, "page": page, "showS": at, "value": value, "evidence": []}}) + "\n"
                for at, kind, page, value in events), encoding="utf-8")
            return check(path)[0]

    def test_final_caption_eof_and_title(self):
        for kind in ("caption", "title", "visible-caption"):
            with self.subTest(kind=kind):
                errors = self.errors([(0, kind, "", "final text"), (1, "sim-hour", "", "6")])
                self.assertEqual(len(errors), 1)
                self.assertIn("visible 1 s", errors[0])

    def test_empty_log_rejected(self):
        self.assertTrue(self.errors([]))

    def test_recording_end_validates_final_title(self):
        for end, valid in ((1, False), (3, True)):
            self.assertEqual(not self.errors([(0, "title", "", "title"), (end, "recording-end", "", "")]), valid)

    def test_rendered_surface_includes_title_and_delayed_claim(self):
        events = [(0, "title", "", "title"), (0, "caption", "1", "claim"),
            (0, "visible-caption", "", "title"), (3, "visible-caption-end", "", "title"),
            (3, "visible-caption", "1", "claim"), (6, "recording-end", "", "")]
        self.assertFalse(self.errors(events))
        events[3] = (1, "visible-caption-end", "", "title")
        self.assertIn("visible 1 s", self.errors(events)[0])


if __name__ == "__main__":
    unittest.main()
