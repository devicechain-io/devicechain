# Copyright The DeviceChain Authors
# SPDX-License-Identifier: Apache-2.0
# shellcheck shell=bash
#
# Sourced, not run. The ONE shell reader of the object store's image default.
#
# Three things need that value: hack/dr-rig.sh (to run the same bytes a default
# install runs), hack/check-image-pulls.sh (to prove its enumeration found the
# image it exists for) and .github/workflows/ko-base-image.yml (to advance the
# digest). Each carrying its own parser is three readings of one input that can
# disagree, so they share this one. The Go test that guards the value's shape
# (deploy/objectstore_image_test.go) reads the same grammar: start at the line
# `variable "image" {`, stop at the first line that is exactly `}`, take the first
# `default = "..."` in between.

# The object store module, relative to the repository root.
# shellcheck disable=SC2034 # read by the scripts that source this
OBJECT_STORE_MODULE_REL="deploy/opentofu/modules/object-store/main.tf"

# image:tag@sha256:<64 lowercase hex>, anchored — the same pattern the Go test
# enforces. Kept here so the shell readers cannot drift from each other.
# shellcheck disable=SC2034 # read by the scripts that source this
OBJECT_STORE_IMAGE_PATTERN='^[a-z0-9][a-zA-Z0-9._/-]*:[A-Za-z0-9][A-Za-z0-9._-]*@sha256:[0-9a-f]{64}$'

# object_store_image_from FILE — print the default of `variable "image"` in FILE,
# or nothing when the block or its default is missing. Callers check the result
# against OBJECT_STORE_IMAGE_PATTERN: an empty or malformed read must stop them,
# never be passed on to docker as a reference.
object_store_image_from() {
  awk '
    /^variable "image" \{[ \t]*$/ { in_var = 1; next }
    in_var && /^\}[ \t]*$/        { exit }
    in_var && /^[ \t]*default[ \t]*=[ \t]*"/ {
      sub(/^[ \t]*default[ \t]*=[ \t]*"/, ""); sub(/"[ \t]*$/, ""); print; exit
    }' "$1"
}
