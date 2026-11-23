# Copyright The DeviceChain Authors
# SPDX-License-Identifier: Apache-2.0

# A root with NO backend block: tofu keeps state in a local file inside the work directory, which in
# a Job pod is a state nobody else can see. The runner must refuse it after init.
terraform {
  required_version = ">= 1.10"
}

resource "terraform_data" "cluster" {
  input = "x"
}
