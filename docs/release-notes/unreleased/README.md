# Release-notes fragments

A pull request that needs a line in the release notes adds **one new file here** instead of editing
`docs/docs/deployment/releases-and-upgrades.md` or its translations. New files never conflict with
each other, so one merge no longer makes every other open pull request conflict.

This directory is not part of the docs site: nothing here is rendered or versioned. The
release-notes pull request runs `hack/assemble-release-notes.py`, which inserts every fragment
under the in-development version's headings in all three locales and deletes the fragments.

## Format

Name the file `<slug>.md`, using lowercase letters, digits and hyphens, for example
`mcp-inflight-limit.md`:

```markdown
---
section: before-upgrade
---

## en

- **The thing.** What changed, and what an operator does about it.

## es

- **La cosa.** Qué cambió y qué hace el operador al respecto.

## zh-CN

- **该事项。** 发生了什么变化，以及运维人员需要做什么。
```

- `section:` is `before-upgrade` (what an operator must do or know before or during the upgrade)
  or `whats-new` (everything else). It selects the "Before you upgrade to vX.Y.0" or
  "What is new in vX.Y.0" heading of the version in development.
- `## en`, `## es` and `## zh-CN` each appear exactly once. Each body is inserted as written.
- A bullet (or paragraph) goes at the end of the heading's list. A body may instead start its own
  subsection with a `####` heading carrying an explicit anchor, such as
  `#### Deleted devices end their updates {#v170-deleted-device-updates}`; the anchor is the same
  in all three locales and must not already be on the page.
- The three locales must have the same headings, anchors, code fences and admonitions.
- Do not cite internal design-decision numbers; the text is published.

`hack/assemble-release-notes.py --check` validates every fragment, and the docs CI job runs it.
