#!/usr/bin/env python3
# Copyright The DeviceChain Authors
# SPDX-License-Identifier: Apache-2.0
"""Offline executor controls: actual batch/test/race/skip scripts, stub Go and network helpers."""
import json
import os
from pathlib import Path
import shutil
import subprocess
import sys
import tempfile

HACK = Path(__file__).resolve().parent


def main():
    bash = os.environ.get("BASH_EXE") or shutil.which("bash")
    with tempfile.TemporaryDirectory(prefix="go-ci-batch-") as tmp:
        root = Path(tmp)
        (root / "hack").mkdir()
        (root / "bin").mkdir()
        (root / "temp").mkdir()
        for name in ("go-ci-batch.sh", "go-ci-test.sh", "go-ci-race.sh", "check-no-silent-skips.sh"):
            shutil.copyfile(HACK / name, root / "hack" / name)
        (root / "hack/silent-skip-allowlist.txt").write_text("# no fixture skips allowed\n")
        first, second = "backend/tools/first", "backend/tools/second"
        for module in (first, second):
            (root / module).mkdir(parents=True)

        def script(rel, body):
            path = root / rel
            path.write_text("#!/usr/bin/env bash\nset -euo pipefail\n" + body, encoding="utf-8", newline="\n")
            path.chmod(0o755)

        script("bin/go", '''echo "${PWD##*/}: go $*" >> "$CHECK_LOG"
if [ "${FAIL_CHECK:-}" = "build" ] && [ "${PWD##*/}" = first ] && [ "$1" = build ]; then exit 7; fi
if [ "$1" = test ]; then
  if [ "${FAIL_CHECK:-}" = skip ] && [ "${PWD##*/}" = first ]; then
    echo '{"Action":"skip","Package":"fixture/first","Test":"TestUnexpectedSkip"}'
  fi
  echo '{"Action":"pass","Package":"fixture/pkg"}'
  if [ "${FAIL_CHECK:-}" = test ] && [ "${PWD##*/}" = first ]; then exit 9; fi
elif [ "$1" = list ]; then
  echo '{"ImportPath":"fixture/selected"}'
fi
''')
        script("bin/gofmt", '''echo "${PWD##*/}: gofmt $*" >> "$CHECK_LOG"
if [ "${FAIL_CHECK:-}" = fmt ] && [ "${PWD##*/}" = first ]; then echo broken.go; fi
''')
        for name in ("go-mod-download.sh", "govulncheck.sh", "check-go-work-sum.sh", "check-go-mod-tidy.sh"):
            script("hack/" + name, '''echo "''' + name + ''' $*" >> "$CHECK_LOG"
if [ "${FAIL_CHECK:-}" = tidy ] && [ "''' + name + '''" = check-go-mod-tidy.sh ] && [[ "$*" = *first* ]]; then exit 2; fi
''')
        script("hack/go-leg.sh", '''echo "go-leg.sh $1 pkgs=$LEG_PKGS shard=$LEG_SHARD exclude=$LEG_EXCLUDE" >> "$CHECK_LOG"
printf '%s\\n' "${LEG_PKGS:-./...}" > "$2"
''')
        script("hack/go-race.sh", '''echo "go-race.sh $1 scope=$DC_RACE_SCOPE $*" >> "$CHECK_LOG"
if [ "${FAIL_CHECK:-}" = verdict ] && [[ "$1" = *first ]]; then exit 0; fi
case "$DC_RACE_SCOPE" in
 all) echo "race: COVERED $1 -- fixture" ;;
 changed) echo "race: PARTIAL $1 -- fixture" ;;
 none) echo "race: SKIPPED $1 -- fixture" ;;
esac
''')
        for path in (root / "hack").glob("*.sh"):
            path.chmod(0o755)
        log, summary = root / "checks.log", root / "summary.md"
        base = dict(os.environ, GITHUB_WORKSPACE=root.as_posix(), RUNNER_TEMP=(root / "temp").as_posix(),
                    CHECK_LOG=log.as_posix(), GITHUB_STEP_SUMMARY=summary.as_posix(),
                    GITHUB_EVENT_NAME="push", PYTHON=Path(sys.executable).as_posix())
        base["PATH"] = os.pathsep.join((str(root / "bin"), str(Path(bash).parent), os.environ["PATH"]))
        legs = [dict(module=m, static=True, pkgs="", exclude="", shard="", race_scope="all", race_pkgs="")
                for m in (first, second)]

        def run(failure="", entries=None, event="push"):
            log.write_text("")
            summary.write_text("")
            env = dict(base, FAIL_CHECK=failure, BATCH_ENTRIES=json.dumps(legs if entries is None else entries),
                       GITHUB_EVENT_NAME=event)
            proc = subprocess.run([bash, (root / "hack/go-ci-batch.sh").as_posix()], env=env,
                                  text=True, stdout=subprocess.PIPE, stderr=subprocess.STDOUT)
            return proc, log.read_text(), summary.read_text()

        proc, calls, report = run()
        assert proc.returncode == 0, proc.stdout
        for module in (first, second):
            short = module.rsplit("/", 1)[-1]
            for command in ("gofmt -l .", "go build ./...", "go vet ./...", "go vet -tags integration interop ./...",
                            "go test -json -count=1 -timeout 20m ./..."):
                assert short + ": " + command in calls, calls
            for helper in ("go-mod-download.sh " + module, "govulncheck.sh " + module,
                           "check-go-work-sum.sh --module " + module, "check-go-mod-tidy.sh --module " + module,
                           "go-race.sh " + module + " scope=all"):
                assert helper in calls, calls
            assert "**%s**: PASS" % module in report, report
        print("ok: executor runs every small-module check with original test flags and module working directories")
        for failure, label in (("build", "go build"), ("fmt", "gofmt"), ("test", "go test / silent-skip gate"),
                               ("skip", "go test / silent-skip gate"), ("tidy", "tidy"), ("verdict", "race detector")):
            proc, calls, report = run(failure)
            assert proc.returncode != 0, proc.stdout
            assert "::error::%s: %s failed" % (first, label) in proc.stdout, proc.stdout
            assert "**%s**: FAIL" % first in report and "**%s**: PASS" % second in report, report
            assert "check-go-mod-tidy.sh --module " + first in calls, calls
            print("ok: planted %s failure is red, names its module in log/summary, runs tidy and later members" % failure)
        reader = dict(legs[0], static=False, pkgs="./reader/", race_scope="changed", race_pkgs="./reader/")
        proc, calls, report = run(entries=[reader], event="pull_request")
        assert proc.returncode == 0 and "go test -json -count=1 -timeout 20m ./reader/" in calls, proc.stdout
        assert "scope=changed" in calls and "go build" not in calls and "tidy.sh" not in calls, calls
        assert second not in calls, calls
        print("ok: reader-only batch preserves selected packages, static=false and narrowed PR race scope")
        proc, _, _ = run(entries=[reader])
        assert proc.returncode != 0, proc.stdout
        print("ok: narrowed race verdict on push is refused")
        proc, calls, report = run(entries=[])
        assert proc.returncode == 0 and not calls and not report and "skipped" in proc.stdout, proc.stdout
        print("ok: empty executor selection performs no checks")
    print("go-ci-batch executor self-test passed")


if __name__ == "__main__":
    main()
