# Copyright The DeviceChain Authors
# SPDX-License-Identifier: Apache-2.0

terraform {
  # Same OpenTofu floor as dcctl and the other roots; move it with CI pins.
  required_version = ">= 1.10"

  required_providers {
    # Pinned exactly, like the roots under deploy/opentofu: `init -upgrade` with a
    # range would move the provider on a routine re-run, and nobody chose that version.
    google = {
      source  = "hashicorp/google"
      version = "8.4.0"
    }
  }
}
