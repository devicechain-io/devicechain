# Copyright The DeviceChain Authors
# SPDX-License-Identifier: Apache-2.0

# cert-manager (ADR-002): issues and renews the TLS certificates that secure the
# ingress. OpenTofu installs cert-manager and its CRDs; the cert Issuer (and the
# Certificate, via ingress-shim) is rendered by the Helm chart once these CRDs
# exist — which also avoids the Terraform "create a custom resource before its
# CRD exists" plan-time problem.

variable "namespace" {
  type    = string
  default = "cert-manager"
}

variable "release_name" {
  type    = string
  default = "cert-manager"
}

variable "chart_version" {
  description = <<-EOT
    cert-manager chart version. PINNED — this module exists to put CRDs on the
    cluster, and the value that asks for them is version-sensitive (see the crds
    block below). v1.21.1 is the version that path was verified against.

    Bumping is a deliberate act: render the new chart and count the rendered
    CustomResourceDefinitions before trusting it. Keep the root's
    cert_manager_chart_version in step (hack/check-chart-pins.sh enforces that they
    agree — the root's value is the one that installs).
  EOT
  type        = string
  default     = "v1.21.1"

  # Fail closed. An empty string used to mean "install latest", which made the chart
  # version resolve at APPLY time — the chart repository became a dependency of
  # planning, and a repo hiccup surfaced as the helm provider's "inconsistent final
  # plan ... .version: was known, but now unknown", naming neither the chart nor the
  # network. The regex also refuses a helm version RANGE ("4.15.1 - 5.0.0"), which is
  # a constraint resolved at apply time wearing a pin's clothes.
  validation {
    condition     = can(regex("^v?[0-9]+\\.[0-9]+\\.[0-9]+(-[0-9A-Za-z.]+)?$", var.chart_version))
    error_message = "chart_version must be an exact chart version (e.g. \"1.2.3\" or \"v1.2.3\"); an empty value or a version range is resolved at apply time, which is not a pin."
  }
}

resource "helm_release" "cert_manager" {
  name             = var.release_name
  namespace        = var.namespace
  create_namespace = true
  repository       = "https://charts.jetstack.io"
  chart            = "cert-manager"
  version          = var.chart_version

  # Install the cert-manager CRDs with the chart so the Issuer/Certificate kinds
  # the Helm chart renders are available.
  #
  # 🔴 `crds.enabled`, NOT the older `installCRDs`. The two are aliases for the same
  # template guard and both still work at the pinned version — measured against
  # v1.21.1, each renders exactly 6 CRDs, as does setting both, and setting NEITHER
  # renders 0. But `installCRDs` is the deprecated one and `crds.enabled` DEFAULTS TO
  # FALSE, so the day upstream drops the alias, an install that still asks the old way
  # does not fail: helm ignores a value the chart no longer declares, and cert-manager
  # comes up healthy with no CRDs at all. The first thing to notice would be the
  # instance chart's Issuer failing to apply, one step later and nowhere near here.
  #
  # 🔴 The hazard is symmetric, and the other direction is reachable TODAY: moving the
  # pin BACKWARDS past the release that introduced `crds.enabled` produces exactly the
  # same silent zero-CRD install, because that chart declares only `installCRDs`. Bumps
  # here are forward moves in practice, but terraform.tfvars.example does invite moving
  # this pin, so: whichever way you move it, render the chart and COUNT the
  # CustomResourceDefinitions before believing the apply.
  set {
    name  = "crds.enabled"
    value = "true"
  }
}

output "namespace" {
  value = var.namespace
}
