// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// Package product implements `dcctl product export | plan | apply` -- the git-native way to
// promote a tenant's product between tenants and instances.
//
// A product is a directory (the bundle) with a product.yaml and one file per resource. It is
// read and written entirely over the tenant GraphQL planes the console already uses, as the
// identity that runs the command; the package adds no server behaviour and no schema.
//
// The bundle format is PREVIEW (apiVersion product.devicechain.io/v1alpha1): it spends one
// minor outside the versioning policy's covered surfaces, and a later apiVersion may change
// it. Reading a bundle with an apiVersion this build does not know is refused rather than
// guessed at.
//
// 🔴 SECRETS ARE HANDLES, NEVER VALUES. A connector's credential is write-only on the server
// (only "is one set" is readable) and an integration's bearer token is stored as a digest, so
// an export CANNOT contain either. The bundle names a connector credential by handle
// ("env:NAME"); apply resolves it at apply time. Independently, every bundle is scanned for
// anything shaped like a secret value, and an export or a read that finds one is refused.
package product

import "encoding/json"

// APIVersion is the bundle format's version. Preview tier: see the package comment.
const APIVersion = "product.devicechain.io/v1alpha1"

// Kind names a resource family in a bundle, a plan and an error.
type Kind string

const (
	KindDeviceType  Kind = "DeviceType"
	KindProfile     Kind = "DeviceProfile"
	KindMetric      Kind = "Metric"
	KindCalculated  Kind = "CalculatedMetric"
	KindCommand     Kind = "Command"
	KindRule        Kind = "DetectionRule"
	KindIntegration Kind = "Integration"
	KindConnector   Kind = "Connector"
	KindDashboard   Kind = "Dashboard"
)

// Doc is an opaque JSON document the platform stores as text and does not interpret here (a
// decoder, a rule, a canvas graph, a dashboard, a parameter schema, metadata). It is always
// held in canonical form -- sorted keys, no insignificant whitespace -- so two equal documents
// compare byte-equal and a re-export is stable.
type Doc = json.RawMessage

// Product is product.yaml: the bundle's manifest.
type Product struct {
	APIVersion string `json:"apiVersion"`
	Kind       string `json:"kind"`
	// Name is an optional label for the product. It is never sent to the tenant.
	Name string `json:"name,omitempty"`
}

// DeviceType is one device type.
type DeviceType struct {
	Kind            string `json:"kind"`
	Token           string `json:"token"`
	Name            string `json:"name,omitempty"`
	Description     string `json:"description,omitempty"`
	ImageURL        string `json:"imageUrl,omitempty"`
	Icon            string `json:"icon,omitempty"`
	BackgroundColor string `json:"backgroundColor,omitempty"`
	ForegroundColor string `json:"foregroundColor,omitempty"`
	BorderColor     string `json:"borderColor,omitempty"`
	// Profile is the token of the device profile the type adopts.
	Profile string `json:"profile,omitempty"`
	// MaintenanceWindow is the token of a maintenance window. Windows are NOT part of this
	// bundle: the token is a reference, and applying fails (the server refuses an unknown
	// token) on a tenant that does not already hold the window.
	MaintenanceWindow string `json:"maintenanceWindow,omitempty"`
	Manufacturer      string `json:"manufacturer,omitempty"`
	Model             string `json:"model,omitempty"`
	Metadata          Doc    `json:"metadata,omitempty"`
}

// Location declares that a profile's devices report their own position.
type Location struct {
	ExpectedAccuracyMeters        *float64 `json:"expectedAccuracyMeters,omitempty"`
	ExpectedUpdateIntervalSeconds *int     `json:"expectedUpdateIntervalSeconds,omitempty"`
}

// ConfigKey is one device-visible configuration key a profile declares.
type ConfigKey struct {
	Key         string `json:"key"`
	ValueType   string `json:"valueType"`
	Description string `json:"description,omitempty"`
}

// Metric is a measured metric: one devices report.
type Metric struct {
	Token       string   `json:"token"`
	Key         string   `json:"key"`
	Name        string   `json:"name,omitempty"`
	Description string   `json:"description,omitempty"`
	DataType    string   `json:"dataType"`
	Unit        string   `json:"unit,omitempty"`
	Min         *float64 `json:"min,omitempty"`
	Max         *float64 `json:"max,omitempty"`
	// Enum is a JSON array of the allowed string values.
	Enum       Doc    `json:"enum,omitempty"`
	Descriptor string `json:"descriptor,omitempty"`
	Metadata   Doc    `json:"metadata,omitempty"`
}

