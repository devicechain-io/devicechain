# Copyright The DeviceChain Authors
# SPDX-License-Identifier: Apache-2.0
"""Tests for the pure parts of video_capture.py (no browser, no network, no credential)."""

import base64
import copy
import os
import subprocess
import sys
import tempfile
import unittest
from types import SimpleNamespace

sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))
import video_capture as v  # noqa: E402

GOOD = {
    "version": 1, "video": "t", "login": {"mode": "sim", "sim": "x"},
    "shots": [{"id": "a-1", "start": "/devices", "steps": [{"click": {"role": "tab", "name": "Metrics"}}, {"wait": 1}],
               "mask": [{"shot": "user-menu"}]}],
}


def bad(mutate):
    d = copy.deepcopy(GOOD)
    mutate(d)
    return v.validate(d)


class Validate(unittest.TestCase):
    def test_good(self):
        self.assertEqual(v.validate(GOOD), [])

    def test_unknown_keys_are_errors(self):
        self.assertTrue(bad(lambda d: d.update(bogus=1)))
        self.assertTrue(bad(lambda d: d["shots"][0].update(bogus=1)))
        self.assertTrue(bad(lambda d: d["shots"][0]["steps"].append({"click": {"text": "x"}, "bogus": 1})))
        self.assertTrue(bad(lambda d: d["shots"][0]["mask"].append({"shot": "a", "bogus": 1})))

    def test_a_shot_list_never_carries_a_secret(self):
        e = bad(lambda d: d["login"].update(password="hunter2"))
        self.assertTrue(any("never carries a secret" in x for x in e))
        self.assertFalse(any("hunter2" in x for x in e))

    def test_steps_need_one_verb(self):
        self.assertTrue(bad(lambda d: d["shots"][0].update(steps=[{"click": {"text": "a"}, "wait": 1}])))
        self.assertTrue(bad(lambda d: d["shots"][0].update(steps=[{}])))
        self.assertTrue(bad(lambda d: d["shots"][0].update(steps=[])))

    def test_targets_name_exactly_one_locator(self):
        self.assertTrue(bad(lambda d: d["shots"][0].update(steps=[{"click": {"text": "a", "testid": "b"}}])))
        self.assertTrue(bad(lambda d: d["shots"][0].update(steps=[{"click": {"name": "a"}}])))
        self.assertEqual(bad(lambda d: d["shots"][0].update(steps=[{"click": {"shot": "page", "nth": 1}}])), [])

    def test_type_and_scroll_shapes(self):
        self.assertEqual(bad(lambda d: d["shots"][0].update(steps=[{"type": {"into": {"selector": "#k"}, "text": "abc"}, "secret": True}])), [])
        self.assertTrue(bad(lambda d: d["shots"][0].update(steps=[{"type": {"text": "abc"}}])))
        self.assertTrue(bad(lambda d: d["shots"][0].update(steps=[{"scroll": {"by": 1, "to": {"text": "a"}}}])))
        self.assertEqual(bad(lambda d: d["shots"][0].update(steps=[{"scroll": {"by": 300}}])), [])

    def test_masks(self):
        self.assertTrue(bad(lambda d: d["shots"][0].update(mask=[{"text_regex": "("}])))
        self.assertTrue(bad(lambda d: d["shots"][0].update(mask=[{"shot": "a", "selector": "b"}])))
        self.assertTrue(bad(lambda d: d["shots"][0].update(mask=[{"shot": "a", "style": "smudge"}])))

    def test_shot_ids_unique_kebab(self):
        self.assertTrue(bad(lambda d: d["shots"].append(copy.deepcopy(d["shots"][0]))))
        self.assertTrue(bad(lambda d: d["shots"][0].update(id="Bad_ID")))

    def test_viewport_and_fps_fixed(self):
        self.assertTrue(bad(lambda d: d.update(viewport={"width": 1280, "height": 720})))
        self.assertTrue(bad(lambda d: d.update(fps=24)))
        self.assertEqual(bad(lambda d: d.update(fps=60)), [])

    def test_login_modes(self):
        self.assertTrue(bad(lambda d: d.update(login={"mode": "sim"})))
        self.assertTrue(bad(lambda d: d.update(login={"mode": "superuser-secret", "instance": "g"})))
        self.assertEqual(bad(lambda d: d.update(login={"mode": "superuser-secret", "instance": "g", "email": "a@b"})), [])
        self.assertTrue(bad(lambda d: d.update(login={"mode": "carrier-pigeon"})))

    def test_the_fixtures_are_valid(self):
        import yaml

        here = os.path.dirname(os.path.abspath(__file__))
        names = os.listdir(os.path.join(here, "shots"))
        self.assertTrue(names)
        for name in names:
            with open(os.path.join(here, "shots", name), encoding="utf-8") as f:
                self.assertEqual(v.validate(yaml.safe_load(f)), [], name)


