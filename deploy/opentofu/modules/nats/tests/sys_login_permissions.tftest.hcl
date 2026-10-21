# Copyright The DeviceChain Authors
# SPDX-License-Identifier: Apache-2.0

# The system-account login (dc_sys) is held to an allow list, and an instance that has not
# minted it keeps a SYS account with no users.
#
# A SYS user with no permissions block can put a server into lame-duck mode, make it reload,
# and read every account's subscriptions and JetStream state. event-sources needs the cluster
# ping, one server's connection list and kick, the connect/disconnect advisories and its reply
# inboxes. A Go test (natsauth.TestSysPermissionsMatchTheInfrastructureModule) holds the lists
# in step with natsauth.SysPublishAllow / SysSubscribeAllow; this one reads the values document
# Helm is handed, so a permissions block under the wrong key cannot pass.
#
# Runs against mocked providers, so no cluster is needed. Run it with
# hack/check-tofu-module-tests.sh, which tests a COPY of the module.

mock_provider "helm" {}
mock_provider "kubernetes" {}

variables {
  namespace               = "dci-test"
  enable_tls              = false
  enable_auth             = true
  callout_issuer_public   = "AAUTHCALLOUTISSUERPUBLICKEYPLACEHOLDER"
  service_password_bcrypt = "$2a$11$placeholderplaceholderplaceholderplaceholderplace"
}

run "sys_login_is_held_to_an_allow_list" {
  command = plan

  variables {
    sys_password_bcrypt = "$2a$11$sysplaceholdersysplaceholdersysplaceholdersysplac"
  }

  assert {
    condition = try(yamldecode(helm_release.nats.values[0]).config.merge.accounts.SYS.users[0].permissions.publish.allow, null) == [
      "$SYS.REQ.SERVER.PING", "$SYS.REQ.SERVER.*.CONNZ", "$SYS.REQ.SERVER.*.KICK",
    ]
    error_message = "dc_sys must publish only the cluster ping, one server's CONNZ and one server's KICK"
  }

  assert {
    condition = try(yamldecode(helm_release.nats.values[0]).config.merge.accounts.SYS.users[0].permissions.subscribe.allow, null) == [
      "$SYS.ACCOUNT.APP.CONNECT", "$SYS.ACCOUNT.APP.DISCONNECT", "_INBOX.>",
    ]
    error_message = "dc_sys must subscribe only to the APP connect/disconnect advisories and its reply inboxes"
  }

  assert {
    condition     = try(yamldecode(helm_release.nats.values[0]).config.merge.accounts.SYS.users[0].user, null) == "dc_sys"
    error_message = "the SYS user must be dc_sys"
  }
}

run "no_sys_login_until_minted" {
  command = plan

  assert {
    condition     = length(try(yamldecode(helm_release.nats.values[0]).config.merge.accounts.SYS.users, ["missing"])) == 0
    error_message = "an instance with no dc_sys hash must keep a SYS account with no users"
  }
}
