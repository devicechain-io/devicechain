# Copyright The DeviceChain Authors
# SPDX-License-Identifier: Apache-2.0

# Route compression reaches the chart's cluster block, and only when asked for.
#
# The nats chart lays config.cluster.merge over the cluster{} stanza it generates,
# and it has no values schema, so a compression key under the wrong parent is
# accepted and does nothing. These read the values document Helm is handed.
#
# Left unset, a clustered broker gets s2_fast and a single server gets off. An
# explicit "off" wins over that default, and must leave the document as it was
# before the variable existed: no merge key at all on the cluster block.
#
# Runs against mocked providers, so no cluster is needed. Run it with
# hack/check-tofu-module-tests.sh, which tests a COPY of the module.

mock_provider "helm" {}
mock_provider "kubernetes" {}

variables {
  namespace   = "dci-test"
  enable_tls  = false
  enable_auth = false
  ha          = true
}

run "s2_fast_by_default_on_a_cluster" {
  command = plan

  assert {
    condition     = yamldecode(helm_release.nats.values[0]).config.cluster.merge == { compression = "s2_fast" }
    error_message = "a clustered broker with route_compression unset must render config.cluster.merge.compression = s2_fast and nothing else"
  }

  assert {
    condition     = output.ha_topology.route_compression == "s2_fast"
    error_message = "ha_topology.route_compression must report s2_fast by default on a cluster"
  }
}

# An explicit null is the same as leaving it out.
run "explicit_null_is_the_default" {
  command = plan

  variables {
    route_compression = null
  }

  assert {
    condition     = output.ha_topology.route_compression == "s2_fast"
    error_message = "route_compression = null must take the clustered default, s2_fast"
  }
}

# An explicit off wins over the clustered default, and renders nothing.
run "explicit_off_renders_nothing" {
  command = plan

  variables {
    route_compression = "off"
  }

  assert {
    condition     = !contains(keys(yamldecode(helm_release.nats.values[0]).config.cluster), "merge")
    error_message = "with route_compression = off the cluster block must carry no merge key at all"
  }

  assert {
    condition     = output.ha_topology.route_compression == "off"
    error_message = "ha_topology.route_compression must report off when off is asked for"
  }
}

run "s2_fast_reaches_the_cluster_block" {
  command = plan

  variables {
    route_compression = "s2_fast"
  }

  assert {
    condition     = yamldecode(helm_release.nats.values[0]).config.cluster.merge == { compression = "s2_fast" }
    error_message = "route_compression=s2_fast must render config.cluster.merge.compression = s2_fast and nothing else"
  }

  assert {
    condition     = output.ha_topology.route_compression == "s2_fast"
    error_message = "ha_topology.route_compression must read s2_fast back out of the values"
  }

  # The merge is laid over the cluster map; it must not displace what is already in it.
  assert {
    condition     = yamldecode(helm_release.nats.values[0]).config.cluster.replicas == 3 && yamldecode(helm_release.nats.values[0]).config.cluster.enabled == true
    error_message = "route compression must leave the cluster's size and enablement alone"
  }
}

# Route TLS and its verify flag stay in place alongside compression.
run "compression_keeps_route_tls" {
  command = plan

  variables {
    enable_tls        = true
    ca_cert_pem       = "-----CA-----"
    route_compression = "s2_fast"
  }

  assert {
    condition     = output.ha_topology.route_tls_verified && yamldecode(helm_release.nats.values[0]).config.cluster.tls.merge.verify == true
    error_message = "route TLS must stay mutually verified with compression on"
  }

  assert {
    condition     = yamldecode(helm_release.nats.values[0]).config.cluster.merge.compression == "s2_fast"
    error_message = "compression must reach the values with route TLS on"
  }
}

# The auth toggle re-encodes the values at the string level; compression must survive it.
run "compression_survives_the_auth_merge" {
  command = plan

  variables {
    enable_auth             = true
    callout_issuer_public   = "AISSUER"
    service_password_bcrypt = "$2a$10$x"
    service_users           = "{\"APP\":{\"dc_service\":{\"publish\":{\"deny\":[\"*.*.devices.*.events\"]}}}}"
    route_compression       = "s2_better"
  }

  assert {
    condition     = yamldecode(helm_release.nats.values[0]).config.cluster.merge == { compression = "s2_better" }
    error_message = "route_compression must reach the values when broker auth is on"
  }
}

run "s2_best" {
  command = plan

  variables {
    route_compression = "s2_best"
  }

  assert {
    condition     = yamldecode(helm_release.nats.values[0]).config.cluster.merge.compression == "s2_best"
    error_message = "route_compression=s2_best must reach the values"
  }
}

run "s2_auto" {
  command = plan

  variables {
    route_compression = "s2_auto"
  }

  assert {
    condition     = yamldecode(helm_release.nats.values[0]).config.cluster.merge.compression == "s2_auto"
    error_message = "route_compression=s2_auto must reach the values"
  }
}

# Unset on a single server is off, and is not refused: the clustered default must
# not trip the guard below on the install that has no routes.
run "unset_on_a_single_server_is_off" {
  command = plan

  variables {
    ha = false
  }

  assert {
    condition     = output.ha_topology.route_compression == "off" && !output.ha_topology.clustered
    error_message = "a single server with route_compression unset reports off"
  }

  # The report above reads the mode back out of the merge, so a merge carrying
  # "off" would satisfy it too. A single server's values must not change at all.
  assert {
    condition     = !contains(keys(yamldecode(helm_release.nats.values[0]).config.cluster), "merge")
    error_message = "a single server with route_compression unset must render no merge key on the cluster block"
  }
}

run "explicit_off_on_a_single_server" {
  command = plan

  variables {
    ha                = false
    route_compression = "off"
  }

  assert {
    condition     = output.ha_topology.route_compression == "off" && !output.ha_topology.clustered
    error_message = "a single server with route_compression = off reports off"
  }
}

# A single server renders no cluster block, so the mode would be dropped in silence.
run "compression_on_a_single_server_is_refused" {
  command = plan

  variables {
    ha                = false
    route_compression = "s2_fast"
  }

  expect_failures = [helm_release.nats]
}

# nats-server also accepts these spellings, and other casings; this module does
# not, so there is one way to write each mode and a typo cannot pass for one.
run "on_is_refused" {
  command = plan

  variables {
    route_compression = "on"
  }

  expect_failures = [var.route_compression]
}

run "accept_is_refused" {
  command = plan

  variables {
    route_compression = "accept"
  }

  expect_failures = [var.route_compression]
}

run "short_alias_is_refused" {
  command = plan

  variables {
    route_compression = "fast"
  }

  expect_failures = [var.route_compression]
}

run "uppercase_is_refused" {
  command = plan

  variables {
    route_compression = "S2_FAST"
  }

  expect_failures = [var.route_compression]
}

run "empty_is_refused" {
  command = plan

  variables {
    route_compression = ""
  }

  expect_failures = [var.route_compression]
}