class Pure(unittest.TestCase):
    def test_scrub(self):
        self.assertEqual(v.scrub("bad hunter2 and a@b.c", ("hunter2", "a@b.c", "")), "bad *** and ***")

    def test_password_leak_ignores_short_values(self):
        self.assertTrue(v.password_leaked("the password is hunter22 ok", ("hunter22",)))
        self.assertFalse(v.password_leaked("the password is abc ok", ("abc",)))
        self.assertTrue(v.password_leaked("the password is abcd ok", ("abcd",)))
        self.assertFalse(v.password_leaked("nothing here", ("hunter22",)))

    def test_typing_is_seeded_and_human(self):
        a, b = v.typing_delays("temperature_c", "s", 85), v.typing_delays("temperature_c", "s", 85)
        self.assertEqual(a, b)
        self.assertNotEqual(a, v.typing_delays("temperature_c", "other", 85))
        self.assertGreater(len(set(a)), 3)
        self.assertEqual(len(a), len("temperature_c"))

    def test_glide_ends_exactly_on_target(self):
        pts = v.glide_points(0, 0, 100, 50)
        self.assertEqual(pts[-1], (100, 50))
        xs = [p[0] for p in pts]
        self.assertEqual(xs, sorted(xs))

    def test_mask_css_and_selectors(self):
        specs = [{"shot": "user-menu"}, {"testid": "tok", "style": "box"}, {"text_regex": "x"}, {"selector": ".a"}]
        css = v.mask_css(specs)
        self.assertIn('[data-shot="user-menu"]{filter:blur(14px)', css)
        self.assertIn('[data-testid="tok"]{filter:brightness(0)', css)
        self.assertEqual(v.mask_selectors(specs), [(0, '[data-shot="user-menu"]'), (1, '[data-testid="tok"]'), (3, ".a")])

    def test_target_spec(self):
        self.assertEqual(v.target_spec({"role": "tab", "name": "M"}), ("role", ("tab", "M"), 0, False))
        self.assertEqual(v.target_spec({"text": "a"})[3], True)
        self.assertEqual(v.target_spec({"text": "a", "exact": False})[3], False)
        self.assertTrue(v.regex_arg("re:^a").match("A"))
        self.assertEqual(v.regex_arg("plain"), "plain")

    def test_timeline_holds_each_frame_until_the_next(self):
        tl = v.clip_timeline([(1.0, "b"), (0.0, "a")], 0.0, 3.0)
        self.assertEqual([(p, round(d, 3)) for p, d in tl], [("a", 1.0), ("b", 2.0)])
        self.assertTrue(v.concat_script(tl).strip().endswith("file 'b'"))

    def test_timeline_covers_exactly_the_window(self):
        frames = [(100.0, "a"), (103.0, "b"), (106.0, "c"), (109.0, "d")]
        tl = v.clip_timeline(frames, 102.0, 107.0)
        self.assertEqual([(p, round(d, 3)) for p, d in tl], [("a", 1.0), ("b", 3.0), ("c", 1.0)])
        self.assertAlmostEqual(sum(d for _, d in tl), 5.0, places=3)
        # a window that starts before the first frame holds the first frame
        self.assertEqual([p for p, _ in v.clip_timeline([(5.0, "x")], 0.0, 4.0)], ["x"])

    def test_scrub_obj_reaches_every_string(self):
        o = v.scrub_obj({"failed": [{"error": "mask a@b.c matched nothing"}], "n": 3, "marks": ["type: hunter22"]}, ("a@b.c", "hunter22"))
        self.assertEqual(o, {"failed": [{"error": "mask *** matched nothing"}], "n": 3, "marks": ["type: ***"]})

    def test_unsafe_failures_take_no_screenshot(self):
        self.assertTrue(v.SecretLeak("x").unsafe)
        self.assertFalse(v.CaptureError("x").unsafe)
        self.assertEqual(v.SecretLeak("x").exit_code, v.EXIT_SECRET_LEAK)

    def test_trim_window(self):
        self.assertEqual(v.trim_window(10, 20, {"head_s": 1, "tail_s": 2}), (11, 18))
        self.assertEqual(v.trim_window(10, 12, {}, min_s=5), (10, 15))
        with self.assertRaises(v.CaptureError):
            v.trim_window(10, 12, {"head_s": 1.5, "tail_s": 1})

    def test_manifest_marks_are_relative_to_the_clip(self):
        e = v.shot_entry({"id": "s", "line": "L1"}, 100.0, 110.0, [(101.5, "a"), (99.0, "before")], {}, 30, 1, (100.0, 110.0))
        self.assertEqual(e["marks"], [{"at_s": 1.5, "what": "a"}])
        self.assertEqual((e["duration_s"], e["line"], e["size"]), (10.0, "L1", "1920x1080"))

    def test_anchor_tokens_cannot_break_out_of_a_selector(self):
        for bad_v in ('a"]', "a]", "a b", 'a"', "a\b", ""):
            self.assertTrue(bad(lambda d, x=bad_v: d["shots"][0].update(mask=[{"shot": x}])), repr(bad_v))
            self.assertTrue(bad(lambda d, x=bad_v: d["shots"][0].update(steps=[{"click": {"testid": x}}])), repr(bad_v))
        self.assertEqual(bad(lambda d: d["shots"][0].update(mask=[{"testid": "user.menu-1:x_y"}])), [])

    def test_posix_drive_path_is_refused_on_windows_only(self):
        for p in ("/c", "/c/", "/cygdrive/c/x"):
            self.assertEqual(v.posix_drive_path(p), os.name == "nt", p)
        self.assertEqual(v.posix_drive_path("/c/Users/x"), os.name == "nt")
        self.assertFalse(v.posix_drive_path("C:/Users/x"))


