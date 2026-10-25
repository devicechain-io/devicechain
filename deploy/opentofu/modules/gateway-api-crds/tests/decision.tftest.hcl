# Copyright The DeviceChain Authors
# SPDX-License-Identifier: Apache-2.0

# The gateway-api-crds module's decision: install, keep converged, accept, or refuse.
#
# The CRD set it reads is somebody else's on a managed cluster, and the cost of getting
# this wrong is asymmetric. Writing over a platform-managed set fights the platform's
# reconcile; accepting a too-old set leaves Traefik's GatewayClass unaccepted forever;
# and a delete of any CRD cascades to every Gateway and route. Each branch of the rule
# is therefore a run block, including the refusals, and the REFUSALS are the ones to read
# first: a module that only ever accepts is the failure this exists to prevent.
#
# Mocked providers, no cluster. Run it with hack/check-tofu-module-tests.sh.

mock_provider "helm" {}
mock_provider "kubernetes" {}

# --- the shipped bundle -----------------------------------------------------------
#
# Everything the decision logic believes about "ours" comes from the vendored files, so
# these pin the files: a regenerated bundle that picked up the admission policy, lost a
# CRD, or lost the annotation that protects it from deletion fails here.

run "the_bundle_is_ten_standard_crds_at_the_pinned_version" {
  command = plan

  assert {
    condition     = length(local.crds) == 10
    error_message = "the v1.6.3 standard bundle is 10 CRDs; got ${length(local.crds)}"
  }

  assert {
    condition     = jsonencode(local.pinned_versions) == jsonencode(["v1.6.3"])
    error_message = "every vendored CRD must carry one bundle-version, v1.6.3; got ${jsonencode(local.pinned_versions)}"
  }

  assert {
    condition     = alltrue([for n, c in local.crds : c.metadata.annotations["gateway.networking.k8s.io/channel"] == "standard"])
    error_message = "every vendored CRD must be the standard channel: the experimental channel brings CRDs a stable install must not carry"
  }

  assert {
    condition     = alltrue([for n, c in local.crds : c.kind == "CustomResourceDefinition" && c.apiVersion == "apiextensions.k8s.io/v1"])
    error_message = "the chart must contain CustomResourceDefinitions and nothing else: the upstream bundle's ValidatingAdmissionPolicy denies writes of older CRDs and would block a managed platform's own reconcile"
  }

  assert {
    condition     = alltrue([for n, c in local.crds : c.metadata.annotations["helm.sh/resource-policy"] == "keep"])
    error_message = "every CRD must carry helm.sh/resource-policy: keep: it is what stops an uninstall, an orphaned module or a count flip from deleting the CRD and, with it, every Gateway and route"
  }

  assert {
    condition     = alltrue([for n in local.required_crds : contains(keys(local.crds), n)])
    error_message = "every CRD Traefik requires of a foreign set must be in our own"
  }
}

run "the_observed_list_is_exactly_the_shipped_crds" {
  command = plan

  assert {
    condition     = sort(keys(local.observed)) == sort(keys(local.crds))
    error_message = "the ten data sources must read exactly the CRDs the chart ships: ${jsonencode(sort(keys(local.observed)))} vs ${jsonencode(sort(keys(local.crds)))}"
  }
}

run "the_minimum_is_below_the_pin_and_is_the_measured_floor" {
  command = plan

  assert {
    condition     = local.minimum_version == "v1.5.1"
    error_message = "the minimum is v1.5.1, the oldest set Traefik 3.7 was measured to run against; v1.3.0 leaves the GatewayClass Accepted=Unknown forever"
  }

  assert {
    condition     = local.minimum_key < 100000000 * 1 + 10000 * 6 + 3
    error_message = "the minimum must be below the pin: a platform managing v1.5.x is fine, and demanding the pin would mean fighting it"
  }
}

# --- the decision -----------------------------------------------------------------

