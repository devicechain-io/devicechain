# Copyright The DeviceChain Authors
# SPDX-License-Identifier: Apache-2.0
"""The wrong-CA control must be able to mint its certificate. Run: python3 -m unittest test_phase_a_script (from this directory).

phase-a-acceptance.sh, sourced, only defines its functions. Under Git Bash on Windows a bare `openssl req -subj "/CN=..."`
has its subject rewritten into a path and fails, which used to abort the whole controls run at the wrong-ca step with no message.
"""

import os
import shutil
import subprocess
import tempfile
import unittest

HERE = os.path.dirname(os.path.abspath(__file__))
SCRIPT = os.path.join(HERE, "phase-a-acceptance.sh")


@unittest.skipUnless(shutil.which("bash") and shutil.which("openssl"), "needs bash and openssl")
class WrongCaTest(unittest.TestCase):
    def test_make_wrong_ca_writes_a_certificate(self):
        with tempfile.TemporaryDirectory() as d:
            out = os.path.join(d, "wrong-ca.pem").replace("\\", "/")
            script = 'source "%s"; make_wrong_ca "%s"' % (SCRIPT.replace("\\", "/"), out)
            r = subprocess.run([shutil.which("bash"), "-c", script], capture_output=True, text=True)
            self.assertEqual(r.returncode, 0, r.stderr)
            with open(os.path.join(d, "wrong-ca.pem")) as f:
                self.assertIn("BEGIN CERTIFICATE", f.read())
            # the throwaway key is not kept next to it
            self.assertEqual(os.listdir(d), ["wrong-ca.pem"])


if __name__ == "__main__":
    unittest.main()
