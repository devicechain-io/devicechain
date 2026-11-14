# Copyright The DeviceChain Authors
# SPDX-License-Identifier: Apache-2.0

mock_provider "helm" {}
mock_provider "kubernetes" {}

variables {
  namespace        = "dci-test"
  server_image_tag = "v0.0.0-test"
  enable_tls       = false
  enable_auth      = false
  cluster_replicas = 3
}

run "off_is_byte_identical" {
  command = plan
  assert {
    condition     = helm_release.nats.values[0] == yamlencode(local.chart_values)
    error_message = "The absent shardGroups path must preserve all chart values bytes."
  }
}

run "on_uses_per_server_env_tag" {
  command = plan
  variables { shard_groups = 3 }
  assert {
    condition     = yamldecode(helm_release.nats.values[0]).config.merge.server_tags == ["<< $SERVER_NAME >>"]
    error_message = "Every server must advertise its own SERVER_NAME as its placement tag."
  }
}

run "insufficient_servers_refused" {
  command = plan
  variables { shard_groups = 4 }
  expect_failures = [helm_release.nats]
}

run "invalid_count_refused" {
  command = plan
  variables { shard_groups = 1 }
  expect_failures = [var.shard_groups]
}
