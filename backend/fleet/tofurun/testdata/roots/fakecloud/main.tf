# Copyright The DeviceChain Authors
# SPDX-License-Identifier: Apache-2.0

# A stand-in for a fleet root that needs no cloud and no provider: the built-in terraform_data
# resource, with a "cloud" that is a shell check on the credential the child was handed. It has the
# fleet roots' shape where it matters to the runner: a pg backend with its three creation steps off,
# and state and plan encryption ENFORCED (the key itself arrives in TF_ENCRYPTION).
terraform {
  required_version = ">= 1.10"

  backend "pg" {
    skip_schema_creation = true
    skip_table_creation  = true
    skip_index_creation  = true
  }

  encryption {
    state {
      enforced = true
    }
    plan {
      enforced = true
    }
  }
}

variable "gen" {
  type    = string
  default = "1"
}

# Changing this replaces the cluster: a plan the gate must refuse in every kind but destroy.
variable "rev" {
  type    = string
  default = "1"
}

# A count, so a test can make a fresh plan differ from the saved one.
variable "extra" {
  type    = number
  default = 0
}

resource "terraform_data" "cluster" {
  input            = var.gen
  triggers_replace = var.rev

  # The fake cloud: creating needs the apply token, deleting needs the destroy token. The cloud
  # sees only what the runner put in the child's environment.
  provisioner "local-exec" {
    interpreter = ["sh", "-c"]
    command     = "test \"$FLEET_FAKE_CLOUD_TOKEN\" = apply-token"
  }

  provisioner "local-exec" {
    when        = destroy
    interpreter = ["sh", "-c"]
    command     = "test \"$FLEET_FAKE_CLOUD_TOKEN\" = destroy-token"
  }
}

resource "terraform_data" "extra" {
  count = var.extra
  input = count.index
}
