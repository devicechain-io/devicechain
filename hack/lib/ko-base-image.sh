# Copyright The DeviceChain Authors
# SPDX-License-Identifier: Apache-2.0
# shellcheck shell=bash
#
# Sourced, not run. The ONE shell reader of `.ko.yaml`'s defaultBaseImage.
#
# Three things need that value: hack/check-ko-base-pin.sh (is it a digest pin?),
# .github/workflows/ko-base-image.yml (to advance the digest) and
# hack/check-image-pulls.sh (to prove its enumeration reached a pin outside
# deploy/). Each carrying its own awk is three readings of one input that can
# disagree, so they share this one.

# .ko.yaml, relative to the repository root.
# shellcheck disable=SC2034 # read by the scripts that source this
KO_BASE_FILE_REL=".ko.yaml"

# ko_base_image_from FILE — print the defaultBaseImage value in FILE, or nothing.
#
# Strips the key and surrounding whitespace rather than splitting on ':' — an
# image reference contains colons of its own (the tag, and the digest's algorithm
# prefix), so a field split returns a truncated ref that then fails the digest
# check for the wrong reason. check-ko-base-pin.sh's "a correct pin passes"
# self-test case is what caught that.
ko_base_image_from() {
  awk '/^defaultBaseImage:/ {
         sub(/^defaultBaseImage:[[:space:]]*/, "")
         sub(/[[:space:]]*$/, "")
         print; exit
       }' "$1"
}
