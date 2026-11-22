// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package schema

// FleetOperationWriterProtocolVersion is the device attribute writer protocol
// this device-management build follows, independently of database enforcement.
const FleetOperationWriterProtocolVersion = 1

// The next minor's enforcement migration MUST use these function and trigger
// names, and MUST NOT give the trigger a WHEN clause: readiness reports ENFORCED
// only for an unconditional trigger, because a condition (WHEN (false), say) can
// keep the function from ever running and the catalog cannot judge it. Filter by
// entity type and scope inside the function instead. Verifying the function-body
// hash is that release's job. This release creates neither the function nor the
// trigger.
const (
	FleetOperationEnforcementFunction = "dc_require_attr_protocol"
	FleetOperationEnforcementTrigger  = "dc_enforce_attr_protocol"
)
