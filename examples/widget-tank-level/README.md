<!--
Copyright The DeviceChain Authors
SPDX-License-Identifier: Apache-2.0
-->

# Example widget package: tank level

A complete, independently built DeviceChain widget package. It is deliberately **not** an npm
workspace of this repository and imports nothing from it: it depends on
`@devicechain/widget-sdk` and `@devicechain/dashboards` as peers, exactly as a third-party package
would.

| Widget id | Entry | Pulls in |
| --- | --- | --- |
| `acme/tank-level-card` | `./card` | the SDK root only (no ECharts, no MapLibre, no built-in widgets) |
| `acme/tank-level` | `.` (with the card) | also `@devicechain/widget-sdk/echarts` and so `echarts` |

The card widget also shows the authoring side of the contract: portable option text (`label`,
`help`, enum `valueLabels` and `placeholder`, plus optional host message ids), an option that names
a namespaced `control` a host may not implement (it then gets the generic field), and a settings
editor (`implementation.editor`) that writes only through the schema-checked `setOptions`.

```bash
npm install
npm run build
```

`hack/widget-package-proof.sh all` builds this directory against **packed** SDK tarballs in a
temporary copy, outside the repository, checks the result, and proves the check fails when a chart
import is planted in the DOM-only entry. CI runs it; on Windows, run it with Git Bash. Its `@devicechain/*` peer ranges are
the workspace's development version; set them to the released version when you copy this package
as a starting point.
