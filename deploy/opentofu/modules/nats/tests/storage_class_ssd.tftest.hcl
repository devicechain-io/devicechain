# Copyright The DeviceChain Authors
# SPDX-License-Identifier: Apache-2.0

mock_provider "helm" {}
mock_provider "kubernetes" {}

variables {
  namespace        = "dci-test"
  server_image_tag = "v0.26.0"
  enable_tls       = false
  enable_auth      = false
}

run "create_ssd_broker" {
  command = apply
  variables {
    jetstream_storage_class = "premium-rwo"
  }
}

# dcctl upgrade leaves the new-install default unset, including for brokers
# created with SSD. The recorded choice must still reach Helm unchanged.
run "upgrade_preserves_ssd_without_repeating_the_default" {
  command = plan
  plan_options {
    refresh = false
  }
  assert {
    condition     = yamldecode(helm_release.nats.values[0]).config.jetstream.fileStore.pvc.storageClassName == "premium-rwo"
    error_message = "an upgrade must preserve the SSD class selected at creation"
  }
}
