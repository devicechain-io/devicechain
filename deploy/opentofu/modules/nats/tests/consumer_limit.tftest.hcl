# Copyright The DeviceChain Authors
# SPDX-License-Identifier: Apache-2.0

# Every server is handed default_max_consumers = -1, with broker auth on and off.
#
# nats-server 2.15 caps each stream at 1,000 consumers unless the stream or the
# account sets a limit, and the MQTT gateway's $MQTT_msgs stream sets neither, so
# without this the 1,001st QoS 1 subscription in a fleet is refused (see
# config.jetstream.merge in main.tf). The chart has no values schema, so a limits
# block under the wrong parent would be accepted and do nothing; this reads the
# values document Helm is handed.
#
# The auth branch is checked separately because it rebuilds `config` with merge():
# a jetstream map dropped there would leave every authenticated instance, which is
# every real one, on the 1,000 default.
#
# Runs against mocked providers, so no cluster is needed. Run it with
# hack/check-tofu-module-tests.sh, which tests a COPY of the module.

mock_provider "helm" {}
mock_provider "kubernetes" {}

variables {
  namespace   = "dci-test"
  enable_tls  = false
  enable_auth = false
}

run "no_per_stream_consumer_default" {
  command = plan

  assert {
    condition     = try(yamldecode(helm_release.nats.values[0]).config.jetstream.merge.limits.default_max_consumers, null) == -1
    error_message = "config.jetstream.merge.limits.default_max_consumers must be -1, or the 1,001st MQTT QoS 1 subscription is refused"
  }

  assert {
    condition     = output.ha_topology.default_max_consumers == -1
    error_message = "ha_topology.default_max_consumers must report -1"
  }
}

run "with_broker_auth" {
  command = plan

  variables {
    enable_auth             = true
    callout_issuer_public   = "AAUTHCALLOUTISSUERPUBLICKEYPLACEHOLDER"
    service_password_bcrypt = "$2a$11$placeholderplaceholderplaceholderplaceholderplace"
    service_users           = "{\"APP\":{\"dc_service\":{\"publish\":{\"deny\":[\"*.*.devices.*.events\"]}}}}"
  }

  assert {
    condition     = try(yamldecode(helm_release.nats.values[0]).config.jetstream.merge.limits.default_max_consumers, null) == -1
    error_message = "the broker-auth branch must keep config.jetstream.merge.limits.default_max_consumers = -1"
  }

  # The jetstream block the auth branch rebuilds must still be the whole block.
  assert {
    condition     = try(yamldecode(helm_release.nats.values[0]).config.jetstream.enabled, null) == true
    error_message = "the broker-auth branch must keep JetStream enabled"
  }
}

# default_max_consumers is a nats-server 2.15 key: 2.14 refuses it at startup
# ("unknown field"), so the module refuses an older chart rather than render a config
# that would crash-loop the broker.
run "an_older_chart_is_refused" {
  command = plan

  variables {
    chart_version = "2.14.4"
  }

  expect_failures = [var.chart_version]
}

run "the_floor_itself_is_accepted" {
  command = plan

  variables {
    chart_version = "2.15.0"
  }
}
