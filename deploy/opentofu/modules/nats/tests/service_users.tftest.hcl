# Copyright The DeviceChain Authors
# SPDX-License-Identifier: Apache-2.0

# The broker's static logins carry the permissions dcctl generates, and the module renders them
# VERBATIM.
#
# The module holds no subject. dcctl produces one JSON document (var.service_users, from the same
# stream and bucket declarations the services are held to) and each block in it is rendered into its
# user unchanged. This suite tests the renderer, not a second list: it hands the module blocks that
# look nothing like the real ones and requires them back byte for byte, in order, and it requires that
# a login with no block is refused rather than rendered (nats-server reads a user with no permissions
# block as unrestricted).
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

  callout_issuer_public   = "ABCDEFGHIJKLMNOPQRSTUVWXYZ"
  service_password_bcrypt = "$2a$10$abcdefghijklmnopqrstuuABCDEFGHIJKLMNOPQRSTUVWXYZ01234"
}

run "every_block_is_rendered_verbatim_and_in_order" {
  command = plan

  variables {
    sys_password_bcrypt = "$2a$11$sysplaceholdersysplaceholdersysplaceholdersysplac"
    login_password_bcrypts = {
      "dc-event-management-g1" = "$2a$10$eventmanagementeventmanaUABCDEFGHIJKLMNOPQRSTUVWXYZ0"
      "dc-broker-admin-g1"     = "$2a$10$brokeradminbrokeradminbUABCDEFGHIJKLMNOPQRSTUVWXYZ0"
    }
    # Deliberately not alphabetical, and with characters a renderer might mangle.
    service_users = <<-EOT
      {"APP": {
        "dc_service": {"publish": {"deny": ["zz.*.devices.*.events", "$MQTT.msgs.>"]}},
        "dc-event-management-g1": {"publish": {"allow": ["z.>", "$JS.API.STREAM.INFO.x", "a.*.b"], "deny": ["q"]}, "subscribe": {"allow": ["_INBOX_event-management.>", "m.n"]}, "allow_responses": true},
        "dc-broker-admin-g1": {}
      },
      "SYS": {
        "dc_sys": {"publish": {"allow": ["$SYS.REQ.SERVER.PING", "$SYS.REQ.SERVER.*.KICK"]}, "subscribe": {"allow": ["$SYS.ACCOUNT.APP.CONNECT", "_INBOX.>"]}}
      }}
      EOT
  }

  assert {
    condition = alltrue([
      for u in yamldecode(helm_release.nats.values[0]).config.merge.accounts.APP.users :
      jsonencode(u.permissions) == jsonencode(jsondecode(var.service_users).APP[u.user])
    ])
    error_message = "an APP user's permissions must be exactly the block supplied for it: same keys, same lists, same order"
  }

  assert {
    condition = (
      jsonencode(yamldecode(helm_release.nats.values[0]).config.merge.accounts.SYS.users[0].permissions) ==
      jsonencode(jsondecode(var.service_users).SYS.dc_sys)
    )
    error_message = "the SYS user's permissions must be exactly the block supplied for it"
  }

  assert {
    condition     = length(yamldecode(helm_release.nats.values[0]).config.merge.accounts.APP.users) == 3
    error_message = "one APP user per static login: the shared one and the two supplied, and none invented"
  }

  # The module adds nothing of its own: a block supplied empty stays empty, with no deny of the
  # module's making.
  assert {
    condition     = jsonencode([for u in yamldecode(helm_release.nats.values[0]).config.merge.accounts.APP.users : u.permissions if u.user == "dc-broker-admin-g1"][0]) == "{}"
    error_message = "the module must not add a permission the generated document does not carry"
  }
}

run "no_sys_login_until_minted" {
  command = plan

  variables {
    service_users = "{\"APP\":{\"dc_service\":{}}}"
  }

  assert {
    condition     = length(try(yamldecode(helm_release.nats.values[0]).config.merge.accounts.SYS.users, ["missing"])) == 0
    error_message = "an instance with no dc_sys hash must keep a SYS account with no users"
  }
}

# The control for the refusals below: a complete document plans.
run "a_complete_document_plans" {
  command = plan

  variables {
    sys_password_bcrypt = "$2a$11$sysplaceholdersysplaceholdersysplaceholdersysplac"
    service_users       = "{\"APP\":{\"dc_service\":{}},\"SYS\":{\"dc_sys\":{}}}"
  }
}

run "the_shared_login_without_a_block_is_refused" {
  command = plan

  variables {
    service_users = "{\"APP\":{}}"
  }

  expect_failures = [helm_release.nats]
}

run "the_system_login_without_a_block_is_refused" {
  command = plan

  variables {
    sys_password_bcrypt = "$2a$11$sysplaceholdersysplaceholdersysplaceholdersysplac"
    service_users       = "{\"APP\":{\"dc_service\":{}}}"
  }

  expect_failures = [helm_release.nats]
}

run "no_document_at_all_is_refused_when_auth_is_on" {
  command = plan

  variables {
    service_users = ""
  }

  expect_failures = [helm_release.nats]
}

run "a_document_that_is_not_json_is_refused" {
  command = plan

  variables {
    service_users = "not json"
  }

  expect_failures = [var.service_users]
}
