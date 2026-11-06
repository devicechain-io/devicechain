# Copyright The DeviceChain Authors
# SPDX-License-Identifier: Apache-2.0
"""Browser-path negative controls for video_capture.py, against a throwaway local web server (no DeviceChain instance, no real credential).

Each case runs the real recorder in a subprocess and judges it by what it leaves behind, not by what it says:

    planted password (a transient toast, an attribute)   exit 3, NO mp4, NO failure screenshot, the password in no output file
    a required mask that matches nothing                  exit 1, NO mp4, NO failure screenshot
    a 404 start page, a 500 fetch, a GraphQL `errors`     exit 1, NO mp4
    a well-formed shot (with an element mask and a text mask)   exit 0, an mp4, a manifest

It needs Chromium and ffmpeg, so it is skipped unless asked for (the CI job runs only the pure tests):

    hack/video-capture/setup.sh                                   # once
    DC_BROWSER_TESTS=1 ~/.cache/video-capture/venv/bin/python -m unittest -v hack/video-capture/test_browser_paths.py
    (Windows: ...\\venv\\Scripts\\python)
"""

import http.server
import json
import os
import shutil
import subprocess
import sys
import tempfile
import threading
import unittest
import urllib.parse

HERE = os.path.dirname(os.path.abspath(__file__))
PASSWORD = "correct-horse-91"
EMAIL = "tester@example.test"
SHELL = '<div data-shot="user-menu">tester@example.test</div>'

PAGES = {
    "/ok": SHELL + "<h1>ok</h1><p>contact alice@example.test today</p>",
    "/nomask": "<h1>no user menu here</h1>",
    # the password appears for 400 ms in a toast, then is removed: only a per-change report can see it
    "/leak": SHELL + "<h1>leak</h1><script>setTimeout(()=>{const d=document.createElement('div');d.id='toast';"
             "d.textContent='pw '+(document.cookie.match(/pw=([^;]+)/)||[])[1];document.body.appendChild(d);"
             "setTimeout(()=>d.remove(),400)},300)</script>",
    "/leak-attr": SHELL + "<h1>leak</h1><script>setTimeout(()=>{document.body.setAttribute('data-x',"
                  "(document.cookie.match(/pw=([^;]+)/)||[])[1]);setTimeout(()=>document.body.removeAttribute('data-x'),300)},300)</script>",
    "/boom": SHELL + "<h1>boom</h1><script>setTimeout(()=>fetch('/api/boom'),200)</script>",
    "/gql": SHELL + "<h1>gql</h1><script>setTimeout(()=>fetch('/graphql',{method:'POST',body:'{}'}),200)</script>",
}


class Handler(http.server.BaseHTTPRequestHandler):
    def log_message(self, *a):
        pass

    def _send(self, code, body, ctype="text/html; charset=utf-8", headers=()):
        data = body.encode()
        self.send_response(code)
        self.send_header("Content-Type", ctype)
        self.send_header("Content-Length", str(len(data)))
        for k, v in headers:
            self.send_header(k, v)
        self.end_headers()
        self.wfile.write(data)

    def do_GET(self):
        path = urllib.parse.urlsplit(self.path).path
        if path == "/login":
            return self._send(200, '<form method="post" action="/login"><input id="email" name="email"><input id="password" type="password" name="password">'
                                   '<button type="submit">Sign in</button></form>')
        if "session=1" not in (self.headers.get("Cookie") or ""):
            return self._send(303, "", headers=[("Location", "/login")])
        if path == "/api/boom":
            return self._send(500, "{}", "application/json")
        if path in PAGES:
            return self._send(200, PAGES[path])
        return self._send(404, "<h1>not found</h1>")

    def do_POST(self):
        n = int(self.headers.get("Content-Length") or 0)
        form = urllib.parse.parse_qs(self.rfile.read(n).decode())
        if self.path == "/graphql":
            return self._send(200, json.dumps({"errors": [{"message": "nope"}]}), "application/json")
        if form.get("password", [""])[0] != PASSWORD:
            return self._send(401, "bad")
        # the page-readable cookie is what lets a test page echo the password, as a buggy console might
        return self._send(303, "", headers=[("Location", "/ok"), ("Set-Cookie", "session=1; Path=/"), ("Set-Cookie", f"pw={PASSWORD}; Path=/")])


def shotlist(shot):
    return ("version: 1\nvideo: neg\nlogin: {mode: env, email_env: DC_TEST_EMAIL, password_env: DC_TEST_PASSWORD}\nshots:\n" + shot)


