# Copyright The DeviceChain Authors
# SPDX-License-Identifier: Apache-2.0

# The Gateway API STANDARD-channel CustomResourceDefinitions (G15). Gateways and
# HTTPRoutes are the routing objects the instance chart renders and Traefik serves, and
# cert-manager's Gateway shim watches them, so the CRDs are a CLUSTER prerequisite that
# has to exist before either.
#
# This module decides WHETHER to write the CRDs at all, because on some clusters
# somebody else already owns them (GKE installs and reconciles them with
# `--gateway-api=standard`, and an operator may have applied them by hand). The rule:
#
#   nothing present                   install the pinned bundle
#   present, ours (helm-owned here)   keep it converged on the pin (upgrade only)
#   present, NOT ours, >= minimum     ACCEPT, and never write. Not ours to manage, and a
#                                     write would fight whoever reconciles them.
#   present, NOT ours, < minimum      REFUSE at plan time, naming the remedy. Never overwrite.
#   present, NOT ours, incomplete     REFUSE: a half-installed set is neither ours to
#                                     complete nor something Traefik will run against.
#   part ours, part not               REFUSE: ownership is ambiguous.
#
# The minimum is v1.5.1, the oldest set Traefik 3.7 was measured to run on (v1.3.0 leaves
# its GatewayClass `Accepted=Unknown` forever). It is deliberately BELOW our pin: a
# platform that manages v1.5.x is fine, and demanding the pin would make us fight it.
#
# 🔴 THE BUNDLE IS CRDs ONLY. The upstream install bundle also carries a
# ValidatingAdmissionPolicy ("safe-upgrades") that denies writes of any
# gateway.networking.k8s.io CRD whose bundle-version matches v1.[0-5].x, so on a cluster
# whose platform manages an older set it would block the PLATFORM's own reconcile. It is
# stripped by hack/vendor-gateway-api-crds.sh and a test here pins that nothing but CRDs
# is in the chart.
#
# 🔴 DELETING THESE CRDs DELETES EVERY GATEWAY AND ROUTE IN THE CLUSTER, and
# `prevent_destroy` cannot be the protection: removing a module block orphans its
# resources, and orphans are destroyed WITHOUT consulting their lifecycle rules (the
# block that said prevent_destroy is no longer in the configuration to be asked). So the
# protection lives on the OBJECTS: every CRD carries `helm.sh/resource-policy: keep`, which
# makes helm leave it behind on uninstall no matter who asked or whether this
# configuration still exists. Destroying this release, flipping its count to 0, or
# dropping the module all leave the CRDs, and with them the routes, in place.

# Named so hack/check-tofu-module-tests.sh pins the provider version the shipped roots run (it only pins
# a provider the module declares), instead of testing against whatever the registry calls latest.
terraform {
  required_providers {
    helm = {
      source = "hashicorp/helm"
    }
    kubernetes = {
      source = "hashicorp/kubernetes"
    }
  }
}

variable "namespace" {
  description = "Namespace the helm release record lives in. The CRDs are cluster-scoped; this only holds helm's bookkeeping, and is created if absent."
  type        = string
  default     = "gateway-api-system"
}

variable "release_name" {
  description = "Helm release name. It is also how an existing CRD set is recognised as ours (the meta.helm.sh/release-name annotation), so renaming it makes a previous install look foreign."
  type        = string
  default     = "gateway-api-crds"
}

variable "accept_unversioned_crds" {
  description = <<-EOT
    Accept an existing, not-ours Gateway API CRD set whose CRDs carry no
    gateway.networking.k8s.io/bundle-version annotation. Off by default: without the
    annotation the version cannot be read, and treating "cannot tell" as "new enough"
    is the reading that lets an old set through. Turn it on only after checking the set
    is v1.5.1 or newer by other means.
  EOT
  type        = bool
  default     = false
}

