# DeviceChain — Repository Guide

## License headers

DeviceChain is licensed under **Apache License 2.0** (see [LICENSE](LICENSE) and [NOTICE](NOTICE)).

**Every source file must begin with this two-line SPDX header:**

```go
// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0
```

Notes:
- Copyright is held by **"The DeviceChain Authors"** — there is no single corporate owner. Do not add a year and do not attribute to any individual or company.
- New `.go` files **must** include the header (followed by a blank line, then the build tags / `package` clause).
- For files with `//go:build` constraints, the header goes **above** the build tag, separated by a blank line.
- Generated code carries the same header. The controller-gen boilerplate lives at
  [backend/k8s/hack/boilerplate.go.txt](backend/k8s/hack/boilerplate.go.txt); keep it in sync with the header above.

To apply or check headers in bulk:

```bash
# add header to any missing files
go run github.com/google/addlicense@latest -f hack/license-header.txt backend

# verify all files have it (CI will enforce this)
go run github.com/google/addlicense@latest -check -f hack/license-header.txt backend
```

(The CI `license` job enforces this on every PR and push to `main` — see
[.github/workflows/ci.yml](.github/workflows/ci.yml). It checks Go + hand-authored
GraphQL/proto sources; controller-gen-generated YAML and the operator Dockerfile are
excluded because a header would drift away on every regeneration.)