@unittest.skipUnless(os.environ.get("DC_BROWSER_TESTS") == "1", "set DC_BROWSER_TESTS=1 (needs Chromium and ffmpeg: hack/video-capture/setup.sh)")
class BrowserPaths(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        cls.server = http.server.ThreadingHTTPServer(("127.0.0.1", 0), Handler)
        cls.base = f"http://127.0.0.1:{cls.server.server_address[1]}"
        threading.Thread(target=cls.server.serve_forever, daemon=True).start()

    @classmethod
    def tearDownClass(cls):
        cls.server.shutdown()

    def record(self, shot_yaml, **kw):
        tmp = tempfile.mkdtemp(prefix="vc-neg-")
        self.addCleanup(shutil.rmtree, tmp, True)
        sl = os.path.join(tmp, "shots.yaml")
        with open(sl, "w", encoding="utf-8") as f:
            f.write(shotlist(shot_yaml))
        out = os.path.join(tmp, "out")
        env = dict(os.environ, DC_TEST_EMAIL=EMAIL, DC_TEST_PASSWORD=kw.get("password", PASSWORD))
        r = subprocess.run([sys.executable, os.path.join(HERE, "video_capture.py"), "record", sl, "--out", out, "--base-url", self.base],
                           capture_output=True, text=True, env=env, timeout=240)
        return r, out

    def assertNothingKept(self, out):
        self.assertEqual([f for f in os.listdir(out) if f.endswith(".mp4")], [], "a clip was kept")
        self.assertFalse(os.path.exists(os.path.join(out, "failures")) and os.listdir(os.path.join(out, "failures")), "a failure screenshot was taken")

    def assertSecretNowhere(self, r, out):
        blob = r.stdout + r.stderr
        for root, _, files in os.walk(out):
            for fn in files:
                if fn.endswith((".json", ".txt")):
                    blob += open(os.path.join(root, fn), encoding="utf-8").read()
        self.assertNotIn(PASSWORD, blob)

    def shot(self, route, steps="      - wait: 1.2\n", mask="    mask: [{shot: user-menu}]\n", extra=""):
        return f"  - id: s\n    start: {route}\n    retries: 0\n{mask}{extra}    steps:\n{steps}"

    def test_ok_shot_records_with_both_mask_kinds(self):
        r, out = self.record(self.shot("/ok", mask="    mask: [{shot: user-menu}, {text_regex: '[a-z]+@example\\.test'}]\n"))
        self.assertEqual(r.returncode, 0, r.stderr)
        self.assertTrue(os.path.getsize(os.path.join(out, "s.mp4")) > 1000)
        man = json.load(open(os.path.join(out, "manifest.json"), encoding="utf-8"))
        self.assertEqual((len(man["clips"]), man["failed"]), (1, []))
        self.assertSecretNowhere(r, out)
        self.assertFalse([d for d in os.listdir(out) if d.startswith(".frames-")], "frames dir left behind")

    def test_a_transient_toast_with_the_password_is_exit_3(self):
        r, out = self.record(self.shot("/leak"))
        self.assertEqual(r.returncode, 3, r.stderr)
        self.assertNothingKept(out)
        self.assertSecretNowhere(r, out)

    def test_the_password_in_an_attribute_is_exit_3(self):
        r, out = self.record(self.shot("/leak-attr"))
        self.assertEqual(r.returncode, 3, r.stderr)
        self.assertNothingKept(out)
        self.assertSecretNowhere(r, out)

    def test_a_required_mask_that_matches_nothing_is_exit_1(self):
        r, out = self.record(self.shot("/nomask"))
        self.assertEqual(r.returncode, 1, r.stderr)
        self.assertNothingKept(out)

    def test_a_404_start_page_is_exit_1(self):
        r, out = self.record(self.shot("/missing", mask=""))
        self.assertEqual(r.returncode, 1, r.stderr)
        self.assertNothingKept(out)
        self.assertIn("404", r.stderr)

    def test_a_500_fetch_is_exit_1(self):
        r, out = self.record(self.shot("/boom"))
        self.assertEqual(r.returncode, 1, r.stderr)
        self.assertEqual([f for f in os.listdir(out) if f.endswith(".mp4")], [])
        self.assertIn("500", r.stderr)

    def test_a_graphql_errors_body_is_exit_1(self):
        r, out = self.record(self.shot("/gql"))
        self.assertEqual(r.returncode, 1, r.stderr)
        self.assertEqual([f for f in os.listdir(out) if f.endswith(".mp4")], [])
        self.assertIn("GraphQL", r.stderr)

    def test_a_short_password_is_refused_before_a_browser_starts(self):
        r, out = self.record(self.shot("/ok"), password="abc")
        self.assertEqual(r.returncode, 2, r.stderr)
        self.assertIn("shorter than 4", r.stderr)


if __name__ == "__main__":
    unittest.main()
