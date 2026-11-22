# Copyright The DeviceChain Authors
# SPDX-License-Identifier: Apache-2.0

# Credentials come from the environment: the runner impersonates the apply identity
# (GOOGLE_IMPERSONATE_SERVICE_ACCOUNT). Nothing here names a credential.
provider "google" {
  project = var.project_id
}
