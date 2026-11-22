# Copyright The DeviceChain Authors
# SPDX-License-Identifier: Apache-2.0
# Pinned toolchain for the Tier K experiments: the premise record binds to tofu 1.10.10, so the
# harness runs in an image whose tofu is exactly that, whatever the host has installed.
FROM debian:12-slim
RUN apt-get update && apt-get install -y --no-install-recommends ca-certificates curl unzip python3 postgresql-client util-linux iptables bash git procps && rm -rf /var/lib/apt/lists/*
ARG TOFU=1.10.10
RUN curl -fsSL -o /tmp/t.zip https://github.com/opentofu/opentofu/releases/download/v${TOFU}/tofu_${TOFU}_linux_amd64.zip && unzip -q /tmp/t.zip tofu -d /usr/local/bin && rm /tmp/t.zip && tofu version
RUN curl -fsSL -o /usr/local/bin/kubectl https://dl.k8s.io/release/v1.34.1/bin/linux/amd64/kubectl && chmod +x /usr/local/bin/kubectl
RUN curl -fsSL -o /usr/local/bin/kind https://kind.sigs.k8s.io/dl/v0.33.0/kind-linux-amd64 && chmod +x /usr/local/bin/kind
COPY --from=docker:29-cli /usr/local/bin/docker /usr/local/bin/docker
