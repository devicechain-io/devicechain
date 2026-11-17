#!/usr/bin/env python3
# Copyright The DeviceChain Authors
# SPDX-License-Identifier: Apache-2.0
"""Does a pull request's diff touch a dependency manifest? Decides whether dependency-review runs.

    git diff --name-only ... | hack/dependency-review-scope.py   # prints "true" or "false"
    hack/dependency-review-scope.py --self-test

dependency-review compares the dependency graph GitHub derives from MANIFESTS at the base and the
head of a pull request. A diff that touches none of them has an identical graph on both sides, so
the review has nothing to find; .github/workflows/dependency-review.yml then reports its required
check as a success without running the action.

FAIL SAFE: the answer is "false" only for a non-empty list in which no path matches. An empty
list (the file list could not be read, or came back empty) is "true", and so is any error: the
workflow runs the review whenever this script does not print exactly "false".

The patterns err wide on purpose. Running the review on a diff that did not need it costs a few
seconds; missing a manifest lets a new dependency in unreviewed. So every ecosystem the dependency
graph reads is listed, whether or not this repository uses it today, plus the Actions workflows
(an action is a dependency) and container files.
"""
import fnmatch
import sys

# Matched against the FILE NAME (any directory) unless the pattern contains a "/", in which case
# it is matched against the whole repo-relative path.
MANIFESTS = [
    # Go
    "go.mod", "go.sum", "go.work", "go.work.sum",
    # npm / yarn / pnpm
    "package.json", "package-lock.json", "npm-shrinkwrap.json", "yarn.lock", "pnpm-lock.yaml",
    # NuGet / .NET (sdks/csharp)
    "*.csproj", "*.fsproj", "*.vbproj", "packages.config", "packages.lock.json",
    "Directory.Packages.props", "Directory.Build.props", "Directory.Build.targets",
    "nuget.config", "NuGet.Config", "global.json",
    # Unity package manager (sdks/unity)
    "packages-lock.json", "*/Packages/manifest.json",
    # Maven / Gradle (the LwM2M interop client)
    "pom.xml", "*.gradle", "*.gradle.kts", "gradle.lockfile", "gradle/libs.versions.toml",
    # Python, Ruby, Rust, PHP
    "requirements*.txt", "pyproject.toml", "poetry.lock", "uv.lock", "Pipfile", "Pipfile.lock",
    "setup.py", "setup.cfg", "Gemfile", "Gemfile.lock", "*.gemspec", "Cargo.toml", "Cargo.lock",
    "composer.json", "composer.lock",
    # Containers
    "Dockerfile", "Dockerfile.*", "*.Dockerfile", "*.dockerfile", "Containerfile",
    # GitHub Actions: every `uses:` is a dependency
    ".github/workflows/*", ".github/actions/*", "action.yml", "action.yaml",
]


def is_manifest(path):
    name = path.rsplit("/", 1)[-1]
    for p in MANIFESTS:
        if "/" in p:
            # A "/"-pattern matches the path itself or any path ending in it.
            if fnmatch.fnmatchcase(path, p) or fnmatch.fnmatchcase(path, "*/" + p) or (
                    p.endswith("/*") and path.startswith(p[:-1])):
                return True
        elif fnmatch.fnmatchcase(name, p):
            return True
    return False


def decide(paths):
    """(verdict, why). True unless a non-empty list has no manifest in it."""
    paths = [p.strip() for p in paths if p.strip()]
    if not paths:
        return True, "the changed-file list is empty, so nothing can be ruled out"
    hits = [p for p in paths if is_manifest(p)]
    if hits:
        return True, "%s is a dependency manifest (%d in all)" % (hits[0], len(hits))
    return False, "none of the %d changed files is a dependency manifest" % len(paths)


def self_test():
    fails = []

    def expect(cond, what):
        print(("  ok: " if cond else "  FAIL: ") + what)
        if not cond:
            fails.append(what)

    runs = [
        "go.work", "backend/core/go.mod", "backend/services/x/go.sum", "go.work.sum",
        "frontend/package.json", "frontend/package-lock.json", "hack/api-report/package-lock.json",
        "sdks/csharp/src/DeviceChain.Client/DeviceChain.Client.csproj", "sdks/csharp/Directory.Packages.props",
        "global.json", "sdks/unity/Packages/manifest.json", "sdks/unity/Packages/packages-lock.json",
        "backend/services/lwm2m-ingest/interop/leshan-client/pom.xml",
        "deploy/images/timescaledb/Dockerfile", "x/Dockerfile.dev", "x/build.Dockerfile",
        ".github/workflows/ci.yml", ".github/actions/thing/action.yml", "docs/requirements.txt",
    ]
    for f in runs:
        expect(decide([f])[0], "%s runs the review" % f)
    expect(decide(["docs/x.md", "backend/core/go.mod"])[0], "one manifest among other files runs it")
    # THE COUNTERWEIGHTS: "always true" passes every case above.
    skips = ["docs/docs/x.md", "backend/core/rdb/x.go", "frontend/apps/console/src/a.tsx",
             "hack/ci-select.py", "deploy/helm/devicechain/values.yaml", "go.mode", "mygo.mod",
             "frontend/packages/x/package.json.bak", "sdks/unity/Runtime/manifest.json"]
    expect(decide(skips) == (False, "none of the %d changed files is a dependency manifest" % len(skips)),
           "a diff with no manifest skips it: %s" % (decide(skips),))
    # FAIL SAFE: nothing to decide from is never "skip".
    expect(decide([])[0] and decide(["", "  "])[0], "an empty list runs it")
    if fails:
        print("self-test FAILED: %d case(s)" % len(fails))
        sys.exit(1)
    print("self-test passed")


def main(argv):
    if argv == ["--self-test"]:
        self_test()
        return 0
    if argv:
        print(__doc__, file=sys.stderr)
        return 2
    verdict, why = decide(sys.stdin.read().splitlines())
    print("dependency-review: %s (%s)" % ("runs" if verdict else "skipped", why), file=sys.stderr)
    print("true" if verdict else "false")
    return 0


if __name__ == "__main__":
    sys.exit(main(sys.argv[1:]))
