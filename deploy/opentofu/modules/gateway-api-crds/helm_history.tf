# Copyright The DeviceChain Authors
# SPDX-License-Identifier: Apache-2.0

# 🔴 THE PROVIDER'S DEFAULT IS UNLIMITED, WHICH IS NOT THE `helm` COMMAND'S DEFAULT.
# `max_history` is documented as "Defaults to 0 (no limit)", where the CLI passes 10.
# So a release left unset keeps every revision it has ever had, each one holding the
# values it was rendered with, and the record only ever grows.
#
# Locals do not cross module boundaries, so this is declared per module rather than
# passed in: a variable would put the number in the caller's hands, and a release
# whose caller forgot it would be silently unbounded again — the shape this exists to
# prevent. hack/check-helm-release-history.sh refuses a helm_release without it.
locals {
  helm_max_history = 10
}