run "a_clean_cluster_gets_the_pinned_bundle" {
  command = plan

  assert {
    condition     = output.mode == "install"
    error_message = "nothing present must install the bundle"
  }

  assert {
    condition     = length(helm_release.crds) == 1
    error_message = "the release must exist on a clean cluster"
  }

  assert {
    condition     = helm_release.crds[0].max_history == 10
    error_message = "the release must bound its revision history"
  }
}

# A complete foreign set at v1.5.1: the GKE shape.
run "a_foreign_set_at_the_minimum_is_accepted_and_never_written" {
  command = plan

  override_data {
    target = data.kubernetes_resources.backendtlspolicies
    values = { objects = [{ metadata = { annotations = { "gateway.networking.k8s.io/bundle-version" = "v1.5.1" } } }] }
  }
  override_data {
    target = data.kubernetes_resources.gatewayclasses
    values = { objects = [{ metadata = { annotations = { "gateway.networking.k8s.io/bundle-version" = "v1.5.1" } } }] }
  }
  override_data {
    target = data.kubernetes_resources.gateways
    values = { objects = [{ metadata = { annotations = { "gateway.networking.k8s.io/bundle-version" = "v1.5.1" } } }] }
  }
  override_data {
    target = data.kubernetes_resources.grpcroutes
    values = { objects = [{ metadata = { annotations = { "gateway.networking.k8s.io/bundle-version" = "v1.5.1" } } }] }
  }
  override_data {
    target = data.kubernetes_resources.httproutes
    values = { objects = [{ metadata = { annotations = { "gateway.networking.k8s.io/bundle-version" = "v1.5.1" } } }] }
  }
  override_data {
    target = data.kubernetes_resources.referencegrants
    values = { objects = [{ metadata = { annotations = { "gateway.networking.k8s.io/bundle-version" = "v1.5.1" } } }] }
  }
  override_data {
    target = data.kubernetes_resources.tlsroutes
    values = { objects = [{ metadata = { annotations = { "gateway.networking.k8s.io/bundle-version" = "v1.5.1" } } }] }
  }

  assert {
    condition     = output.mode == "accept"
    error_message = "a complete foreign set at the minimum must be accepted"
  }

  assert {
    condition     = length(helm_release.crds) == 0
    error_message = "an accepted foreign set must NEVER be written: no release at all"
  }
}

# One CRD under the floor refuses the whole set, even when every other CRD is current.
run "one_foreign_crd_under_the_minimum_refuses" {
  command = plan

  override_data {
    target = data.kubernetes_resources.backendtlspolicies
    values = { objects = [{ metadata = { annotations = { "gateway.networking.k8s.io/bundle-version" = "v1.6.3" } } }] }
  }
  override_data {
    target = data.kubernetes_resources.gatewayclasses
    values = { objects = [{ metadata = { annotations = { "gateway.networking.k8s.io/bundle-version" = "v1.6.3" } } }] }
  }
  override_data {
    target = data.kubernetes_resources.gateways
    values = { objects = [{ metadata = { annotations = { "gateway.networking.k8s.io/bundle-version" = "v1.5.0" } } }] }
  }
  override_data {
    target = data.kubernetes_resources.grpcroutes
    values = { objects = [{ metadata = { annotations = { "gateway.networking.k8s.io/bundle-version" = "v1.6.3" } } }] }
  }
  override_data {
    target = data.kubernetes_resources.httproutes
    values = { objects = [{ metadata = { annotations = { "gateway.networking.k8s.io/bundle-version" = "v1.6.3" } } }] }
  }
  override_data {
    target = data.kubernetes_resources.referencegrants
    values = { objects = [{ metadata = { annotations = { "gateway.networking.k8s.io/bundle-version" = "v1.6.3" } } }] }
  }
  override_data {
    target = data.kubernetes_resources.tlsroutes
    values = { objects = [{ metadata = { annotations = { "gateway.networking.k8s.io/bundle-version" = "v1.6.3" } } }] }
  }

  expect_failures = [terraform_data.compatibility]
}

