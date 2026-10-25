# Copyright The DeviceChain Authors
# SPDX-License-Identifier: Apache-2.0

# The Gateway API STANDARD-channel CustomResourceDefinitions (G15). Gateways and
# HTTPRoutes are the routing objects the instance chart renders and Traefik serves, and
# cert-manager's Gateway shim watches them, so the CRDs are a CLUSTER prerequisite that
# has to exist before either.
#
# This module decides WHETHER to write the CRDs at all, because on some clusters
# somebody else already owns them. GKE installs and reconciles a set of its own when the
# Gateway API is enabled (always, on Autopilot), marking it `components.gke.io/component-name`;
# an operator may have applied one by hand. The rule:
#
#   nothing present                   install the pinned bundle
#   present, ours (helm-owned here)   keep it converged on the pin (upgrade only)
#   present, NOT ours, usable         ACCEPT, and never write. Not ours to manage, and a
#                                     write would fight whoever reconciles them.
#   present, NOT ours, not usable     SKIP: write nothing, and let the root skip Traefik and
#                                     cert-manager's Gateway shim, with a warning saying why.
#   part ours, part not               SKIP: ownership is ambiguous.
#
# "Usable" means every CRD Traefik 3.7.14 needs is present (local.required_crds) at a
# version at or above local.minimum_version. It is NEVER an error. This module lives in the
# cluster root, so a failure here would block every other cluster-prerequisite change on a
# cluster whose platform owns a Gateway API set we cannot use: the database operator, the
# monitoring stack, cert-manager itself.
#
# 🔴 THE FLOOR AND THE REQUIRED SET ARE PROVISIONAL until measured on GKE (slice S5). What was
# measured on kind, against Traefik 3.7: a set missing BackendTLSPolicy or TLSRoute (v1)
# makes its reflector error and the GatewayClass is never Accepted; v1.3.0 does the same;
# v1.5.1 works. Nothing between v1.3.0 and v1.5.1 was tried, and no GKE-managed set has been
# looked at. GKE's reported bundle is v1.5.0 with only six CRDs, so this is expected to
# SKIP on GKE until S5 measures what actually runs there and moves the floor or the set.
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
  # it looks for, and the version it reports cannot drift from what the chart installs (a test
  # pins the channel and the keep annotation too).
  crds = {
    for f in fileset("${path.module}/chart/templates", "*.yaml") :
    yamldecode(file("${path.module}/chart/templates/${f}")).metadata.name => yamldecode(file("${path.module}/chart/templates/${f}"))
  }

  pinned_versions = distinct([for n, c in local.crds : c.metadata.annotations["gateway.networking.k8s.io/bundle-version"]])
  pinned_version  = local.pinned_versions[0]

  # What Traefik 3.7.14 needs present to run, with the reason for each. Only the first two
  # groups were MEASURED; the rest is "a Gateway cannot exist without it".
  #   backendtlspolicies, tlsroutes  MEASURED on kind: Traefik's reflector errors on a missing
  #                                  v1 BackendTLSPolicy or TLSRoute and the GatewayClass stays
  #                                  Accepted=Unknown forever.
  #   gatewayclasses, gateways,      the objects the controller is registered for and the
  #   httproutes                     instance chart renders.
  # NOT required, deliberately: grpcroutes and referencegrants (not measured as needed, and
  # the reported GKE set has no grpcroutes, so demanding it would skip every GKE cluster on
  # an unmeasured assumption) and listenersets, tcproutes, udproutes (shipped by us on a clean
  # cluster, not asked of somebody else's). tests/decision.tftest.hcl pins this list.
  required_crds = [
    "backendtlspolicies.gateway.networking.k8s.io",
    "gatewayclasses.gateway.networking.k8s.io",
    "gateways.gateway.networking.k8s.io",
    "httproutes.gateway.networking.k8s.io",
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
      # GKE marks the CRDs it manages with this, as a label on some releases and an
      # annotation on others; either counts.
      platform = o == null ? "" : try(o.metadata.labels["components.gke.io/component-name"], try(o.metadata.annotations["components.gke.io/component-name"], ""))
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

  platform_managed = [for n in local.foreign : n if local.annotation[n].platform != ""]

  # Only the CRDs Traefik needs are held to the floor: a platform's extra or missing CRDs
  # outside that set are not our business.
  foreign_required = [for n in local.foreign : n if contains(local.required_crds, n)]
  unversioned      = [for n in local.foreign_required : n if local.version_key[n] == -1]
  too_old          = [for n in local.foreign_required : n if local.version_key[n] != -1 && local.version_key[n] < local.minimum_key]
  missing          = [for n in local.required_crds : n if !contains(local.present, n)]

  # The conditional short-circuits at every OpenTofu version, which `&&` does not below
  # 1.10, so each arm is a flat ternary rather than a conjunction guarding an index.
  mode = (
    length(local.present) == 0 ? "install"
    : length(local.foreign) == 0 ? "ours"
    : length(local.ours) > 0 ? "skip"
    : length(local.too_old) > 0 ? "skip"
    : length(local.missing) > 0 ? "skip"
    : (length(local.unversioned) > 0 && !var.accept_unversioned_crds) ? "skip"
    : "accept"
  )
  usable = local.mode != "skip"

  who = length(local.platform_managed) > 0 ? " They are platform-managed (components.gke.io/component-name), so this module will not change them." : " They were not installed by this module, so it will not change them."

  # The cascade is in every message that could be read as "remove them": deleting a Gateway
  # API CRD deletes every Gateway and HTTPRoute of that kind on the cluster, the operator's
  # own included, and a platform that manages the set re-creates the CRDs.
  cascade = " Do not delete the CRDs to make room: deleting a Gateway API CRD deletes every Gateway and HTTPRoute on the cluster, including your own, and a platform that manages them re-creates the CRDs."

  notice = (
    local.usable ? ""
    : length(local.ours) > 0
    ? "Traefik and cert-manager's Gateway API shim were SKIPPED: some Gateway API CRDs on this cluster were installed by this module (${join(", ", local.ours)}) and some were not (${join(", ", local.foreign)}), so ownership is ambiguous and nothing was written. Make one party own the whole set.${local.cascade}"
    : length(local.too_old) > 0
    ? "Traefik and cert-manager's Gateway API shim were SKIPPED: the Gateway API CRDs on this cluster are older than ${local.minimum_version} (found ${join(", ", distinct([for n in local.too_old : local.annotation[n].version]))} on ${join(", ", local.too_old)}), the floor Traefik 3.7 was measured on (provisional until measured on GKE).${local.who} Move the set to ${local.minimum_version} or newer through whatever manages it.${local.cascade}"
    : length(local.missing) > 0
    ? "Traefik and cert-manager's Gateway API shim were SKIPPED: the Gateway API CRDs on this cluster lack ${join(", ", local.missing)}, which Traefik 3.7.14 needs.${local.who} Bring the set up to include them through whatever manages it.${local.cascade}"
    : "Traefik and cert-manager's Gateway API shim were SKIPPED: the Gateway API CRDs on this cluster carry no gateway.networking.k8s.io/bundle-version annotation (${join(", ", local.unversioned)}), so their version cannot be read.${local.who} If you have verified they are ${local.minimum_version} or newer, set accept_unversioned_crds = true.${local.cascade}"
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

# A set we cannot use is a WARNING, never an error: see the header for why. `check` blocks
# report a failed assertion without failing the plan.
check "gateway_api_crds_usable" {
  assert {
    condition     = local.usable
    error_message = local.notice
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

}

output "usable" {
  description = "True when the cluster has a Gateway API set Traefik and cert-manager's shim can run against (installed here, ours, or accepted). False means skip both."
  value       = local.usable
}

output "notice" {
  description = "Why the set is not usable, or empty."
  value       = local.notice
}

output "mode" {
  description = "install, ours, accept or skip: whether this module wrote the CRDs, left them alone, or found a set it cannot use."
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
