# Copyright The DeviceChain Authors
# SPDX-License-Identifier: Apache-2.0

# The broker encrypts its auth-callout requests when, and only when, it is given the responder's
# curve key.
#
# A device's CONNECT username and password ride inside the authorization request. Unencrypted,
# that request is readable by every login in the APP account that can subscribe to
# $SYS.REQ.USER.AUTH or capture it into a stream. auth_callout.xkey seals it to the responder.
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
  service_users           = "{\"APP\":{\"dc_service\":{\"publish\":{\"deny\":[\"*.*.devices.*.events\"]}}}}"
}

run "xkey_is_rendered_into_the_callout_when_given" {
  command = plan

  variables {
    callout_xkey_public = "XABCDEFGHIJKLMNOPQRSTUVWXYZ234567ABCDEFGHIJKLMNOPQRSTUVW"
  }

  assert {
    condition     = yamldecode(helm_release.nats.values[0]).config.merge.authorization.auth_callout.xkey == "XABCDEFGHIJKLMNOPQRSTUVWXYZ234567ABCDEFGHIJKLMNOPQRSTUVW"
    error_message = "auth_callout.xkey must carry the responder's curve public key so the broker seals each request to it"
  }

  assert {
    condition     = yamldecode(helm_release.nats.values[0]).config.merge.authorization.auth_callout.issuer == "ABCDEFGHIJKLMNOPQRSTUVWXYZ"
    error_message = "adding the xkey must not disturb the issuer"
  }
}

# The negative control, and the reason the positive run above can fail: with no key the block
# is byte-for-byte what every earlier release rendered, with NO xkey entry at all (an empty
# `xkey` would be a different config, and a broker that rejects it would refuse every device).
run "no_xkey_leaves_the_callout_as_it_was" {
  command = plan

  assert {
    condition     = !can(yamldecode(helm_release.nats.values[0]).config.merge.authorization.auth_callout.xkey)
    error_message = "with callout_xkey_public empty, auth_callout must carry no xkey key"
  }

  assert {
    condition     = yamldecode(helm_release.nats.values[0]).config.merge.authorization.auth_callout.auth_users == ["dc_service", "dc_sys"]
    error_message = "the exempt logins must be unchanged"
  }
}

run "a_malformed_xkey_is_refused" {
  command = plan

  variables {
    callout_xkey_public = "SXAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"
  }

  expect_failures = [var.callout_xkey_public]
}

run "an_xkey_without_the_callout_is_refused" {
  command = plan

  variables {
    enable_auth         = false
    callout_xkey_public = "XABCDEFGHIJKLMNOPQRSTUVWXYZ234567ABCDEFGHIJKLMNOPQRSTUVW"
  }

  expect_failures = [helm_release.nats]
}
