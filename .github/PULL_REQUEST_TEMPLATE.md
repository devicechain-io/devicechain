<!-- Thanks for contributing to DeviceChain! Please fill out the sections below. -->

## Summary

<!-- What does this PR do and why? Link any related issue: Fixes #123 -->

## Changes

<!-- Bullet the notable changes. -->
-

## Checklist

- [ ] I have signed the [Contributor License Agreement](../CONTRIBUTING.md#contributor-license-agreement-required) (the CLA Assistant bot will prompt on this PR if not).
- [ ] New source files carry the SPDX header (`Copyright The DeviceChain Authors` / `Apache-2.0`).
- [ ] Local CI gates pass: `gofmt -l .` (empty), `go build ./...`, `go vet ./...`, `go test ./...`.
- [ ] Each new or changed **call site** of a helper has a test that would fail if one of its arguments were wrong — not just a test of the helper. A helper's own tests do not cover its callers, and coverage counts a call site as covered because it *ran*, not because anything asserted what it ran with.
- [ ] Area-specific checks pass where relevant (frontend `codegen`/`typecheck`/`build`, `helm lint`, `tofu validate`).
- [ ] The change is focused (one logical change) and the PR description explains the "why".
