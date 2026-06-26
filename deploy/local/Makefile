# Copyright The DeviceChain Authors
# SPDX-License-Identifier: Apache-2.0
#
# Convenience targets for the local kind cluster. See README.md.
# Override knobs inline, e.g.: make up INSTANCE=demo PROFILE=telemetry

HERE := $(dir $(abspath $(lastword $(MAKEFILE_LIST))))

.PHONY: help preflight up down purge images

help: ## Show this help
	@grep -hE '^[a-z-]+:.*?## ' $(MAKEFILE_LIST) | \
	  awk 'BEGIN{FS=":.*?## "}{printf "  \033[36m%-12s\033[0m %s\n", $$1, $$2}'

preflight: ## Diagnose the host before bring-up
	@"$(HERE)preflight.sh"

up: ## Create cluster + registry + LB, apply infra, install core + chart, seed
	@"$(HERE)up.sh"

down: ## Tear down the cluster + cloud-provider-kind (keeps the registry)
	@"$(HERE)down.sh"

purge: ## Tear down everything including the local registry
	@"$(HERE)down.sh" --purge-registry

images: ## Build & push service + operator images to the registry (ko)
	@"$(HERE)build-images.sh"
