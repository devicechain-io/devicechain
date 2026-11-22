# Copyright The DeviceChain Authors
# SPDX-License-Identifier: Apache-2.0

# State lives in the provider's own bucket, one prefix per cluster (fleet/<cluster_uid>/), written
# through the runner's impersonated identity. The bucket and prefix are given at init with
# -backend-config: a root with no backend block would silently use local state inside a Job pod, so
# a backend is always declared, and `init` with no configuration fails.
terraform {
  backend "gcs" {}
}
