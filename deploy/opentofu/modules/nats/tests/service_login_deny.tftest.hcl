# Copyright The DeviceChain Authors
# SPDX-License-Identifier: Apache-2.0

# The shared service login is denied the device-events namespace, and nothing else.
#
# Only a device writes "{instance}.{tenant}.devices.{device}.events" (through the grant the
# callout mints); a subject there is what the platform takes as a device's own identity. The
# service login has no allow list, so it is held to a deny on that namespace. The
# command-responses / device-reports / device-commands subjects the services publish to must
# stay outside the deny, or the store forwarder and the downlinks stop.
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

run "service_login_is_denied_device_events" {
  command = plan

  assert {
    condition = yamldecode(helm_release.nats.values[0]).config.merge.accounts.APP.users[0].permissions.publish.deny == [
      "*.*.devices.*.events",
      "$MQTT.msgs.*.*.devices.*.events",
    ]
    error_message = "the service login must carry a publish deny on exactly the device-events namespace (and its $MQTT.msgs form)"
  }

  assert {
    condition     = !can(yamldecode(helm_release.nats.values[0]).config.merge.accounts.APP.users[0].permissions.publish.allow)
    error_message = "the service login must stay allow-all apart from the deny: an allow list would have to track every platform subject"
  }
}
