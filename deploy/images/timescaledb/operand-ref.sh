#!/usr/bin/env bash
# Copyright The DeviceChain Authors
# SPDX-License-Identifier: Apache-2.0
#
# Print the operand image's full reference (`ghcr.io/...:17.11-ts2.28.3-r1`),
# computed from versions.conf. The ONE formula for it: the publish workflow, the
# tofu-default check (hack/check-tofu-validations.sh) and the CI drills' "is this
# tag published yet?" probe (hack/ensure-operand-image.sh) all call this, so they
# cannot disagree about what tag the tree points at.
#
#   operand-ref.sh          # image:tag
#   operand-ref.sh --tag    # tag only
#
# Exits non-zero, printing nothing on stdout, if the PostgreSQL minor cannot be
# derived from PG_IMAGE.

set -euo pipefail

HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
IMAGE="${IMAGE:-ghcr.io/devicechain-io/postgresql-timescaledb}"

# shellcheck source=/dev/null
set -a; . "$HERE/versions.conf"; set +a

if ! pg_minor="$("$HERE/pg-minor.sh" "$PG_IMAGE")"; then
  echo "operand-ref.sh: could not derive a PostgreSQL minor from PG_IMAGE='${PG_IMAGE}'" >&2
  exit 1
fi

# The REVISION is part of the tag: the versions alone do not determine the bits
# (see IMAGE_REVISION in versions.conf).
tag="${pg_minor}-ts${TIMESCALEDB_VERSION}-r${IMAGE_REVISION}"

case "${1:-}" in
  --tag) printf '%s\n' "$tag" ;;
  "") printf '%s:%s\n' "$IMAGE" "$tag" ;;
  *) echo "usage: operand-ref.sh [--tag]" >&2; exit 2 ;;
esac
