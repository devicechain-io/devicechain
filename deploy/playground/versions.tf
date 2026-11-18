# Copyright The DeviceChain Authors
# SPDX-License-Identifier: Apache-2.0

terraform {
  # 1.8 for the test suite in tests/, which uses mock_provider (see deploy/gke).
  required_version = ">= 1.10"

  # Pinned exactly, like every other root in this repository: `init -upgrade` with a
  # range would move a provider on a routine re-run, and nobody chose that version.
  required_providers {
    google = {
      source  = "hashicorp/google"
      version = "8.4.0"
    }
    kubernetes = {
      source  = "hashicorp/kubernetes"
      version = "3.2.1"
    }
  }
}