run "an_old_foreign_set_is_refused_with_the_remedy" {
  command = plan

  override_data {
    target = data.kubernetes_resources.gateways
    values = { objects = [{ metadata = { annotations = { "gateway.networking.k8s.io/bundle-version" = "v1.3.0" } } }] }
  }
  override_data {
    target = data.kubernetes_resources.httproutes
    values = { objects = [{ metadata = { annotations = { "gateway.networking.k8s.io/bundle-version" = "v1.3.0" } } }] }
  }

  expect_failures = [terraform_data.compatibility]
}

# The message is what an operator on GKE reads, so the remedy is part of the contract.
run "the_refusal_names_the_minimum_and_the_gke_remedy" {
  command = plan

  override_data {
    target = data.kubernetes_resources.gateways
    values = { objects = [{ metadata = { annotations = { "gateway.networking.k8s.io/bundle-version" = "v1.3.0" } } }] }
  }

  assert {
    condition     = local.mode == "refuse" && strcontains(local.refusal, "v1.5.1") && strcontains(local.refusal, "--gateway-api=standard")
    error_message = "the refusal must name the minimum and the GKE remedy; got: ${local.refusal}"
  }

  expect_failures = [terraform_data.compatibility]
}

run "an_unversioned_foreign_set_is_refused_not_assumed_new_enough" {
  command = plan

  override_data {
    target = data.kubernetes_resources.gateways
    values = { objects = [{ metadata = { annotations = {} } }] }
  }

  expect_failures = [terraform_data.compatibility]
}

run "a_partial_foreign_set_is_refused" {
  command = plan

  # Only the gateways CRD, current. Everything Traefik also needs is absent.
  override_data {
    target = data.kubernetes_resources.gateways
    values = { objects = [{ metadata = { annotations = { "gateway.networking.k8s.io/bundle-version" = "v1.6.3" } } }] }
  }

  expect_failures = [terraform_data.compatibility]
}

# A set that is ours at an older pin converges on the pin: this is the upgrade path, and
# it is the only case in which the module writes over something that already exists.
run "our_own_older_set_is_upgraded" {
  command = plan

  override_data {
    target = data.kubernetes_resources.gateways
    values = {
      objects = [{ metadata = { annotations = {
        "gateway.networking.k8s.io/bundle-version" = "v1.5.1"
        "meta.helm.sh/release-name"                = "gateway-api-crds"
        "meta.helm.sh/release-namespace"           = "gateway-api-system"
      } } }]
    }
  }

  assert {
    condition     = output.mode == "ours"
    error_message = "a set this module installed must be recognised as ours"
  }

  assert {
    condition     = length(helm_release.crds) == 1
    error_message = "our own set must stay under the release so it can be upgraded"
  }
}

# The same CRD, but owned by a release of the same name in ANOTHER namespace, is not ours.
run "a_same_named_release_elsewhere_is_not_ours" {
  command = plan

  override_data {
    target = data.kubernetes_resources.gateways
    values = {
      objects = [{ metadata = { annotations = {
        "gateway.networking.k8s.io/bundle-version" = "v1.5.1"
        "meta.helm.sh/release-name"                = "gateway-api-crds"
        "meta.helm.sh/release-namespace"           = "somewhere-else"
      } } }]
    }
  }

  # Foreign and partial (only one CRD), so it is refused rather than written over.
  expect_failures = [terraform_data.compatibility]
}

run "a_set_that_is_part_ours_and_part_foreign_is_refused" {
  command = plan

  override_data {
    target = data.kubernetes_resources.gateways
    values = {
      objects = [{ metadata = { annotations = {
        "gateway.networking.k8s.io/bundle-version" = "v1.6.3"
        "meta.helm.sh/release-name"                = "gateway-api-crds"
        "meta.helm.sh/release-namespace"           = "gateway-api-system"
      } } }]
    }
  }
  override_data {
    target = data.kubernetes_resources.httproutes
    values = { objects = [{ metadata = { annotations = { "gateway.networking.k8s.io/bundle-version" = "v1.6.3" } } }] }
  }

  expect_failures = [terraform_data.compatibility]
}

