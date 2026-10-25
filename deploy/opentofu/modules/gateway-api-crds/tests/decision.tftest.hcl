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
run "one_required_foreign_crd_under_the_minimum_skips" {
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

  assert {
    condition     = output.mode == "skip" && output.usable == false && length(helm_release.crds) == 0 && output.notice != ""
    error_message = "a set this module cannot use must SKIP (nothing written, a notice, usable=false) and must never fail the plan"
  }

  # A set the module cannot use is a WARNING in a real plan (a check block); the test
  # harness reports it as a failure, so the run names it as expected.
  expect_failures = [check.gateway_api_crds_usable]
}

run "an_old_foreign_set_is_skipped" {
  command = plan

  override_data {
    target = data.kubernetes_resources.gateways
    values = { objects = [{ metadata = { annotations = { "gateway.networking.k8s.io/bundle-version" = "v1.3.0" } } }] }
  }
  override_data {
    target = data.kubernetes_resources.httproutes
    values = { objects = [{ metadata = { annotations = { "gateway.networking.k8s.io/bundle-version" = "v1.3.0" } } }] }
  }

  assert {
    condition     = output.mode == "skip" && output.usable == false && length(helm_release.crds) == 0 && output.notice != ""
    error_message = "a set this module cannot use must SKIP (nothing written, a notice, usable=false) and must never fail the plan"
  }

  # A set the module cannot use is a WARNING in a real plan (a check block); the test
  # harness reports it as a failure, so the run names it as expected.
  expect_failures = [check.gateway_api_crds_usable]
}

# The message is what an operator reads, so what it says and what it must never say are part
# of the contract.
run "the_notice_names_the_floor_and_never_advises_a_bare_delete" {
  command = plan

  override_data {
    target = data.kubernetes_resources.gateways
    values = { objects = [{ metadata = { annotations = { "gateway.networking.k8s.io/bundle-version" = "v1.3.0" } } }] }
  }

  assert {
    condition     = local.mode == "skip" && strcontains(local.notice, "v1.5.1") && strcontains(local.notice, "provisional")
    error_message = "the notice must name the floor and say it is provisional; got: ${local.notice}"
  }

  assert {
    condition     = !strcontains(local.notice, "gcloud") && strcontains(local.notice, "deletes every Gateway and HTTPRoute")
    error_message = "the notice must not give a gcloud remedy, and must state the cascade wherever it mentions removing CRDs; got: ${local.notice}"
  }

  # A set the module cannot use is a WARNING in a real plan (a check block); the test
  # harness reports it as a failure, so the run names it as expected.
  expect_failures = [check.gateway_api_crds_usable]
}

run "an_unversioned_foreign_set_is_skipped_not_assumed_new_enough" {
  command = plan

  override_data {
    target = data.kubernetes_resources.gateways
    values = { objects = [{ metadata = { annotations = {} } }] }
  }

  assert {
    condition     = output.mode == "skip" && output.usable == false && length(helm_release.crds) == 0 && output.notice != ""
    error_message = "a set this module cannot use must SKIP (nothing written, a notice, usable=false) and must never fail the plan"
  }

  # A set the module cannot use is a WARNING in a real plan (a check block); the test
  # harness reports it as a failure, so the run names it as expected.
  expect_failures = [check.gateway_api_crds_usable]
}

run "a_partial_foreign_set_is_skipped" {
  command = plan

  # Only the gateways CRD, current. Everything Traefik also needs is absent.
  override_data {
    target = data.kubernetes_resources.gateways
    values = { objects = [{ metadata = { annotations = { "gateway.networking.k8s.io/bundle-version" = "v1.6.3" } } }] }
  }

  assert {
    condition     = output.mode == "skip" && output.usable == false && length(helm_release.crds) == 0 && output.notice != ""
    error_message = "a set this module cannot use must SKIP (nothing written, a notice, usable=false) and must never fail the plan"
  }

  # A set the module cannot use is a WARNING in a real plan (a check block); the test
  # harness reports it as a failure, so the run names it as expected.
  expect_failures = [check.gateway_api_crds_usable]
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
  assert {
    condition     = output.mode == "skip" && output.usable == false && length(helm_release.crds) == 0 && output.notice != ""
    error_message = "a set this module cannot use must SKIP (nothing written, a notice, usable=false) and must never fail the plan"
  }

  # A set the module cannot use is a WARNING in a real plan (a check block); the test
  # harness reports it as a failure, so the run names it as expected.
  expect_failures = [check.gateway_api_crds_usable]
}

