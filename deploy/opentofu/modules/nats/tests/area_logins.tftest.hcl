# Copyright The DeviceChain Authors
# SPDX-License-Identifier: Apache-2.0

# Every area that uses the broker has a login of its own, and so does the broker administrator.
#
# Each lands in the APP account with the permissions block var.service_users carries for it (the
# generated, per-area one), and is exempt from the device callout. The shared login stays beside them
# only in the transitional phase (shared_login_retired.tftest.hcl covers its retirement).
#
# Runs against mocked providers, so no cluster is needed. Run it with
# hack/check-tofu-module-tests.sh, which tests a COPY of the module.

mock_provider "helm" {}
mock_provider "kubernetes" {}

variables {
  namespace   = "dci-test"
  enable_tls  = false
  enable_auth = true
  ha          = false

  callout_issuer_public   = "ABCDEFGHIJKLMNOPQRSTUVWXYZ"
  service_password_bcrypt = "$2a$10$abcdefghijklmnopqrstuuABCDEFGHIJKLMNOPQRSTUVWXYZ01234"

  # A distinct block per login, so a render that applied one login's block to another is visible.
  service_users = <<-EOT
    {"APP": {
      "dc_service": {"publish": {"deny": ["*.*.devices.*.events"]}},
      "dc-device-management-g1": {"publish": {"allow": ["dm.>"]}, "subscribe": {"allow": ["_INBOX_device-management.>"]}, "allow_responses": true},
      "dc-user-management-g1": {"publish": {"allow": ["um.>"]}, "subscribe": {"allow": ["_INBOX_user-management.>"]}, "allow_responses": true},
      "dc-broker-admin-g1": {"publish": {"deny": ["*.*.devices.*.events"]}}
    }}
  EOT
}

run "each_login_is_in_app_with_its_own_permissions" {
  command = plan

  variables {
    login_password_bcrypts = {
      "dc-device-management-g1" = "$2a$10$devicemanagementdevicemanUABCDEFGHIJKLMNOPQRSTUVWXYZ0"
      "dc-user-management-g1"   = "$2a$10$usermanagementusermanageUABCDEFGHIJKLMNOPQRSTUVWXYZ0"
      "dc-broker-admin-g1"      = "$2a$10$brokeradminbrokeradminbUABCDEFGHIJKLMNOPQRSTUVWXYZ0"
    }
  }

  assert {
    condition = [for u in yamldecode(helm_release.nats.values[0]).config.merge.accounts.APP.users : u.user] == [
      "dc_service", "dc-broker-admin-g1", "dc-device-management-g1", "dc-user-management-g1",
    ]
    error_message = "APP must hold the shared login and one user per supplied login"
  }

  assert {
    condition = alltrue([
      for u in yamldecode(helm_release.nats.values[0]).config.merge.accounts.APP.users :
      jsonencode(u.permissions) == jsonencode(jsondecode(var.service_users).APP[u.user])
    ])
    error_message = "every login must carry exactly the block generated for it, and no other login's"
  }

  assert {
    condition     = [for u in yamldecode(helm_release.nats.values[0]).config.merge.accounts.APP.users : u.permissions if u.user == "dc-user-management-g1"][0].publish.allow == ["um.>"]
    error_message = "a login must not be given another login's grant"
  }

  assert {
    condition = [for u in yamldecode(helm_release.nats.values[0]).config.merge.accounts.APP.users : u.password if u.user == "dc-user-management-g1"] == [
      "$2a$10$usermanagementusermanageUABCDEFGHIJKLMNOPQRSTUVWXYZ0"
    ]
    error_message = "the broker must carry the bcrypt hash for each user, never a plaintext password"
  }

  # A login absent from auth_users is handed to the device callout, which refuses it.
  assert {
    condition = yamldecode(helm_release.nats.values[0]).config.merge.authorization.auth_callout.auth_users == [
      "dc_service", "dc_sys", "dc-broker-admin-g1", "dc-device-management-g1", "dc-user-management-g1",
    ]
    error_message = "every per-area login must be exempt from the device callout like the other static logins"
  }
}

# The negative control, and the reason the run above can fail: with no hashes the broker renders
# exactly what every earlier release did.
run "no_logins_until_minted" {
  command = plan

  assert {
    condition     = [for u in yamldecode(helm_release.nats.values[0]).config.merge.accounts.APP.users : u.user] == ["dc_service"]
    error_message = "with login_password_bcrypts empty APP must hold only the shared login"
  }

  assert {
    condition     = yamldecode(helm_release.nats.values[0]).config.merge.authorization.auth_callout.auth_users == ["dc_service", "dc_sys"]
    error_message = "the exempt logins must be unchanged when there are no per-area logins"
  }
}

# A login with no entry in service_users would render with no permissions block, which nats-server
# reads as unrestricted. It is refused instead.
run "a_login_with_no_generated_block_is_refused" {
  command = plan

  variables {
    login_password_bcrypts = { "dc-event-management-g1" = "$2a$10$eventmanagementeventmanaUABCDEFGHIJKLMNOPQRSTUVWXYZ0" }
  }

  expect_failures = [helm_release.nats]
}

run "logins_without_broker_auth_are_refused" {
  command = plan

  variables {
    enable_auth            = false
    login_password_bcrypts = { "dc-device-management-g1" = "$2a$10$devicemanagementdevicemanUABCDEFGHIJKLMNOPQRSTUVWXYZ0" }
  }

  expect_failures = [helm_release.nats]
}

run "a_login_must_be_keyed_by_a_username_and_hold_a_bcrypt_hash" {
  command = plan

  variables {
    login_password_bcrypts = { "device-management" = "$2a$10$devicemanagementdevicemanUABCDEFGHIJKLMNOPQRSTUVWXYZ0" }
  }

  expect_failures = [var.login_password_bcrypts]
}
