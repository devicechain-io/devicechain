#!/usr/bin/env python3
# Copyright The DeviceChain Authors
# SPDX-License-Identifier: Apache-2.0
"""
A take is recorded against a platform by dcctl and dc-simulator, and a recording made by a stale build of either is a recording of something the tree no
longer is (a board that reads "Not available for customer users" because the simulator predated the board built on the customer anchor). This refuses a
binary that is older than the newest source it was built from.

    binary_fresh.py BINARY SOURCE_DIR [SOURCE_DIR ...]      exit 0 fresh, 1 stale or missing, 2 usage

"Source" is every file under the directories except build output, VCS and dependency folders, and test files (editing a test does not change a binary).
"""

import os
import sys

SKIP_DIRS = {".git", "build", "bin", "node_modules", "Build", "__pycache__"}


def newest_source(dirs):
    """(mtime, path) of the newest source file under dirs, or (0, None) when there is none."""
    best = (0.0, None)
    for root_dir in dirs:
        for root, subdirs, files in os.walk(root_dir):
            subdirs[:] = [d for d in subdirs if d not in SKIP_DIRS]
            for name in files:
                if name.endswith("_test.go"):
                    continue
                path = os.path.join(root, name)
                try:
                    m = os.path.getmtime(path)
                except OSError:
                    continue
                if m > best[0]:
                    best = (m, path)
    return best


def check(binary, dirs):
    """None when the binary is at least as new as every source, else a sentence saying why not."""
    if not os.path.isfile(binary):
        return f"{binary} does not exist"
    for d in dirs:
        if not os.path.isdir(d):
            return f"source directory {d} does not exist, so {binary} cannot be shown to be current"
    src_time, src = newest_source(dirs)
    if src is None:
        return f"no source files under {', '.join(dirs)}, so {binary} cannot be shown to be current"
    if os.path.getmtime(binary) < src_time:
        return f"{binary} is older than {src}: it was built before the tree last changed"
    return None


def main(argv):
    if len(argv) < 3:
        print(__doc__, file=sys.stderr)
        return 2
    why = check(argv[1], argv[2:])
    if why:
        print("STALE: " + why, file=sys.stderr)
        return 1
    return 0


if __name__ == "__main__":
    sys.exit(main(sys.argv))