# A pre-release of a newer version, and the experimental channel, are both accepted: the
# rule is about the version, and the experimental channel is a superset of standard.
run "a_newer_prerelease_foreign_set_is_accepted" {
  command = plan

  override_data {
    target = data.kubernetes_resources.backendtlspolicies
    values = { objects = [{ metadata = { annotations = { "gateway.networking.k8s.io/bundle-version" = "v1.7.0-rc.3", "gateway.networking.k8s.io/channel" = "experimental" } } }] }
  }
  override_data {
    target = data.kubernetes_resources.gatewayclasses
    values = { objects = [{ metadata = { annotations = { "gateway.networking.k8s.io/bundle-version" = "v1.7.0-rc.3", "gateway.networking.k8s.io/channel" = "experimental" } } }] }
  }
  override_data {
    target = data.kubernetes_resources.gateways
    values = { objects = [{ metadata = { annotations = { "gateway.networking.k8s.io/bundle-version" = "v1.7.0-rc.3", "gateway.networking.k8s.io/channel" = "experimental" } } }] }
  }
  override_data {
    target = data.kubernetes_resources.grpcroutes
    values = { objects = [{ metadata = { annotations = { "gateway.networking.k8s.io/bundle-version" = "v1.7.0-rc.3", "gateway.networking.k8s.io/channel" = "experimental" } } }] }
  }
  override_data {
    target = data.kubernetes_resources.httproutes
    values = { objects = [{ metadata = { annotations = { "gateway.networking.k8s.io/bundle-version" = "v1.7.0-rc.3", "gateway.networking.k8s.io/channel" = "experimental" } } }] }
  }
  override_data {
    target = data.kubernetes_resources.referencegrants
    values = { objects = [{ metadata = { annotations = { "gateway.networking.k8s.io/bundle-version" = "v1.7.0-rc.3", "gateway.networking.k8s.io/channel" = "experimental" } } }] }
  }
  override_data {
    target = data.kubernetes_resources.tlsroutes
    values = { objects = [{ metadata = { annotations = { "gateway.networking.k8s.io/bundle-version" = "v1.7.0-rc.3", "gateway.networking.k8s.io/channel" = "experimental" } } }] }
  }

  assert {
    condition     = output.mode == "accept" && length(helm_release.crds) == 0
    error_message = "a newer, experimental-channel foreign set must be accepted and left alone"
  }
}

run "unversioned_is_accepted_only_when_the_operator_says_so" {
  command = plan

  variables {
    accept_unversioned_crds = true
  }

  override_data {
    target = data.kubernetes_resources.backendtlspolicies
    values = { objects = [{ metadata = { annotations = {} } }] }
  }
  override_data {
    target = data.kubernetes_resources.gatewayclasses
    values = { objects = [{ metadata = { annotations = {} } }] }
  }
  override_data {
    target = data.kubernetes_resources.gateways
    values = { objects = [{ metadata = { annotations = {} } }] }
  }
  override_data {
    target = data.kubernetes_resources.grpcroutes
    values = { objects = [{ metadata = { annotations = {} } }] }
  }
  override_data {
    target = data.kubernetes_resources.httproutes
    values = { objects = [{ metadata = { annotations = {} } }] }
  }
  override_data {
    target = data.kubernetes_resources.referencegrants
    values = { objects = [{ metadata = { annotations = {} } }] }
  }
  override_data {
    target = data.kubernetes_resources.tlsroutes
    values = { objects = [{ metadata = { annotations = {} } }] }
  }

  assert {
    condition     = output.mode == "accept" && length(helm_release.crds) == 0
    error_message = "accept_unversioned_crds must let a complete unversioned foreign set through, and still not write it"
  }
}
