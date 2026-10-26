# Copyright The DeviceChain Authors
# SPDX-License-Identifier: Apache-2.0

# The shared `dc_service` login exists only on an instance upgraded from a release that had one, and
# only until the upgrade that retires it. A fresh install, and a retired instance, pass no
# service_password_bcrypt, and then the broker has no such user at all: not in APP, not in the
# callout's auth_users, and no permissions entry is needed for it.
#
# Runs against mocked providers, so no cluster is needed. Run it with
# hack/check-tofu-module-tests.sh, which tests a COPY of the module.

mock_provider "helm" {}
mock_provider "kubernetes" {}

variables {
  namespace        = "dci-test"
  server_image_tag = "v0.0.0-test"
  enable_tls       = false
  enable_auth      = true
  ha               = false

  callout_issuer_public = "ABCDEFGHIJKLMNOPQRSTUVWXYZ"

  # The generated document carries no dc_service block either (dcctl broker permissions).
  service_users = <<-EOT
    {"APP": {
      "dc-device-management-g1": {"publish": {"allow": ["dm.>"]}, "subscribe": {"allow": ["_INBOX_device-management.>"]}, "allow_responses": true},
      "dc-broker-admin-g1": {"publish": {"deny": ["*.*.devices.*.events"]}}
    }}
  EOT

  login_password_bcrypts = {
    "dc-device-management-g1" = "$2a$10$devicemanagementdevicemanUABCDEFGHIJKLMNOPQRSTUVWXYZ0"
    "dc-broker-admin-g1"      = "$2a$10$brokeradminbrokeradminbUABCDEFGHIJKLMNOPQRSTUVWXYZ0"
  }
}

run "a_retired_instance_has_no_shared_login" {
  command = plan

  assert {
    condition = [for u in yamldecode(helm_release.nats.values[0]).config.merge.accounts.APP.users : u.user] == [
      "dc-broker-admin-g1", "dc-device-management-g1",
    ]
    error_message = "with no service_password_bcrypt APP must hold the per-area logins and no dc_service"
  }

  assert {
    condition = yamldecode(helm_release.nats.values[0]).config.merge.authorization.auth_callout.auth_users == [
      "dc_sys", "dc-broker-admin-g1", "dc-device-management-g1",
    ]
    error_message = "dc_service must not be exempt from the device callout once it is retired"
  }

  assert {
    condition     = !strcontains(helm_release.nats.values[0], "dc_service")
    error_message = "nothing in the rendered broker configuration may name the shared login"
  }
}

# The negative control: the same module with the hash supplied (the upgrade that introduces the
# per-area logins) renders the login, so the run above can fail.
run "the_transitional_phase_still_renders_it" {
  command = plan

  variables {
    service_password_bcrypt = "$2a$10$abcdefghijklmnopqrstuuABCDEFGHIJKLMNOPQRSTUVWXYZ01234"
    service_users           = <<-EOT
      {"APP": {
        "dc_service": {"publish": {"deny": ["*.*.devices.*.events"]}},
        "dc-device-management-g1": {"publish": {"allow": ["dm.>"]}, "subscribe": {"allow": ["_INBOX_device-management.>"]}, "allow_responses": true},
        "dc-broker-admin-g1": {"publish": {"deny": ["*.*.devices.*.events"]}}
      }}
    EOT
  }

  assert {
    condition     = contains([for u in yamldecode(helm_release.nats.values[0]).config.merge.accounts.APP.users : u.user], "dc_service")
    error_message = "the transitional phase must render the shared login"
  }

  assert {
    condition     = contains(yamldecode(helm_release.nats.values[0]).config.merge.authorization.auth_callout.auth_users, "dc_service")
    error_message = "the transitional phase must exempt the shared login from the device callout"
  }
}

# Fail closed: authentication with no login for any service to present would refuse every one.
run "no_shared_login_and_no_per_area_logins_is_refused" {
  command = plan

  variables {
    login_password_bcrypts = {}
  }

  expect_failures = [helm_release.nats]
}
