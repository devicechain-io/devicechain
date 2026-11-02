# DeviceChain Documentation

User-facing documentation for DeviceChain, built with [Docusaurus 3](https://docusaurus.io/).
Content lives under [`docs/`](./docs); some of it also feeds the public website.

## Versions

The site is versioned (Docusaurus docs versioning). `docs/docs` and each locale's
`i18n/<locale>/docusaurus-plugin-content-docs/current` are the LIVING docs for the next,
unreleased release; this is the only place to edit. Frozen snapshots sit beside them:

| What | Where | URL |
|---|---|---|
| Latest release (`lastVersion` in `docusaurus.config.ts`) | `versioned_docs/version-X.Y`, `versioned_sidebars/`, `i18n/<locale>/.../version-X.Y` | `/…` (the site root) |
| Next, unreleased (labelled with a banner) | `docs/` and `.../current` | `/next/…` |
| An older release | `versioned_docs/version-X.Y` | `/X.Y/…` |

`versions.json` lists the snapshots, newest first. Do not edit a snapshot except to fix a
mistake in what that release shipped.

To snapshot a release (for example 1.1), after the tag and before editing for the next
release, from this directory: run `npm run prebuild` (the generated GraphQL reference must
exist), then `npx docusaurus docs:version 1.1`. That copies `current` into
`versioned_docs/version-1.1` and `versioned_sidebars`, and copies each locale's `current`
into `version-1.1`. Then set `lastVersion` and add a `'1.1'` entry to `versions` in
`docusaurus.config.ts`, and commit. Check that every locale got its copy (the
locale-parity guard fails otherwise).

Guards follow the living docs (translation freshness, the docs-drafts rot guard, alert
coverage, the GraphQL and command example checks, and the Go tests that read pages),
except ADR-reference scanning, locale parity and structure parity, which cover the
snapshots too. Each states its choice where it is configured.

## Local development

```bash
cd docs
npm install
npm start          # dev server with hot reload at http://localhost:3000
```

## Build

```bash
npm run build      # static site into build/
npm run serve      # preview the production build
```

## Structure

Documentation follows the [Diátaxis](https://diataxis.fr/) model:

| Folder | Purpose |
|---|---|
| `docs/intro.md` | What DeviceChain is, who it's for |
| `docs/concepts/` | Explanation — architecture, domain model, multi-tenancy |
| `docs/guides/` | How-to — local dev, connecting a device, deployment tasks |
| `docs/reference/` | Reference — GraphQL API, configuration |
| `docs/deployment/` | Operating DeviceChain — operator, OpenTofu, scaling |

The sidebar is defined in `sidebars.ts`. Add the page ID there when adding a
Markdown file; Concepts has groups that do not correspond to folders on disk.
Translate category labels and generated-index descriptions in each locale's
`docusaurus-plugin-content-docs/current.json`.

## Maintaining Chinese translations

English is the reference documentation and the default site language. Simplified
Chinese lives under `i18n/zh-CN/`, at `/zh-CN/`. Translate navigation in the
theme JSON files and sidebar categories in
`i18n/zh-CN/docusaurus-plugin-content-docs/current.json`. Keep page paths, doc IDs,
code examples, configuration keys and explicitly pinned heading anchors stable.

Translations can be refreshed occasionally; an English edit does not require
an immediate Chinese rewrite. From this directory, run `npm run translations:status`
to list English changes since the last reviewed translation. Notices do not fail
the command; `npm run translations:status -- --strict` is an optional release review
gate. Invalid manifests or command arguments always fail. The file-set parity
check and Docusaurus broken-link checks are separate checks of structural completeness.

After reviewing and updating a specific translated page against English, record
the corresponding source and translation hashes:

```bash
npm run translations:status -- --record concepts/architecture.md
# Repeat --record to acknowledge more than one reviewed page.
npm run translations:status -- --record intro.md --record quickstart/first-device.md
```

Spanish (`i18n/es/`) is tracked the same way: pass `--locale es`, for example
`npm run translations:status -- --locale es --record intro.md`.

Commit `i18n/<locale>/source-manifest.json` with those translations. The command
requires both files to exist and validates all requested paths before recording
any of them. It normalizes line endings before hashing. Recording is an explicit
review acknowledgment, not a translation operation or a proof of translation
quality; do not record an unchanged stale translation just to clear a notice.

## Public content

This is **public** documentation. Product strategy, competitive analysis, and
roadmap rationale live in the private strategy repo, not here — keep that
boundary strict when adding content.
