# Copyright The DeviceChain Authors
# SPDX-License-Identifier: Apache-2.0

terraform {
  required_providers {
    # A RANGE, here; the exact pin is the root. A module is consumed by more than one root, each
    # of which chooses the one provider version it was measured against.
    google = {
      source  = "hashicorp/google"
      version = ">= 8.4.0, < 9.0.0"
    }
  }
}
