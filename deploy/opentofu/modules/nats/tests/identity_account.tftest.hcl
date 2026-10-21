# Copyright The DeviceChain Authors
# SPDX-License-Identifier: Apache-2.0

# user-management's sign-in buckets live in an account of their own.
#
# Refresh tokens, OAuth and single sign-on codes are bearer secrets. A login in the shared APP
# account that can create JetStream streams can source, mirror or subject-capture a bucket into a
# stream it reads, so a deny on the shared service login closes nothing. A stream cannot cross an
# account boundary: the IDENTITY account holds the buckets, has one login, and shares nothing with
# APP (no import, no export).
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

run "the_identity_account_has_one_login_and_nothing_shared" {
  command = plan

  variables {
    identity_password_bcrypt = "$2a$10$identityidentityidentityuABCDEFGHIJKLMNOPQRSTUVWXYZ0"
  }

  assert {
    condition     = yamldecode(helm_release.nats.values[0]).config.merge.accounts.IDENTITY.users[0].user == "dc_identity"
    error_message = "the IDENTITY account's login must be dc_identity"
  }

  assert {
    condition     = yamldecode(helm_release.nats.values[0]).config.merge.accounts.IDENTITY.users[0].password == "$2a$10$identityidentityidentityuABCDEFGHIJKLMNOPQRSTUVWXYZ0"
    error_message = "the broker must carry the bcrypt hash, never a plaintext password"
  }

  assert {
    condition     = length(yamldecode(helm_release.nats.values[0]).config.merge.accounts.IDENTITY.users) == 1
    error_message = "the IDENTITY account must have exactly one login"
  }

  assert {
    condition     = yamldecode(helm_release.nats.values[0]).config.merge.accounts.IDENTITY.jetstream == "enabled"
    error_message = "the IDENTITY account needs JetStream: the sign-in buckets are KV"
  }

  # Nothing is shared: an import or export is how one account would reach the other's streams.
  assert {
    condition = (
      !can(yamldecode(helm_release.nats.values[0]).config.merge.accounts.IDENTITY.imports) &&
      !can(yamldecode(helm_release.nats.values[0]).config.merge.accounts.IDENTITY.exports) &&
      !can(yamldecode(helm_release.nats.values[0]).config.merge.accounts.APP.imports) &&
      !can(yamldecode(helm_release.nats.values[0]).config.merge.accounts.APP.exports)
    )
    error_message = "neither account may import or export anything: a shared stream is the route this account exists to remove"
  }

  # The shared service login stays in APP and is not given the new account.
  assert {
    condition     = [for u in yamldecode(helm_release.nats.values[0]).config.merge.accounts.APP.users : u.user] == ["dc_service"]
    error_message = "APP must keep exactly the shared service login; dc_identity must not be in it"
  }

  # A login absent from auth_users is handed to the device callout, which refuses it.
  assert {
    condition     = yamldecode(helm_release.nats.values[0]).config.merge.authorization.auth_callout.auth_users == ["dc_service", "dc_sys", "dc_identity"]
    error_message = "dc_identity must be exempt from the device callout like the other static logins"
  }
}

# The negative control, and the reason the run above can fail: with no hash the broker renders
# exactly what every earlier release did, with NO IDENTITY account.
run "no_identity_account_until_minted" {
  command = plan

  assert {
    condition     = !can(yamldecode(helm_release.nats.values[0]).config.merge.accounts.IDENTITY)
    error_message = "with identity_password_bcrypt empty there must be no IDENTITY account"
  }

  assert {
    condition     = yamldecode(helm_release.nats.values[0]).config.merge.authorization.auth_callout.auth_users == ["dc_service", "dc_sys"]
    error_message = "the exempt logins must be unchanged when there is no identity login"
  }
}

run "an_identity_login_without_broker_auth_is_refused" {
  command = plan

  variables {
    enable_auth              = false
    identity_password_bcrypt = "$2a$10$identityidentityidentityuABCDEFGHIJKLMNOPQRSTUVWXYZ0"
  }

  expect_failures = [helm_release.nats]
}