// CalculatedMetric is a metric computed from a formula over the profile's measured metrics.
type CalculatedMetric struct {
	Token       string   `json:"token"`
	Key         string   `json:"key"`
	Name        string   `json:"name,omitempty"`
	Description string   `json:"description,omitempty"`
	DataType    string   `json:"dataType"`
	Unit        string   `json:"unit,omitempty"`
	Min         *float64 `json:"min,omitempty"`
	Max         *float64 `json:"max,omitempty"`
	Enum        Doc      `json:"enum,omitempty"`
	Descriptor  string   `json:"descriptor,omitempty"`
	Metadata    Doc      `json:"metadata,omitempty"`
	Expression  string   `json:"expression"`
}

// Command is a typed command definition.
type Command struct {
	Token           string `json:"token"`
	Key             string `json:"key"`
	Name            string `json:"name,omitempty"`
	Description     string `json:"description,omitempty"`
	ParameterSchema Doc    `json:"parameterSchema,omitempty"`
	Impact          string `json:"impact,omitempty"`
	Metadata        Doc    `json:"metadata,omitempty"`
}

// Rule is a DETECT rule: the authored definition and, optionally, the canvas graph it was
// authored with.
type Rule struct {
	Token          string `json:"token"`
	Name           string `json:"name,omitempty"`
	Description    string `json:"description,omitempty"`
	Definition     Doc    `json:"definition"`
	AuthoringGraph Doc    `json:"authoringGraph,omitempty"`
	Enabled        bool   `json:"enabled"`
	Metadata       Doc    `json:"metadata,omitempty"`
	// EntityGroupToken and EntityGroupVersion scope the rule to a published dynamic group.
	// Groups are NOT part of this bundle; they are references.
	EntityGroupToken   string `json:"entityGroupToken,omitempty"`
	EntityGroupVersion *int   `json:"entityGroupVersion,omitempty"`
}

// Profile is a device profile with everything authored on it. The metric, command and rule
// lists are part of the profile: applying a profile makes the tenant's draft hold exactly
// these, deleting a draft definition the bundle no longer lists.
type Profile struct {
	Kind                     string             `json:"kind"`
	Token                    string             `json:"token"`
	Name                     string             `json:"name,omitempty"`
	Description              string             `json:"description,omitempty"`
	Category                 string             `json:"category,omitempty"`
	Metadata                 Doc                `json:"metadata,omitempty"`
	Location                 *Location          `json:"location,omitempty"`
	ConfigurationDeclaration []ConfigKey        `json:"configurationDeclaration,omitempty"`
	Decoder                  Doc                `json:"decoder,omitempty"`
	Metrics                  []Metric           `json:"metrics,omitempty"`
	CalculatedMetrics        []CalculatedMetric `json:"calculatedMetrics,omitempty"`
	Commands                 []Command          `json:"commands,omitempty"`
	Rules                    []Rule             `json:"rules,omitempty"`
}

// Integration is an inbound integration. Its bearer token is generated by the server, stored
// as a digest and never readable, so it is not -- and cannot be -- in a bundle.
type Integration struct {
	Kind        string `json:"kind"`
	Token       string `json:"token"`
	Name        string `json:"name,omitempty"`
	Description string `json:"description,omitempty"`
	// Type is immutable once created: CHIRPSTACK_V4, TTS_V3, GENERIC_JSON or GENERIC_BINARY.
	Type        string   `json:"type"`
	Enabled     bool     `json:"enabled"`
	Identity    string   `json:"identity,omitempty"`
	Generic     Doc      `json:"generic,omitempty"`
	LinkMetrics bool     `json:"linkMetrics,omitempty"`
	DeviceTypes []string `json:"deviceTypes"`
	Metadata    Doc      `json:"metadata,omitempty"`
}

// Credential names where a connector's secret comes from. Only handles are ever stored.
type Credential struct {
	// Handle is "env:NAME": the secret is read from that environment variable at apply time.
	Handle string `json:"handle"`
}

// Connector is an outbound connector. Its credential is a handle, never a value.
type Connector struct {
	Kind        string      `json:"kind"`
	Token       string      `json:"token"`
	Name        string      `json:"name,omitempty"`
	Description string      `json:"description,omitempty"`
	Type        string      `json:"type"`
	Config      Doc         `json:"config"`
	Credential  *Credential `json:"credential,omitempty"`
}

// Dashboard is a dashboard; its definition is opaque JSON.
type Dashboard struct {
	Kind        string `json:"kind"`
	Token       string `json:"token"`
	Name        string `json:"name,omitempty"`
	Description string `json:"description,omitempty"`
	Definition  Doc    `json:"definition"`
}

// Bundle is a whole product, in memory.
type Bundle struct {
	Product      Product
	DeviceTypes  []DeviceType
	Profiles     []Profile
	Integrations []Integration
	Connectors   []Connector
	Dashboards   []Dashboard
}