locals {
  # The versions this module reasons about. The PIN is not a variable: it is whatever the
  # vendored files say (below), so there is no second copy to disagree with the CRDs that
  # actually install.
  minimum_version = "v1.5.1"

  # Everything the module knows about the bundle is derived from the files, so the names
  # it looks for, the version it reports and the channel it checks cannot drift from what
  # the chart installs.
  crds = {
    for f in fileset("${path.module}/chart/templates", "*.yaml") :
    yamldecode(file("${path.module}/chart/templates/${f}")).metadata.name => yamldecode(file("${path.module}/chart/templates/${f}"))
  }

  pinned_versions = distinct([for n, c in local.crds : c.metadata.annotations["gateway.networking.k8s.io/bundle-version"]])
  pinned_version  = local.pinned_versions[0]

  # What Traefik needs present to run at all. Measured on v1.5.1: a set missing
  # BackendTLSPolicy or TLSRoute (v1) makes its reflector error and the GatewayClass never
  # becomes Accepted. The remaining CRDs (listenersets, tcproutes, udproutes) are shipped by
  # us on a clean cluster but are not required of somebody else's set.
  required_crds = [
    "backendtlspolicies.gateway.networking.k8s.io",
    "gatewayclasses.gateway.networking.k8s.io",
    "gateways.gateway.networking.k8s.io",
    "grpcroutes.gateway.networking.k8s.io",
    "httproutes.gateway.networking.k8s.io",
    "referencegrants.gateway.networking.k8s.io",
    "tlsroutes.gateway.networking.k8s.io",
  ]

  # --- what the cluster holds today ------------------------------------------------
  observed = {
    "backendtlspolicies.gateway.networking.k8s.io" = try(data.kubernetes_resources.backendtlspolicies.objects[0], null)
    "gatewayclasses.gateway.networking.k8s.io"     = try(data.kubernetes_resources.gatewayclasses.objects[0], null)
    "gateways.gateway.networking.k8s.io"           = try(data.kubernetes_resources.gateways.objects[0], null)
    "grpcroutes.gateway.networking.k8s.io"         = try(data.kubernetes_resources.grpcroutes.objects[0], null)
    "httproutes.gateway.networking.k8s.io"         = try(data.kubernetes_resources.httproutes.objects[0], null)
    "listenersets.gateway.networking.k8s.io"       = try(data.kubernetes_resources.listenersets.objects[0], null)
    "referencegrants.gateway.networking.k8s.io"    = try(data.kubernetes_resources.referencegrants.objects[0], null)
    "tcproutes.gateway.networking.k8s.io"          = try(data.kubernetes_resources.tcproutes.objects[0], null)
    "tlsroutes.gateway.networking.k8s.io"          = try(data.kubernetes_resources.tlsroutes.objects[0], null)
    "udproutes.gateway.networking.k8s.io"          = try(data.kubernetes_resources.udproutes.objects[0], null)
  }
  present = [for n, o in local.observed : n if o != null]

  annotation = {
    for n, o in local.observed : n => {
      version  = o == null ? "" : try(o.metadata.annotations["gateway.networking.k8s.io/bundle-version"], "")
      owner    = o == null ? "" : try(o.metadata.annotations["meta.helm.sh/release-name"], "")
      owner_ns = o == null ? "" : try(o.metadata.annotations["meta.helm.sh/release-namespace"], "")
    }
  }
  ours    = [for n in local.present : n if local.annotation[n].owner == var.release_name && local.annotation[n].owner_ns == var.namespace]
  foreign = [for n in local.present : n if !contains(local.ours, n)]

  # A comparable number for "v1.5.1" (and "v1.7.0-rc.3", which compares as 1.7.0). -1 when
  # the annotation is absent or not a version, which is below every real one.
  version_pattern = "^v([0-9]+)\\.([0-9]+)\\.([0-9]+)"
  version_key = {
    for n in local.present : n => (
      can(regex(local.version_pattern, local.annotation[n].version))
      ? (tonumber(regex(local.version_pattern, local.annotation[n].version)[0]) * 100000000
        + tonumber(regex(local.version_pattern, local.annotation[n].version)[1]) * 10000
      + tonumber(regex(local.version_pattern, local.annotation[n].version)[2]))
      : -1
    )
  }
  minimum_key = (
    tonumber(regex(local.version_pattern, local.minimum_version)[0]) * 100000000
    + tonumber(regex(local.version_pattern, local.minimum_version)[1]) * 10000
    + tonumber(regex(local.version_pattern, local.minimum_version)[2])
  )

  unversioned = [for n in local.foreign : n if local.version_key[n] == -1]
  too_old     = [for n in local.foreign : n if local.version_key[n] != -1 && local.version_key[n] < local.minimum_key]
  missing     = [for n in local.required_crds : n if !contains(local.present, n)]

  # The conditional short-circuits at every OpenTofu version, which `&&` does not below
  # 1.10, so each arm is a flat ternary rather than a conjunction guarding an index.
  mode = (
    length(local.present) == 0 ? "install"
    : length(local.foreign) == 0 ? "ours"
    : length(local.ours) > 0 ? "refuse"
    : length(local.too_old) > 0 ? "refuse"
    : length(local.unversioned) > 0 ? (var.accept_unversioned_crds ? (length(local.missing) > 0 ? "refuse" : "accept") : "refuse")
    : length(local.missing) > 0 ? "refuse"
    : "accept"
  )

  refusal = (
    local.mode != "refuse" ? ""
    : length(local.ours) > 0
    ? "Some Gateway API CRDs on this cluster were installed by this module (${join(", ", local.ours)}) and some were not (${join(", ", local.foreign)}). Ownership is ambiguous, so nothing is written. Remove the foreign ones, or the ones this module installed, so one party owns the set."
    : length(local.too_old) > 0
    ? "The Gateway API CRDs on this cluster are older than ${local.minimum_version}, the oldest set Traefik 3.7 runs against (${join(", ", local.too_old)}). They were not installed by this module, so it will not overwrite them. On GKE, upgrade the cluster or run: gcloud container clusters update CLUSTER --gateway-api=standard. Elsewhere, apply the Gateway API ${local.pinned_version} standard CRDs yourself, or remove the old set so this module can install its own."
    : (length(local.unversioned) > 0 && !var.accept_unversioned_crds)
    ? "The Gateway API CRDs on this cluster carry no gateway.networking.k8s.io/bundle-version annotation (${join(", ", local.unversioned)}), so their version cannot be read and they were not installed by this module. If you have verified they are ${local.minimum_version} or newer, set accept_unversioned_crds = true."
    : "The Gateway API CRD set on this cluster is incomplete: ${join(", ", local.missing)} ${length(local.missing) == 1 ? "is" : "are"} missing, and Traefik will not run without ${length(local.missing) == 1 ? "it" : "them"}. The set was not installed by this module, so it will not complete it. Install the full standard channel (${local.pinned_version}), or remove the partial set so this module can."
  )
}

