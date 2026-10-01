# Copyright The DeviceChain Authors
# SPDX-License-Identifier: Apache-2.0

terraform {
  # 1.8 for the test suite in tests/: `init` and `validate` read test files, and
  # OpenTofu parses `mock_provider` only from 1.8. (Terraform has it from 1.7, but
  # one floor is written for both tools, and the README states it.)
  required_version = ">= 1.8.0"

  required_providers {
    # Pinned exactly, like the roots under deploy/opentofu: `init -upgrade` with a
    # range would move the provider on a routine re-run, and nobody chose that version.
    google = {
      source  = "hashicorp/google"
      version = "8.4.0"
    }
  }
}
