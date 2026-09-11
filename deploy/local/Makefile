# Copyright The DeviceChain Authors
# SPDX-License-Identifier: Apache-2.0
#
# Convenience targets for the local kind cluster. See README.md.
# Override knobs inline, e.g.: make up INSTANCE=demo PROFILE=telemetry

HERE := $(dir $(abspath $(lastword $(MAKEFILE_LIST))))

# 🔴 THERE IS NO `up` TARGET, AND ITS ABSENCE IS DELIBERATE. Bring-up is
# `dcctl bootstrap local <instance>` -- one tool, one implementation of the rules.
# This file used to call deploy/local/up.sh, which applied the infrastructure tree
# directly; once dcctl became the only thing that mints an instance's credentials,
# that script could only produce an instance which comes up healthy and
# authenticates nothing. A make target forwarding to dcctl would just be a second
# name for it, which is the overlap this removal was for.
#
# The targets left here are the ones dcctl does NOT own: the cluster itself, the
# host diagnosis, and the image registry.
.PHONY: help preflight down purge images

help: ## Show this help
	@grep -hE '^[a-z-]+:.*?## ' $(MAKEFILE_LIST) | \
	  awk 'BEGIN{FS=":.*?## "}{printf "  \033[36m%-12s\033[0m %s\n", $$1, $$2}'

preflight: ## Diagnose the host before bring-up
	@"$(HERE)preflight.sh"

down: ## Tear down the cluster + cloud-provider-kind (keeps the registry)
	@"$(HERE)down.sh"

purge: ## Tear down everything including the local registry
	@"$(HERE)down.sh" --purge-registry

images: ## Build & push service + operator images to the registry (ko)
	@"$(HERE)build-images.sh"
