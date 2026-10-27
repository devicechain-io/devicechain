// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"strings"
	"time"

	"github.com/devicechain-io/dc-microservice/devicealias"
	"github.com/devicechain-io/dc-microservice/rdb"
)

// DeviceAlias is one identity alias of a device: a (scheme, value) pair naming it in a
// sender's vocabulary, such as a LoRaWAN DevEUI (devicealias.SchemeDevEUI). It is the key an
// inbound integration finds a device by (ResolveIntegrationDevice).
//
// The value is stored in the canonical spelling devicealias.Normalize gives it, so the same
// DevEUI written in either case is one alias. The pair is unique within a tenant, and a device
// has at most one value per scheme. The row is hard-deleted with its device (the foreign key
// cascades, and DeleteDevice removes it explicitly) and carries no soft delete, so a deleted
// device frees its aliases at once.
//
// It is deliberately not customer-scopable and declares no scope rule: aliases are read through
// their device, which is.
type DeviceAlias struct {
	ID        uint `gorm:"primarykey"`
	CreatedAt time.Time
	TenantId  string `gorm:"not null;size:128;uniqueIndex:uix_device_aliases_tenant_scheme_value,priority:1"`
	DeviceId  uint   `gorm:"not null;uniqueIndex:uix_device_aliases_device_scheme,priority:1"`
	Scheme    string `gorm:"not null;size:70;uniqueIndex:uix_device_aliases_tenant_scheme_value,priority:2;uniqueIndex:uix_device_aliases_device_scheme,priority:2"`
	Value     string `gorm:"not null;size:128;uniqueIndex:uix_device_aliases_tenant_scheme_value,priority:3"`
}

// AuditLabel implements rdb.AuditLabeler: the alias, so the journal reads "device_aliases
// lorawan.deveui=70B3D57ED0000001". An alias names a device to a sender and is not a credential.
func (a DeviceAlias) AuditLabel() string { return a.Scheme + "=" + a.Value }

// DeviceAliasInput is an alias as a caller writes it, before normalization.
type DeviceAliasInput struct {
	Scheme string
	Value  string
}

// OptionalDeviceAliasList is the partial-update state of a device's alias set. Omitted leaves
// the stored aliases alone. An explicit null or an empty list REMOVES them all, the same fold
// the platform's other list fields apply (dcgraphql.OptionalStringList). A list REPLACES the
// set, atomically with the rest of the update.
//
// It implements the same three-state markers as the Optional* types in core/graphql, which the
// partial-update guard checks for on every field of an update request.
type OptionalDeviceAliasList struct {
	// Set is true only when the field was present in the request, whatever its value.
	Set bool
	// Value is the list sent; empty when the field was null or [].
	Value []DeviceAliasInput
}

func (OptionalDeviceAliasList) ImplementsGraphQLType(name string) bool {
	return name == "[DeviceAliasInput!]"
}

// Nullable marks this as a type that can accept an explicit null. REQUIRED: without it the
// schema will not build.
func (OptionalDeviceAliasList) Nullable() {}

func (o *OptionalDeviceAliasList) UnmarshalGraphQL(input any) error {
	o.Set = true
	o.Value = []DeviceAliasInput{}
	if input == nil {
		return nil
	}
	// A single value where a list is expected is coerced to a one-entry list, as the list
	// coercion rule says and as the library does for an ordinary list field.
	entries, ok := input.([]any)
	if !ok {
		entries = []any{input}
	}
	for i, entry := range entries {
		obj, ok := entry.(map[string]any)
		if !ok {
			return invalidDeviceAlias("alias at index %d must be an object with a scheme and a value", i)
		}
		var in DeviceAliasInput
		for k, v := range obj {
			s, ok := v.(string)
			if !ok {
				return invalidDeviceAlias("alias at index %d: %s must be a string", i, k)
			}
			switch k {
			case "scheme":
				in.Scheme = s
			case "value":
				in.Value = s
			default:
				return invalidDeviceAlias("alias at index %d has an unknown field %q", i, k)
			}
		}
		o.Value = append(o.Value, in)
	}
	return nil
}

// ApplyTo returns the alias inputs the update leaves the device with and whether the stored
// set is to be replaced at all.
func (o OptionalDeviceAliasList) ApplyTo() ([]DeviceAliasInput, bool) {
	if !o.Set {
		return nil, false
	}
	return o.Value, true
}

// OptionalDeviceAliasListOf builds a field in the "sent with a value" state.
func OptionalDeviceAliasListOf(v []DeviceAliasInput) OptionalDeviceAliasList {
	return OptionalDeviceAliasList{Set: true, Value: v}
}

// renderDeviceAliases renders aliases as "scheme=value", the form a test reads them back in.
func renderDeviceAliases(aliases []DeviceAlias) []string {
	out := make([]string, 0, len(aliases))
	for _, a := range aliases {
		out = append(out, a.Scheme+"="+a.Value)
	}
	return out
}

