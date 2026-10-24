# Security Policy

## Reporting a vulnerability

**Please do not report security vulnerabilities through public GitHub issues,
discussions, or pull requests.**

Report privately, by either route:

1. **GitHub private vulnerability reporting (preferred).** Open the
   [Security tab](https://github.com/devicechain-io/devicechain/security/advisories/new)
   and choose *Report a vulnerability*. The report is visible only to the maintainers; a fix
   is developed privately and published as a GitHub Security Advisory (with a CVE where one
   applies).
2. **Email** **admin@devicechain.io**, if you cannot use GitHub.

Include, as far as you can:

- A description of the vulnerability and its potential impact.
- Steps to reproduce (proof-of-concept, affected version/commit, configuration).
- Any suggested remediation, if you have one.

## What to expect

We aim to meet these targets, but they are targets, not guarantees: DeviceChain is maintained
by a small team, and a hard report can take longer.

| Step | Target |
| --- | --- |
| Acknowledge your report | within 3 business days |
| Initial assessment (valid? severity? affected versions?) | within 7 days |
| Fix released, by severity | Critical: 14 days. High: 30 days. Medium: 90 days. Low: the next scheduled release |
| Advisory published | with the fix, crediting you unless you ask us not to |

We aim to keep you informed as we go, and to tell you if a target will be missed, and why. We
ask for a reasonable opportunity to release a fix before any public disclosure (coordinated
disclosure), and will seek to agree a disclosure date with you; the default is the day the fix ships.

## Supported versions

From `1.0.0`:

| Version | Security fixes |
| --- | --- |
| Latest `1.x` minor release | All severities |
| Previous `1.x` minor release | High and Critical, for 90 days after the next minor ships |
| Anything older, and all `0.x` releases | None. Upgrade to a supported version |

Fixes ship as patch releases (`1.x.y`). See the
[upgrade guide](https://docs.devicechain.io/deployment/releases-and-upgrades) for moving
between versions.

Until `1.0.0` is released, DeviceChain is pre-1.0 and only `main` is supported; there are no
backported patch releases.

## Verifying a release

From `v0.25.0`, every container image and the Helm chart a release publishes is signed (keyless, with
Sigstore, bound to this repository's release workflow) and carries an SBOM and SLSA build
provenance. To check a release, fetch the script from the tag you are verifying and run it:

```bash
curl -fsSLO https://raw.githubusercontent.com/devicechain-io/devicechain/v0.25.0/hack/verify-release-signatures.sh
chmod +x verify-release-signatures.sh
./verify-release-signatures.sh v0.25.0
```

[Verifying releases](https://docs.devicechain.io/deployment/verifying-releases) explains what
is checked, the `cosign` commands to verify a single image by hand, and what a signature does
and does not tell you.

## Scope

This policy covers the DeviceChain source in this repository and the artifacts built from it
(container images, the Helm chart, `dcctl`, the edge agent and the published SDK packages). For
issues in third-party dependencies, please also report upstream where appropriate.

Examples of what is in scope: crossing a tenant boundary, bypassing authentication or
authorization, executing code, reaching a network location the egress guard is meant to deny,
reading or forging a secret, or tampering with a release artifact.

---

DeviceChain is stewarded by IoT Innovations, LLC.
