# Copyright The DeviceChain Authors
# SPDX-License-Identifier: Apache-2.0

# Every area that uses the broker has a login of its own, and so does the broker administrator.
#
# Each lands in the APP account with the SAME permissions as the shared service login, so nothing an
# area can do changes; what changes is that the credential an area presents is one only that area
# holds. The shared login stays beside them (it is retired in a later step).
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
}

run "each_login_is_in_app_with_the_service_logins_permissions" {
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
      u.permissions == yamldecode(helm_release.nats.values[0]).config.merge.accounts.APP.users[0].permissions
    ])
    error_message = "every login must carry exactly the shared login's permissions: narrowing them is a later change, and a login with MORE would widen the grant"
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
