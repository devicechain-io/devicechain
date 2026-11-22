# Copyright The DeviceChain Authors
# SPDX-License-Identifier: Apache-2.0

terraform {
  # Same OpenTofu floor as dcctl and the other roots. The runner will pin ONE exact version on top
  # of this floor (a floor cannot say "never two minors on one state").
  required_version = ">= 1.10"

  required_providers {
    # This root's OWN exact pin, deliberately not deploy/gke's. The premise record the fleet gates
    # read is bound to THIS version, so a routine provider bump on the public root is not held
    # hostage to a paid manual run, and moving this one is a deliberate act that re-runs the
    # provider-bound experiments. The set is exactly {google}: no in-cluster provider.
    google = {
      source  = "hashicorp/google"
      version = "8.4.0"
    }
  }
}
