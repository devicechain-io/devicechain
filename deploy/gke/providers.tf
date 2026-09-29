# Copyright The DeviceChain Authors
# SPDX-License-Identifier: Apache-2.0

# Credentials come from the environment: `gcloud auth application-default login`
# on a workstation, or a service account's GOOGLE_APPLICATION_CREDENTIALS in CI.
provider "google" {
  project = var.project_id
}
