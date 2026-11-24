# Copyright The DeviceChain Authors
# SPDX-License-Identifier: Apache-2.0

# A root whose module is a RELATIVE sibling in the tree, as the fleet roots' are
# ("../../opentofu/modules/gke-cluster"): the runner must copy and run the tree, not the root alone.
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

module "inner" {
  source = "../../modules/inner"
}