# One read per CRD, written out rather than looped: a test can override a data source by
# its address but not one instance of a for_each, and the per-CRD cases (a partial set, a
# set that is part ours) are the ones worth testing. tests/decision.tftest.hcl pins that
# these ten are exactly the CRDs the chart ships, so the list cannot drift from the files.
# A CRD that is not there returns ZERO objects rather than an error, so a clean cluster is
# the empty case and not a failure.
data "kubernetes_resources" "backendtlspolicies" {
  api_version    = "apiextensions.k8s.io/v1"
  kind           = "CustomResourceDefinition"
  field_selector = "metadata.name=backendtlspolicies.gateway.networking.k8s.io"
}

data "kubernetes_resources" "gatewayclasses" {
  api_version    = "apiextensions.k8s.io/v1"
  kind           = "CustomResourceDefinition"
  field_selector = "metadata.name=gatewayclasses.gateway.networking.k8s.io"
}

data "kubernetes_resources" "gateways" {
  api_version    = "apiextensions.k8s.io/v1"
  kind           = "CustomResourceDefinition"
  field_selector = "metadata.name=gateways.gateway.networking.k8s.io"
}

data "kubernetes_resources" "grpcroutes" {
  api_version    = "apiextensions.k8s.io/v1"
  kind           = "CustomResourceDefinition"
  field_selector = "metadata.name=grpcroutes.gateway.networking.k8s.io"
}

data "kubernetes_resources" "httproutes" {
  api_version    = "apiextensions.k8s.io/v1"
  kind           = "CustomResourceDefinition"
  field_selector = "metadata.name=httproutes.gateway.networking.k8s.io"
}

data "kubernetes_resources" "listenersets" {
  api_version    = "apiextensions.k8s.io/v1"
  kind           = "CustomResourceDefinition"
  field_selector = "metadata.name=listenersets.gateway.networking.k8s.io"
}

data "kubernetes_resources" "referencegrants" {
  api_version    = "apiextensions.k8s.io/v1"
  kind           = "CustomResourceDefinition"
  field_selector = "metadata.name=referencegrants.gateway.networking.k8s.io"
}

data "kubernetes_resources" "tcproutes" {
  api_version    = "apiextensions.k8s.io/v1"
  kind           = "CustomResourceDefinition"
  field_selector = "metadata.name=tcproutes.gateway.networking.k8s.io"
}

data "kubernetes_resources" "tlsroutes" {
  api_version    = "apiextensions.k8s.io/v1"
  kind           = "CustomResourceDefinition"
  field_selector = "metadata.name=tlsroutes.gateway.networking.k8s.io"
}

data "kubernetes_resources" "udproutes" {
  api_version    = "apiextensions.k8s.io/v1"
  kind           = "CustomResourceDefinition"
  field_selector = "metadata.name=udproutes.gateway.networking.k8s.io"
}

# The refusal. It fires at PLAN time, before anything is written, and it is in the module
# rather than in dcctl so that `tofu apply` run directly cannot bypass it.
resource "terraform_data" "compatibility" {
  input = local.mode != "refuse"

  lifecycle {
    precondition {
      condition     = local.mode != "refuse"
      error_message = local.refusal
    }
  }
}

resource "helm_release" "crds" {
  # 1 for a clean cluster or our own set, 0 when the set is somebody else's. A count of 0
  # on an existing release destroys the RELEASE, never the CRDs (resource-policy: keep).
  count = contains(["install", "ours"], local.mode) ? 1 : 0

  max_history      = local.helm_max_history
  name             = var.release_name
  namespace        = var.namespace
  create_namespace = true
  chart            = "${path.module}/chart"

  depends_on = [terraform_data.compatibility]
}

output "mode" {
  description = "install, ours or accept: whether this module wrote the CRDs, and whether it will leave them alone."
  value       = local.mode
}

output "pinned_version" {
  description = "The Gateway API version this module installs on a clean cluster."
  value       = local.pinned_version
}

output "minimum_version" {
  description = "The oldest Gateway API version this module accepts when somebody else owns the CRDs."
  value       = local.minimum_version
}

output "namespace" {
  value = var.namespace
}
