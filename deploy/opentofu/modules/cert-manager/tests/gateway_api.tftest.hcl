# Copyright The DeviceChain Authors
# SPDX-License-Identifier: Apache-2.0

# cert-manager's Gateway API shim is a controller CONFIGURATION key, and the chart does not
# fail on one it does not apply: the install comes up healthy and never issues a
# certificate for a Gateway. So the value that turns it on is read back out of what the
# module hands the chart. The CRD ordering that makes it safe to turn on is the caller's
# (cluster/main.tf), and the root's own tests pin that.
#
# Mocked providers, no cluster. Run it with hack/check-tofu-module-tests.sh.

mock_provider "helm" {}

run "the_shim_is_off_unless_asked_for" {
  command = plan

  assert {
    condition     = length(helm_release.cert_manager.values) == 0
    error_message = "with enable_gateway_api unset the module must hand the chart no configuration, so it behaves as it always did"
  }
}

run "the_shim_is_configured_when_enabled" {
  command = plan

  variables {
    enable_gateway_api = true
  }

  assert {
    condition     = yamldecode(helm_release.cert_manager.values[0]).config.enableGatewayAPI == true
    error_message = "config.enableGatewayAPI must be true: it is what issues a Certificate for a Gateway listener"
  }

  assert {
    condition     = yamldecode(helm_release.cert_manager.values[0]).config.kind == "ControllerConfiguration" && yamldecode(helm_release.cert_manager.values[0]).config.apiVersion == "controller.config.cert-manager.io/v1alpha1"
    error_message = "the config document must name its kind and apiVersion or the chart renders a file the controller refuses to load"
  }

  assert {
    condition     = helm_release.cert_manager.version == "v1.21.1"
    error_message = "the Gateway API shim was verified against cert-manager v1.21.1; move the pin only with a re-run of that check"
  }
}

run "the_crds_still_install_with_the_chart" {
  command = plan

  variables {
    enable_gateway_api = true
  }

  assert {
    condition     = contains([for s in helm_release.cert_manager.set : "${s.name}=${s.value}"], "crds.enabled=true")
    error_message = "crds.enabled must stay set: without it cert-manager comes up healthy with no CRDs"
  }
}
