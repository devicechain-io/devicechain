# Copyright The DeviceChain Authors
# SPDX-License-Identifier: Apache-2.0

# Volume-snapshot base backups reach the chart only when they are asked for, and
# what reaches it is what was asked for.
#
# The module hands the chart its values as YAML documents, and the chart decides
# from `backup.snapshotClass` whether the daily base backup is a snapshot. A field
# dropped from `backup_values` is invisible to every other check the roots run:
# the plan is valid, the chart renders its default, and the store goes on taking
# daily full copies while the install record says it takes snapshots. This reads
# the values document Helm is handed.
#
# Runs against mocked providers, so no cluster is needed. Run it with
# hack/check-tofu-module-tests.sh, which tests a COPY of the module: `tofu init`
# in place would leave a provider cache in a tree dcctl embeds.

mock_provider "helm" {}
mock_provider "kubernetes" {}

variables {
  namespace          = "dc-system"
  name               = "dc-rdb"
  alias_service_name = "dc-postgresql"
  image              = "ghcr.io/example/postgres:17"
  instances          = 1
  database           = "dc"
  username           = "devicechain"
}

# The default: no class, and the weekly object-store schedule unused. The class
# the module reports is null, not "".
run "default_takes_no_snapshots" {
  command = plan

  variables {
    backup = {
      bucket                = "devicechain-rdb"
      endpoint_url          = "http://dc-object-store.dc-system:9000"
      credentials_secret    = "dc-object-store-credentials"
      access_key_id_key     = "MINIO_ROOT_USER"
      secret_access_key_key = "MINIO_ROOT_PASSWORD"
      retention_policy      = "30d"
    }
  }

  assert {
    condition     = yamldecode(helm_release.cluster.values[1]).backup.snapshotClass == ""
    error_message = "a store that did not ask for snapshots was handed a VolumeSnapshotClass"
  }
  assert {
    condition     = yamldecode(helm_release.cluster.values[1]).backup.objectStoreSchedule == "0 0 4 * * 0"
    error_message = "the object-store schedule's default is not Sunday 04:00"
  }
  assert {
    condition     = output.backup_snapshot_class == null
    error_message = "the module reports a snapshot class for a store taking none"
  }
}

# Asked for: both values reach the chart, and the report reads them back. The
# schedule is one no default has, so a literal on the path cannot pass.
run "snapshot_class_reaches_the_chart" {
  command = plan

  variables {
    backup = {
      bucket                = "devicechain-rdb"
      endpoint_url          = "http://dc-object-store.dc-system:9000"
      credentials_secret    = "dc-object-store-credentials"
      access_key_id_key     = "MINIO_ROOT_USER"
      secret_access_key_key = "MINIO_ROOT_PASSWORD"
      retention_policy      = "30d"
      snapshot_class        = "pd-snapshots"
      object_store_schedule = "0 0 5 * * 1"
    }
  }

  assert {
    condition     = yamldecode(helm_release.cluster.values[1]).backup.snapshotClass == "pd-snapshots"
    error_message = "the VolumeSnapshotClass did not reach the chart"
  }
  assert {
    condition     = yamldecode(helm_release.cluster.values[1]).backup.objectStoreSchedule == "0 0 5 * * 1"
    error_message = "the object-store schedule did not reach the chart"
  }
  assert {
    condition     = output.backup_snapshot_class == "pd-snapshots"
    error_message = "the module does not report the class its chart was handed"
  }
}

# No backups: no values document for them at all, so no class either.
run "no_backups_no_snapshots" {
  command = plan

  assert {
    condition     = length(helm_release.cluster.values) == 1
    error_message = "a store with no backups was handed a backup values document"
  }
  assert {
    condition     = output.backup_snapshot_class == null
    error_message = "a store with no backups reports a snapshot class"
  }
}
