# DeviceChain Documentation

User-facing documentation for DeviceChain, built with [Docusaurus 3](https://docusaurus.io/).
Content lives under [`docs/`](./docs); some of it also feeds the public website.

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

Commit `i18n/zh-CN/source-manifest.json` with those translations. The command
requires both files to exist and validates all requested paths before recording
any of them. It normalizes line endings before hashing. Recording is an explicit
review acknowledgment, not a translation operation or a proof of translation
quality; do not record an unchanged stale translation just to clear a notice.

## Public content

This is **public** documentation. Product strategy, competitive analysis, and
roadmap rationale live in the private strategy repo, not here — keep that
boundary strict when adding content.
