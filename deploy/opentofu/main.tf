# Copyright The DeviceChain Authors
# SPDX-License-Identifier: Apache-2.0

module "namespace" {
  source = "./modules/namespace"

  name   = var.namespace
  create = var.create_namespace
}

# NATS: messaging + MQTT ingress + JetStream KV (ADR-003/006/007).
module "nats" {
  source = "./modules/nats"

  namespace         = var.namespace
  chart_version     = var.nats_chart_version
  jetstream_storage = var.nats_jetstream_storage
  ha                = var.ha

  depends_on = [module.namespace]
}

# Relational Postgres — the control-plane RDB (users, devices, relationships, …).
module "postgres" {
  source = "./modules/postgres"

  namespace = var.namespace
  name      = "dc-postgresql"
  image     = var.postgres_image
  database  = var.postgres_database
  username  = var.postgres_username
  password  = var.postgres_password
  storage   = var.postgres_storage

  depends_on = [module.namespace]
}

# TimescaleDB — event hypertables (ADR-004). Same generic module, different image:
# TimescaleDB is a Postgres superset, so the StatefulSet shape is identical.
module "timescaledb" {
  source = "./modules/postgres"

  namespace = var.namespace
  name      = "dc-timescaledb-single"
  image     = var.timescale_image
  database  = var.timescale_database
  username  = var.timescale_username
  password  = var.timescale_password
  storage   = var.timescale_storage

  depends_on = [module.namespace]
}
