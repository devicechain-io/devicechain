#!/usr/bin/env bash
# Copyright The DeviceChain Authors
# SPDX-License-Identifier: Apache-2.0
#
# Writes .env with two random broker passwords, once. Re-running keeps them, so the Kubernetes
# Secret created from them stays in step with what the broker was given.
set -euo pipefail
cd "$(dirname "$0")"
if [ -f .env ]; then
  echo ".env exists; keeping it"
  exit 0
fi
umask 077
{
  echo "HOST_PASSWORD=$(openssl rand -hex 16)"
  echo "NODE_PASSWORD=$(openssl rand -hex 16)"
} > .env
echo "wrote .env"
