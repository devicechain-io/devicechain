# Copyright The DeviceChain Authors
# SPDX-License-Identifier: Apache-2.0

# A static platform login must not authenticate over MQTT, because the MQTT port can be published to
# the internet (dcctl --mqtt-exposure). With mqtt_capable_users set, every static login (the shared
# one, the per-area ones, the identity login and the system account's) is held to
# allowed_connection_types: STANDARD, plus MQTT for the ones named. Devices do not use these logins:
# they authenticate through the auth callout, and the callout's own users are not in this block.
#
# Unset (null) leaves every login unrestricted, which is what every earlier release ran.
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

  callout_issuer_public    = "ABCDEFGHIJKLMNOPQRSTUVWXYZ"
  service_password_bcrypt  = "$2a$10$abcdefghijklmnopqrstuuABCDEFGHIJKLMNOPQRSTUVWXYZ01234"
  sys_password_bcrypt      = "$2a$10$syssyssyssyssyssyssyuABCDEFGHIJKLMNOPQRSTUVWXYZ0123"
  identity_password_bcrypt = "$2a$10$identityidentityidentiuABCDEFGHIJKLMNOPQRSTUVWXYZ0"

  login_password_bcrypts = {
    "dc-event-sources-g1"     = "$2a$10$eventsourceseventsourcUABCDEFGHIJKLMNOPQRSTUVWXYZ0"
    "dc-device-management-g1" = "$2a$10$devicemanagementdevicemanUABCDEFGHIJKLMNOPQRSTUVWXYZ0"
  }

  service_users = <<-EOT
    {"APP": {
      "dc_service": {"publish": {"deny": ["x"]}},
      "dc-event-sources-g1": {"publish": {"allow": ["es.>"]}},
      "dc-device-management-g1": {"publish": {"allow": ["dm.>"]}}
    }, "SYS": {"dc_sys": {"publish": {"allow": ["$SYS.REQ.>"]}}}}
  EOT
}

run "unset_leaves_every_login_unrestricted" {
  command = plan

  # The key must be ABSENT, not null: the chart renders a null as JSON null and `nats-server -t`
  # refuses it (invalid connection types ["NULL"]). Negative control: restoring the null makes
  # `can()` succeed for every user and fails all three asserts.
  assert {
    condition = alltrue([
      for u in yamldecode(helm_release.nats.values[0]).config.merge.accounts.APP.users :
      !can(u.allowed_connection_types)
    ])
    error_message = "with mqtt_capable_users unset no login may carry allowed_connection_types, not even null: that is what earlier releases ran, and nats-server refuses a null"
  }

  assert {
    condition     = !can(yamldecode(helm_release.nats.values[0]).config.merge.accounts.SYS.users[0].allowed_connection_types)
    error_message = "with mqtt_capable_users unset the system login may not carry allowed_connection_types"
  }

  assert {
    condition     = !can(yamldecode(helm_release.nats.values[0]).config.merge.accounts.IDENTITY.users[0].allowed_connection_types)
    error_message = "with mqtt_capable_users unset the identity login may not carry allowed_connection_types"
  }
}

run "every_static_login_is_standard_only_except_the_mqtt_ones" {
  command = plan

  variables {
    mqtt_capable_users = ["dc-event-sources-g1"]
  }

  assert {
    condition = alltrue([
      for u in yamldecode(helm_release.nats.values[0]).config.merge.accounts.APP.users :
      jsonencode(u.allowed_connection_types) == (u.user == "dc-event-sources-g1" ? jsonencode(["STANDARD", "MQTT"]) : jsonencode(["STANDARD"]))
    ])
    error_message = "APP logins other than the named MQTT one must be STANDARD only"
  }

  assert {
    condition     = yamldecode(helm_release.nats.values[0]).config.merge.accounts.SYS.users[0].allowed_connection_types == ["STANDARD"]
    error_message = "the system account's login must be STANDARD only"
  }

  assert {
    condition     = yamldecode(helm_release.nats.values[0]).config.merge.accounts.IDENTITY.users[0].allowed_connection_types == ["STANDARD"]
    error_message = "the identity login must be STANDARD only"
  }

  # Devices are not in this block: they never match a static user and reach the callout instead.
  assert {
    condition     = !contains(yamldecode(helm_release.nats.values[0]).config.merge.authorization.auth_callout.auth_users, "device")
    error_message = "the callout's exempt list must stay the static logins only"
  }
}

run "an_empty_list_restricts_everything_to_standard" {
  command = plan

  variables {
    mqtt_capable_users = []
  }

  assert {
    condition = alltrue([
      for u in yamldecode(helm_release.nats.values[0]).config.merge.accounts.APP.users :
      u.allowed_connection_types == ["STANDARD"]
    ])
    error_message = "an empty list means no login may speak MQTT"
  }
}

# --- the connection ceiling ---------------------------------------------------------------

run "max_connections_is_absent_unless_set" {
  command = plan

  assert {
    condition     = !can(yamldecode(helm_release.nats.values[0]).config.merge.max_connections)
    error_message = "an unset ceiling must leave the broker config as it always was"
  }
}

run "max_connections_reaches_the_broker_config" {
  command = plan

  variables {
    max_connections = 20000
  }

  assert {
    condition     = yamldecode(helm_release.nats.values[0]).config.merge.max_connections == 20000
    error_message = "max_connections must be rendered as nats-server's max_connections"
  }
}

run "a_negative_ceiling_is_refused" {
  command = plan

  variables {
    max_connections = -1
  }

  expect_failures = [var.max_connections]
}
