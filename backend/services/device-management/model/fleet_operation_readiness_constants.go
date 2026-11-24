// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

// FleetOperationWriterProtocolVersion is the device attribute writer protocol
// this device-management build follows, independently of database enforcement.
const FleetOperationWriterProtocolVersion = 1

// The enforcement migration (schema.NewAttributeWriterProtocolSchema) uses these function and
// trigger names, and gives the trigger NO WHEN clause: readiness reports ENFORCED only for an
// unconditional trigger, because a condition (WHEN (false), say) can keep the function from ever
// running and the catalog cannot judge it. The function filters by entity type and scope inside.
const (
	FleetOperationEnforcementFunction = "dc_require_attr_protocol"
	FleetOperationEnforcementTrigger  = "dc_enforce_attr_protocol"
)

// FleetOperationEnforcementFunctionMD5 is the md5 of the enforcing function's body as the
// migration ships it (schema.AttributeWriterProtocolFunctionSource). The catalog check compares it
// with md5(prosrc) of the installed function, so a function of the right NAME with another body
// (a hand edit, or a no-op left by someone silencing the trigger) is not enforcement.
// TestEnforcementFunctionHashIsPinned holds this and the migration's text in step.
const FleetOperationEnforcementFunctionMD5 = "3eca922a5325ae35a01d0058e9c279a2"
