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

# Plans do not persist state, so the following apply still models an existing
# installation adopting the new variable with its backwards-compatible default.
run "a_new_broker_can_select_ssd" {
  command = plan
  variables {
    jetstream_storage_class = "premium-rwo"
  }
  assert {
    condition     = yamldecode(helm_release.nats.values[0]).config.jetstream.fileStore.pvc.storageClassName == "premium-rwo"
    error_message = "the NATS chart must receive config.jetstream.fileStore.pvc.storageClassName"
  }
}

run "unset_keeps_the_cluster_default" {
  command = apply
  assert {
    condition     = !contains(keys(yamldecode(helm_release.nats.values[0]).config.jetstream.fileStore.pvc), "storageClassName")
    error_message = "unset must omit storageClassName entirely, preserving existing claim templates"
  }
}

run "a_new_default_cannot_change_existing_claims" {
  command = plan
  plan_options {
    refresh = false
  }
  variables {
    jetstream_storage_class = "premium-rwo"
    jetstream_storage       = "20Gi"
  }
  assert {
    condition     = !contains(keys(yamldecode(helm_release.nats.values[0]).config.jetstream.fileStore.pvc), "storageClassName")
    error_message = "an upgrade must not add a class to an existing broker's immutable claim template"
  }
  assert {
    condition     = yamldecode(helm_release.nats.values[0]).config.jetstream.fileStore.pvc.size == "20Gi"
    error_message = "the class guard must not freeze unrelated chart values"
  }
}
