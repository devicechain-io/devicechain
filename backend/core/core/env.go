// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package core

const (
	ENV_INSTANCE_ID        = "DC_INSTANCE_ID"
	ENV_TENANT_ID          = "DC_TENANT_ID"
	ENV_TENANT_NAME        = "DC_TENANT_NAME"
	ENV_MICROSERVICE_ID    = "DC_MICROSERVICE_ID"
	ENV_MICROSERVICE_NAME  = "DC_MICROSERVICE_NAME"
	ENV_MS_FUNCTIONAL_AREA = "DC_MS_FUNCTIONAL_AREA"

	// ENV_LOG_CONSOLE, when set to a non-empty value, selects the human-friendly
	// colorized console logger for local development. Unset (the default, and how
	// the Helm chart runs) emits structured JSON for log aggregation (E16).
	ENV_LOG_CONSOLE = "DC_LOG_CONSOLE"
)
