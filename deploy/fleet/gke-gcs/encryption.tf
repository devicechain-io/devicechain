# Copyright The DeviceChain Authors
# SPDX-License-Identifier: Apache-2.0

# State and plan are encrypted, and encryption is ENFORCED: a run with no key fails rather than
# writing a plaintext state or plan. The key provider and methods are not in this file; they are
# supplied through the TF_ENCRYPTION environment variable, because a key does not belong in a
# repository. `enforced = true` has to be in the file so that CI's throwaway key cannot hide its
# absence: a static test asserts both lines, and a negative step runs a plan with no key.
terraform {
  encryption {
    state {
      enforced = true
    }
    plan {
      enforced = true
    }
  }
}
