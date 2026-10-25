# Copyright The DeviceChain Authors
# SPDX-License-Identifier: Apache-2.0

# cert-manager (ADR-002): issues and renews the TLS certificates that secure the
# ingress. OpenTofu installs cert-manager and its CRDs; the cert Issuer (and the
# Certificate, via ingress-shim) is rendered by the Helm chart once these CRDs
# exist — which also avoids the Terraform "create a custom resource before its
# CRD exists" plan-time problem.

# Named so hack/check-tofu-module-tests.sh pins the provider version the shipped roots run (it only pins
# a provider the module declares), instead of testing against whatever the registry calls latest.
terraform {
  required_providers {
    helm = {
      source = "hashicorp/helm"
    }
  }
}

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

variable "enable_gateway_api" {
  description = <<-EOT
    Turn on cert-manager's Gateway API certificate shim (config.enableGatewayAPI), which
    is what issues a Certificate for a Gateway listener that carries the cert-manager
    issuer annotation. Off by default so the module alone behaves as it always did.

    🔴 THE GATEWAY API CRDs MUST EXIST BEFORE THIS RELEASE STARTS. With the shim on and the
    CRDs absent the controller EXITS ("the Gateway API CRDs do not seem to be present") and
    crash-loops: measured at two restarts before it recovered once the CRDs landed, with a
    CrashLoopBackOff that can stretch to five minutes, during which a `helm --wait` on this
    release fails. The caller therefore orders the CRD module first (depends_on), and an
    UPGRADE that adds the CRDs to a cluster already running cert-manager restarts the pod
    anyway because this value changes the release.
  EOT
  type        = bool
  default     = false
}

resource "helm_release" "cert_manager" {
  max_history      = local.helm_max_history
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

  # The controller configuration file. A ControllerConfiguration document REPLACES the
  # chart's defaults for the keys it states, so it states only the one it exists for.
  values = var.enable_gateway_api ? [yamlencode({
    config = {
      apiVersion       = "controller.config.cert-manager.io/v1alpha1"
      kind             = "ControllerConfiguration"
      enableGatewayAPI = true
    }
  })] : []
}

output "namespace" {
  value = var.namespace
}
