# Copyright The DeviceChain Authors
# SPDX-License-Identifier: Apache-2.0

# The write-ahead-log archive settings the module hands the chart: the
# compression is zstd, base backups stay gzip, and the parallelism is what the
# caller asked for.
#
# The chart has a default for each of these, so a value dropped from or wrong in
# `backup_values` still renders and installs: the archive simply runs with
# something nobody chose. This reads the values document Helm is handed. What the
# chart then renders from it is backend/cli/bootstrap/archivecompression_test.go.
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

# The relational store's shape: no parallelism asked for.
run "archive_is_zstd_at_the_default_parallelism" {
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
    condition     = yamldecode(helm_release.cluster.values[1]).backup.walCompression == "zstd"
    error_message = "the module does not hand the chart zstd for the write-ahead-log archive"
  }
  assert {
    condition     = yamldecode(helm_release.cluster.values[1]).backup.dataCompression == "gzip"
    error_message = "base backups must stay gzip: the plugin's data.compression has no zstd"
  }
  assert {
    condition     = yamldecode(helm_release.cluster.values[1]).backup.walMaxParallel == 2
    error_message = "the default write-ahead-log archive parallelism is not 2"
  }
}

# The event store asks for 4: a value no default has, so a literal on the path
# cannot pass.
run "wal_max_parallel_reaches_the_chart" {
  command = plan

  variables {
    backup = {
      bucket                = "devicechain-tsdb"
      endpoint_url          = "http://dc-object-store.dc-system:9000"
      credentials_secret    = "dc-object-store-credentials"
      access_key_id_key     = "MINIO_ROOT_USER"
      secret_access_key_key = "MINIO_ROOT_PASSWORD"
      retention_policy      = "7d"
      wal_max_parallel      = 4
    }
  }

  assert {
    condition     = yamldecode(helm_release.cluster.values[1]).backup.walMaxParallel == 4
    error_message = "wal_max_parallel did not reach the chart"
  }
}