class Credentials(unittest.TestCase):
    LOGIN = {"mode": "superuser-secret", "instance": "g", "email": "su@x"}
    SECRET = "s3cret-value"

    def test_sim_record_nested(self):
        self.assertEqual(v.find_credentials({"a": [{"simEmail": "e@x", "simPassword": "pw"}]}), ("e@x", "pw"))
        with self.assertRaises(v.CaptureError):
            v.find_credentials({"simEmail": "e@x"})

    def test_sim_record_missing_names_the_sim_not_the_content(self):
        with tempfile.TemporaryDirectory() as home, self.assertRaises(v.CaptureError) as cm:
            v.load_credentials({"mode": "sim", "sim": "nope"}, home=home)
        self.assertIn("nope", str(cm.exception))

    def test_env(self):
        login = {"mode": "env", "email_env": "E", "password_env": "P"}
        self.assertEqual(v.load_credentials(login, env={"E": "a", "P": "b"}), ("a", "b"))
        with self.assertRaises(v.CaptureError):
            v.load_credentials(login, env={"E": "a"})

    def test_superuser_secret_decodes(self):
        def ok(cmd, **kw):
            return SimpleNamespace(returncode=0, stdout=base64.b64encode(self.SECRET.encode()).decode(), stderr="")

        self.assertEqual(v.load_credentials(self.LOGIN, run=ok), ("su@x", self.SECRET))

    def test_superuser_secret_errors_never_quote_the_value(self):
        b64 = base64.b64encode(self.SECRET.encode()).decode()

        def fail(cmd, **kw):
            return SimpleNamespace(returncode=1, stdout=b64, stderr="Error from server (NotFound)\nmore")

        with self.assertRaises(v.CaptureError) as cm:
            v.load_credentials(self.LOGIN, run=fail)
        self.assertNotIn(self.SECRET, str(cm.exception))
        self.assertNotIn(b64, str(cm.exception))
        self.assertIn("exit 1", str(cm.exception))

        def garbage(cmd, **kw):
            return SimpleNamespace(returncode=0, stdout="!!notbase64!!" + self.SECRET, stderr="")

        with self.assertRaises(v.CaptureError) as cm:
            v.load_credentials(self.LOGIN, run=garbage)
        self.assertNotIn(self.SECRET, str(cm.exception))

        def boom(cmd, **kw):
            raise subprocess.TimeoutExpired(cmd, 30)

        with self.assertRaises(v.CaptureError):
            v.load_credentials(self.LOGIN, run=boom)

    def test_kubectl_args_honour_overrides(self):
        ns, name, cmd = v.kubectl_secret_args({"instance": "g", "kube_context": "kind-x"})
        self.assertEqual((ns, name), ("dci-g", "dci-g-superuser"))
        self.assertEqual(cmd[:3], ["kubectl", "--context", "kind-x"])


if __name__ == "__main__":
    unittest.main()