run "a_set_that_is_part_ours_and_part_foreign_is_skipped" {
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

  assert {
    condition     = output.mode == "skip" && output.usable == false && length(helm_release.crds) == 0 && output.notice != ""
    error_message = "a set this module cannot use must SKIP (nothing written, a notice, usable=false) and must never fail the plan"
  }

  # A set the module cannot use is a WARNING in a real plan (a check block); the test
  # harness reports it as a failure, so the run names it as expected.
  expect_failures = [check.gateway_api_crds_usable]
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

# --- the required set --------------------------------------------------------------

# Pinned WITH its reason. Only the first two were measured (Traefik's reflector errors on a
# missing v1 BackendTLSPolicy or TLSRoute); grpcroutes and referencegrants are absent from
# the set a platform is reported to ship and are not measured as needed, so demanding them
# would skip every such cluster on an assumption. A change here is a decision, not a refactor.
run "the_required_set_is_what_traefik_needs_and_no_more" {
  command = plan

  assert {
    condition = jsonencode(sort(local.required_crds)) == jsonencode(sort([
      "backendtlspolicies.gateway.networking.k8s.io",
      "gatewayclasses.gateway.networking.k8s.io",
      "gateways.gateway.networking.k8s.io",
      "httproutes.gateway.networking.k8s.io",
      "tlsroutes.gateway.networking.k8s.io",
    ]))
    error_message = "required_crds must be exactly: backendtlspolicies and tlsroutes (measured), plus gatewayclasses, gateways, httproutes. grpcroutes/referencegrants are deliberately not required."
  }

  assert {
    condition     = !contains(local.required_crds, "grpcroutes.gateway.networking.k8s.io") && !contains(local.required_crds, "referencegrants.gateway.networking.k8s.io")
    error_message = "grpcroutes and referencegrants were never measured as needed and the reported GKE set has no grpcroutes"
  }
}

# --- a platform-managed (GKE-shaped) set ---------------------------------------------

# The reported GKE shape: v1.5.0, six CRDs, no grpcroutes, marked platform-managed. Below the
# provisional floor, so Traefik and the shim are skipped -- and the plan is NOT an error.
run "a_gke_shaped_set_skips_without_failing" {
  command = plan

  override_data {
    target = data.kubernetes_resources.gatewayclasses
    values = { objects = [{ metadata = { annotations = { "gateway.networking.k8s.io/bundle-version" = "v1.5.0" }, labels = { "components.gke.io/component-name" = "gateway-api-crds" } } }] }
  }
  override_data {
    target = data.kubernetes_resources.gateways
    values = { objects = [{ metadata = { annotations = { "gateway.networking.k8s.io/bundle-version" = "v1.5.0" }, labels = { "components.gke.io/component-name" = "gateway-api-crds" } } }] }
  }
  override_data {
    target = data.kubernetes_resources.httproutes
    values = { objects = [{ metadata = { annotations = { "gateway.networking.k8s.io/bundle-version" = "v1.5.0" }, labels = { "components.gke.io/component-name" = "gateway-api-crds" } } }] }
  }
  override_data {
    target = data.kubernetes_resources.referencegrants
    values = { objects = [{ metadata = { annotations = { "gateway.networking.k8s.io/bundle-version" = "v1.5.0" }, labels = { "components.gke.io/component-name" = "gateway-api-crds" } } }] }
  }
  override_data {
    target = data.kubernetes_resources.backendtlspolicies
    values = { objects = [{ metadata = { annotations = { "gateway.networking.k8s.io/bundle-version" = "v1.5.0" }, labels = { "components.gke.io/component-name" = "gateway-api-crds" } } }] }
  }
  override_data {
    target = data.kubernetes_resources.tlsroutes
    values = { objects = [{ metadata = { annotations = { "gateway.networking.k8s.io/bundle-version" = "v1.5.0" }, labels = { "components.gke.io/component-name" = "gateway-api-crds" } } }] }
  }

  assert {
    condition     = output.mode == "skip" && output.usable == false && length(helm_release.crds) == 0
    error_message = "a platform set below the floor must skip, writing nothing"
  }

  assert {
    condition     = strcontains(output.notice, "platform-managed") && strcontains(output.notice, "v1.5.0")
    error_message = "the notice must say the set is platform-managed and name the version found; got: ${output.notice}"
  }

  # A set the module cannot use is a WARNING in a real plan (a check block); the test
  # harness reports it as a failure, so the run names it as expected.
  expect_failures = [check.gateway_api_crds_usable]
}

# The same shape with the CRDs Traefik needs present at the floor and NO grpcroutes: usable.
run "a_platform_set_with_what_traefik_needs_is_accepted_without_grpcroutes" {
  command = plan

  override_data {
    target = data.kubernetes_resources.gatewayclasses
    values = { objects = [{ metadata = { annotations = { "gateway.networking.k8s.io/bundle-version" = "v1.5.1" }, labels = { "components.gke.io/component-name" = "gateway-api-crds" } } }] }
  }
  override_data {
    target = data.kubernetes_resources.gateways
    values = { objects = [{ metadata = { annotations = { "gateway.networking.k8s.io/bundle-version" = "v1.5.1" }, labels = { "components.gke.io/component-name" = "gateway-api-crds" } } }] }
  }
  override_data {
    target = data.kubernetes_resources.httproutes
    values = { objects = [{ metadata = { annotations = { "gateway.networking.k8s.io/bundle-version" = "v1.5.1" }, labels = { "components.gke.io/component-name" = "gateway-api-crds" } } }] }
  }
  override_data {
    target = data.kubernetes_resources.backendtlspolicies
    values = { objects = [{ metadata = { annotations = { "gateway.networking.k8s.io/bundle-version" = "v1.5.1" }, labels = { "components.gke.io/component-name" = "gateway-api-crds" } } }] }
  }
  override_data {
    target = data.kubernetes_resources.tlsroutes
    values = { objects = [{ metadata = { annotations = { "gateway.networking.k8s.io/bundle-version" = "v1.5.1" }, labels = { "components.gke.io/component-name" = "gateway-api-crds" } } }] }
  }

  assert {
    condition     = output.mode == "accept" && output.usable == true && length(helm_release.crds) == 0 && output.notice == ""
    error_message = "a platform set holding what Traefik needs at the floor must be accepted, never written, with no notice"
  }
}

# An annotation-style marker counts as well as a label.
run "the_platform_marker_may_be_an_annotation" {
  command = plan

  override_data {
    target = data.kubernetes_resources.gateways
    values = { objects = [{ metadata = { annotations = { "gateway.networking.k8s.io/bundle-version" = "v1.3.0", "components.gke.io/component-name" = "gateway-api-crds" } } }] }
  }

  assert {
    condition     = strcontains(output.notice, "platform-managed")
    error_message = "the platform marker must be recognised as an annotation too"
  }

  # A set the module cannot use is a WARNING in a real plan (a check block); the test
  # harness reports it as a failure, so the run names it as expected.
  expect_failures = [check.gateway_api_crds_usable]
}

# --- accept_unversioned does not excuse an incomplete set ---------------------------------

run "accept_unversioned_with_a_partial_set_still_skips" {
  command = plan

  variables {
    accept_unversioned_crds = true
  }

  override_data {
    target = data.kubernetes_resources.gateways
    values = { objects = [{ metadata = { annotations = {} } }] }
  }

  assert {
    condition     = output.mode == "skip" && length(helm_release.crds) == 0
    error_message = "accept_unversioned_crds waives the version, not completeness: a partial set must still skip"
  }

  # A set the module cannot use is a WARNING in a real plan (a check block); the test
  # harness reports it as a failure, so the run names it as expected.
  expect_failures = [check.gateway_api_crds_usable]
}