// integrationIdentityScheme is the alias scheme an integration resolves devices by, derived
// from its identity: DEV_EUI is lorawan.deveui, DEVICE_ID is tts.device-id and ALIAS:<scheme>
// is the scheme named. "" for an identity this build does not know, which resolves nothing.
func integrationIdentityScheme(identity string) string {
	switch identity {
	case IntegrationIdentityDevEUI:
		return devicealias.SchemeDevEUI
	case IntegrationIdentityDeviceID:
		return devicealias.SchemeTTSDeviceID
	}
	if scheme, ok := strings.CutPrefix(identity, IntegrationIdentityAliasPrefix); ok && devicealias.IsCustomScheme(scheme) {
		return scheme
	}
	return ""
}

// AliasScheme is the alias scheme this integration resolves devices by, "" for an identity this
// build does not know.
func (i *Integration) AliasScheme() string { return integrationIdentityScheme(i.Identity) }

// IntegrationUnregisteredDevice is a sender-named device the tenant has no device for, as an
// integration saw it: one row per (integration, scheme, value), with the first and last time
// it was seen and how many times. It is what the console offers to turn into a device with
// the alias prefilled.
//
// Rows are capped per integration (MaxUnregisteredPerIntegration); past the cap new values are
// counted by the caller and not stored. A row goes with its integration.
type IntegrationUnregisteredDevice struct {
	ID            uint `gorm:"primarykey"`
	CreatedAt     time.Time
	TenantId      string    `gorm:"index;not null;size:128"`
	IntegrationId uint      `gorm:"not null;uniqueIndex:uix_integration_unregistered_devices_key,priority:1"`
	Scheme        string    `gorm:"not null;size:70;uniqueIndex:uix_integration_unregistered_devices_key,priority:2"`
	Value         string    `gorm:"not null;size:128;uniqueIndex:uix_integration_unregistered_devices_key,priority:3"`
	FirstSeen     time.Time `gorm:"not null"`
	LastSeen      time.Time `gorm:"not null"`
	Count         int64     `gorm:"not null;default:1"`

	// IntegrationToken is the owning integration's token, filled by the read. Not a column.
	IntegrationToken string `gorm:"-"`
}

// AuditExempt implements rdb.AuditExempt. A sighting is an observation the data plane writes on
// every first (and every refreshed) miss of an unknown sender id, not a control-plane change:
// journalling it would let a sender spraying identifiers drive journal writes for no security
// value. The integration and the device a tenant creates from a sighting are journalled.
func (IntegrationUnregisteredDevice) AuditExempt() bool { return true }

// DefaultOrder implements rdb.Sortable: most recently seen first, id as the unique tiebreak.
func (IntegrationUnregisteredDevice) DefaultOrder() string {
	return "integration_unregistered_devices.last_seen DESC, integration_unregistered_devices.id ASC"
}

// MaxUnregisteredPerIntegration caps how many distinct unregistered devices one integration
// records. A sender spraying identifiers cannot grow the table without bound.
const MaxUnregisteredPerIntegration = 1000

// Search criteria for the unregistered devices an integration has seen.
type UnregisteredDeviceSearchCriteria struct {
	rdb.Pagination
	// Integration is the token of the integration; required.
	Integration string
}

// Results for unregistered device search.
type UnregisteredDeviceSearchResults struct {
	Results    []IntegrationUnregisteredDevice
	Pagination rdb.SearchResultsPagination
}

// IntegrationDeviceOutcome is what ResolveIntegrationDevice concluded.
type IntegrationDeviceOutcome string

const (
	// IntegrationDeviceResolved: the alias names a device whose type is in the integration's scope.
	IntegrationDeviceResolved IntegrationDeviceOutcome = "RESOLVED"
	// IntegrationDeviceUnregistered: no device in scope carries the alias. The sighting was recorded
	// (unless Overflow). A device that carries the alias but is out of scope reads exactly the same,
	// so a sender learns nothing about devices it may not reach.
	IntegrationDeviceUnregistered IntegrationDeviceOutcome = "UNREGISTERED"
	// IntegrationDeviceDisabled: the token matched an integration an operator has switched off. This
	// is NOT a credential failure and nothing is resolved or recorded.
	IntegrationDeviceDisabled IntegrationDeviceOutcome = "DISABLED"
	// IntegrationDeviceUnauthenticated: no such integration, or the token did not match. The one
	// answer to both.
	IntegrationDeviceUnauthenticated IntegrationDeviceOutcome = "UNAUTHENTICATED"
)

// IntegrationDeviceResolution is the answer to ResolveIntegrationDevice.
type IntegrationDeviceResolution struct {
	Outcome IntegrationDeviceOutcome
	// DeviceToken and DeviceTypeToken are set only for RESOLVED.
	DeviceToken     string
	DeviceTypeToken string
	// Overflow is true for an UNREGISTERED outcome whose sighting was NOT stored because the
	// integration already records MaxUnregisteredPerIntegration distinct devices.
	Overflow bool
}
