// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// Package functionalarea is the canonical deployment catalog for DeviceChain's
// functional areas (ADR-022 decision 2): which services exist, the messaging
// contract each declares, their inter-area dependencies, and the named profiles
// that expand to a valid enabled set.
//
// Since decision 4 narrowed the operator and moved workload rendering to the
// Helm chart, the deployment-time dependency gate that actually runs is the
// chart's `devicechain.enabledAreas` template guard, which mirrors this catalog.
// No production Go code consults this package today (only its tests do); it is
// deliberately retained as the single Go source of truth for that guard and for
// the deferred dependency-admission webhook (ADR-022 review E18), which would
// re-establish a Go enforcement path so the Helm template stops being a parallel
// reimplementation. Until then the webhook is consciously descoped in favor of
// the template guard (tracked on the roadmap).
package functionalarea

import (
	"fmt"
	"sort"
	"strings"
)

// FunctionalArea identifies a deployable DeviceChain service.
type FunctionalArea string

const (
	UserManagement   FunctionalArea = "user-management"
	DeviceManagement FunctionalArea = "device-management"
	EventSources     FunctionalArea = "event-sources"
	EventManagement  FunctionalArea = "event-management"
	DeviceState      FunctionalArea = "device-state"
	DashboardMgmt    FunctionalArea = "dashboard-management"
	CommandDelivery  FunctionalArea = "command-delivery"
	NotificationMgmt FunctionalArea = "notification-management"
	EventProcessing  FunctionalArea = "event-processing"
	OutboundConn     FunctionalArea = "outbound-connectors"
	Mcp              FunctionalArea = "mcp"
	AiInference      FunctionalArea = "ai-inference"
	SparkplugIngest  FunctionalArea = "sparkplug-ingest"
	Lwm2mIngest      FunctionalArea = "lwm2m-ingest"
)

// Manifest is a functional area's deployment contract (ADR-022 decision 2): its
// dependencies on other areas, classified Hard (cannot function without — the
// gate rejects an enabled set missing one) vs Soft (degrades or sits idle
// without — always allowed, made safe by NATS pub/sub, ADR-003). Core marks the
// required base every deployment runs: event resolution (device-management) and
// auth (user-management).
//
// This deliberately carries no Produces/Consumes subject lists. It used to. They
// were read by nothing — the gate has always worked off HardDeps/SoftDeps — and
// as unenforced documentation they had silently drifted to naming 7 of the
// platform's streams, while still reading as an authoritative wiring map. The
// stream set has one home now, core/streams; a second enumeration here could only
// ever drift away from it again.
type Manifest struct {
	Area     FunctionalArea
	Core     bool
	HardDeps []FunctionalArea
	SoftDeps []FunctionalArea
}

// catalog is the authoritative manifest set. Inter-area messaging flows on NATS
// (ADR-003), so a producer never depends on its consumers (an absent consumer is
// safe by construction); the Hard edges are the reverse — a consumer that is
// functionally dead without the area producing what it consumes. user-management
// is a Soft dep everywhere because auth degrades rather than failing a peer's
// startup (ADR-022 decision 3), but it is Core so every profile includes it.
var catalog = map[FunctionalArea]Manifest{
	UserManagement: {
		Area: UserManagement,
		Core: true,
	},
	DeviceManagement: {
		Area:     DeviceManagement,
		Core:     true,
		SoftDeps: []FunctionalArea{UserManagement, EventSources},
	},
	EventSources: {
		Area:     EventSources,
		HardDeps: []FunctionalArea{DeviceManagement},
		SoftDeps: []FunctionalArea{UserManagement},
	},
	EventManagement: {
		Area:     EventManagement,
		HardDeps: []FunctionalArea{DeviceManagement},
		SoftDeps: []FunctionalArea{UserManagement},
	},
	DeviceState: {
		Area:     DeviceState,
		HardDeps: []FunctionalArea{DeviceManagement},
		SoftDeps: []FunctionalArea{UserManagement},
	},
	DashboardMgmt: {
		// Dashboard-definition CRUD (ADR-039). It reads no messaging subjects — the
		// live telemetry a dashboard renders is subscribed straight from
		// event-management by the client Hub — but a dashboard's selectors reference
		// devices/anchors, so it is functionally dead without device-management.
		Area:     DashboardMgmt,
		HardDeps: []FunctionalArea{DeviceManagement},
		SoftDeps: []FunctionalArea{UserManagement},
	},
	CommandDelivery: {
		Area:     CommandDelivery,
		HardDeps: []FunctionalArea{DeviceManagement},
		SoftDeps: []FunctionalArea{UserManagement},
	},
	NotificationMgmt: {
		// The alarm→human last mile (ADR-017): a durable consumer of the alarm-events
		// envelope (ADR-041, produced by device-management) that fans transitions to
		// notification channels. Functionally dead without the area producing what it
		// consumes, so device-management is a Hard dep.
		Area:     NotificationMgmt,
		HardDeps: []FunctionalArea{DeviceManagement},
		SoftDeps: []FunctionalArea{UserManagement},
	},
	EventProcessing: {
		// The DETECT + REACT pipeline (ADR-051): a durable consumer of the
		// resolved-events stream (produced by device-management) that detects
		// conditions and dispatches actions. Functionally dead without the area
		// producing what it consumes, so device-management is a Hard dep.
		Area:     EventProcessing,
		HardDeps: []FunctionalArea{DeviceManagement},
		SoftDeps: []FunctionalArea{UserManagement},
	},
	OutboundConn: {
		// The outbound-connectors service (ADR-060): a durable consumer of the
		// connector-dispatch stream (produced by event-processing's REACT dispatcher)
		// that executes each fired httpCall/publish action. Functionally dead without the
		// area producing what it consumes, so event-processing is a Hard dep (which in turn
		// hard-deps device-management). It produces only a terminal dead-letter subject
		// (consumed by nothing). Like mcp it is held back from ProfileDefault — outbound
		// connectors open an EGRESS surface, so a deployment opts in deliberately, either
		// via ProfileFull or by naming it in enabledFunctionalAreas.
		Area:     OutboundConn,
		HardDeps: []FunctionalArea{EventProcessing},
		SoftDeps: []FunctionalArea{UserManagement},
	},
	Mcp: {
		// The MCP server (ADR-047): an OAuth 2.1 Resource Server fronting the per-area
		// GraphQL over the Model Context Protocol. It is synchronous (no messaging), so
		// it produces/consumes nothing on NATS. Its read tools resolve devices, alarms,
		// and capabilities, so it is functionally dead without device-management (Hard);
		// the telemetry/state/command tools degrade per-tool without their areas (Soft).
		// Auth is a Soft dep (the token validator degrades, ADR-022 decision 3). It is held
		// back from ProfileDefault — an AGENT-FACING API is a deliberate choice — so a
		// deployment opts in via ProfileFull or by naming it in enabledFunctionalAreas. Its
		// required resource + issuer URLs are derived from the ingress by the chart, but a
		// client can only obtain a token once the OAuth AS it fronts is enabled.
		Area:     Mcp,
		HardDeps: []FunctionalArea{DeviceManagement},
		SoftDeps: []FunctionalArea{UserManagement, EventManagement, DeviceState, CommandDelivery},
	},
	AiInference: {
		// The ai-inference service (ADR-056): the fail-closed inference seam for NL→rule
		// authoring. It is synchronous (no messaging), so it produces/consumes nothing on
		// NATS — event-processing CALLS it over a service token (slice 0c), the reverse of
		// a stream dependency, so it has no Hard deps. It reads the per-tenant external-AI
		// consent flag from user-management (Soft — the read degrades fail-closed when
		// absent). Like mcp/outbound-connectors it is held back from ProfileDefault: NL
		// authoring reaches an EXTERNAL provider on a paid key, so a deployment opts in
		// deliberately, either via ProfileFull or by naming it in enabledFunctionalAreas.
		Area:     AiInference,
		SoftDeps: []FunctionalArea{UserManagement},
	},
	SparkplugIngest: {
		// The Sparkplug ingest service (ADR-069): a stateful Sparkplug B Host
		// Application that terminates edge-node telemetry over the MQTT gateway onto
		// inbound-events (the ingest path lands in SP3; SP1 decodes and logs) — a
		// parallel inbound transport to event-sources, so like it it is functionally
		// dead without the area that RESOLVES what it produces (device-management,
		// Hard, its designed contract). It is an OPT-IN edge
		// area — held back from ProfileDefault (a Sparkplug deployment is a
		// deliberate topology choice, and until the ADR-070 lease lands it runs as a
		// single non-HA instance), shipped by ProfileFull, or named explicitly in
		// enabledFunctionalAreas. It produces onto inbound-events and consumes
		// nothing, so it declares no consumer of its own as a Hard edge.
		Area:     SparkplugIngest,
		HardDeps: []FunctionalArea{DeviceManagement},
		SoftDeps: []FunctionalArea{UserManagement},
	},
	Lwm2mIngest: {
		// The LwM2M ingest service (ADR-075): a stateful adapter that terminates OMA
		// LwM2M over CoAP/UDP+DTLS from constrained devices onto the one device model —
		// the second standards-native edge protocol alongside Sparkplug. Like the
		// Sparkplug adapter it is a parallel inbound transport that RESOLVES what it
		// produces through device-management (Hard, its designed contract, wired when the
		// registration/measurement handlers land in L1). It is an OPT-IN edge area held
		// back from ProfileDefault — a deliberate topology choice, and (like Sparkplug)
		// it runs as a single inbound-UDP binder (ADR-075 HA posture: an inbound socket
		// cannot warm-standby; GA is one serving replica). L1 wires the /rd registration +
		// presence handlers and exposes the device-facing CoAPS/UDP port (5684); it
		// consumes nothing, so it declares no consumer of its own as a Hard edge.
		Area:     Lwm2mIngest,
		HardDeps: []FunctionalArea{DeviceManagement},
		SoftDeps: []FunctionalArea{UserManagement},
	},
}

// Profile names a curated, valid enabled set (ADR-022 decision 2). Every profile
// includes the Core areas.
type Profile string

const (
	// ProfileDefault is the standard instance: the whole device/telemetry/automation
	// system, and what an unset selection resolves to. It omits the areas that reach
	// OUTSIDE the instance — AI inference, outbound connectors, and MCP — because each
	// carries a decision an operator should make deliberately (a paid provider key, an
	// egress surface, an agent-facing API), not inherit from a default.
	ProfileDefault Profile = "default"
	// ProfileFull is literally everything this build ships, including the areas
	// ProfileDefault holds back. Its contract is that it stays exhaustive: a new area
	// belongs here unless there is a reason it cannot be, so "full" never again drifts
	// into meaning "most of it".
	ProfileFull Profile = "full"
	// ProfileTelemetry is ingest → resolve → persist + live state, without the
	// command path.
	ProfileTelemetry Profile = "telemetry"
	// ProfileIngestOnly is the minimal resolve pipeline: ingest + resolution, with
	// no persistence, state, or command delivery.
	ProfileIngestOnly Profile = "ingest-only"
)

// standardAreas is ProfileDefault's set, shared so ProfileFull is expressed as
// "the standard system plus the rest" rather than a second hand-maintained list
// that could silently disagree with it.
var standardAreas = []FunctionalArea{
	UserManagement, DeviceManagement, EventSources,
	EventManagement, DeviceState, DashboardMgmt, CommandDelivery,
	NotificationMgmt, EventProcessing,
}

var profiles = map[Profile][]FunctionalArea{
	ProfileDefault: standardAreas,
	ProfileFull:    append(append([]FunctionalArea{}, standardAreas...), AiInference, OutboundConn, Mcp, SparkplugIngest, Lwm2mIngest),
	ProfileTelemetry: {
		UserManagement, DeviceManagement, EventSources, EventManagement, DeviceState, DashboardMgmt,
	},
	ProfileIngestOnly: {
		UserManagement, DeviceManagement, EventSources,
	},
}

// Known reports whether the named area is in the catalog.
func Known(area FunctionalArea) bool {
	_, ok := catalog[area]
	return ok
}

// ManifestFor returns the manifest for an area and whether it exists.
func ManifestFor(area FunctionalArea) (Manifest, bool) {
	m, ok := catalog[area]
	return m, ok
}

// CoreAreas returns the areas every deployment must run (sorted for determinism).
func CoreAreas() []FunctionalArea {
	core := make([]FunctionalArea, 0, 2)
	for area, m := range catalog {
		if m.Core {
			core = append(core, area)
		}
	}
	sortAreas(core)
	return core
}

// ResolveEnabled turns an Instance's declared deployment intent into a concrete
// enabled set (ADR-022 decision 2). Exactly one of profile or explicit may be
// given; an empty intent resolves to ProfileDefault — the standard system, NOT
// ProfileFull, which would silently deploy the areas that reach outside the instance
// to anyone who declared no intent at all. This must stay in step with the chart's
// own empty-selection fallback (_helpers.tpl), which resolves the same way.
// The result is not yet validated — callers run Validate on it.
func ResolveEnabled(profile string, explicit []FunctionalArea) ([]FunctionalArea, error) {
	hasProfile := strings.TrimSpace(profile) != ""
	hasExplicit := len(explicit) > 0
	switch {
	case hasProfile && hasExplicit:
		return nil, fmt.Errorf("set either profile or enabledFunctionalAreas, not both")
	case hasProfile:
		areas, ok := profiles[Profile(profile)]
		if !ok {
			return nil, fmt.Errorf("unknown profile %q (known: %s)", profile, joinProfiles())
		}
		return append([]FunctionalArea(nil), areas...), nil
	case hasExplicit:
		return append([]FunctionalArea(nil), explicit...), nil
	default:
		return append([]FunctionalArea(nil), profiles[ProfileDefault]...), nil
	}
}

// Validate checks that an enabled set is internally consistent (ADR-022 decision
// 2): every area is known, the Core areas are all present, and every enabled
// area's Hard dependencies are also enabled. Soft dependencies are never
// enforced — pub/sub makes an absent peer safe. It returns a single error
// describing the first violation in a deterministic order.
func Validate(enabled []FunctionalArea) error {
	set := make(map[FunctionalArea]bool, len(enabled))
	for _, area := range enabled {
		if !Known(area) {
			return fmt.Errorf("unknown functional area %q", area)
		}
		set[area] = true
	}

	for _, core := range CoreAreas() {
		if !set[core] {
			return fmt.Errorf("required core functional area %q is not enabled", core)
		}
	}

	// Check hard deps in a deterministic order over the enabled set.
	ordered := append([]FunctionalArea(nil), enabled...)
	sortAreas(ordered)
	for _, area := range ordered {
		for _, dep := range catalog[area].HardDeps {
			if !set[dep] {
				return fmt.Errorf("functional area %q requires %q, which is not enabled", area, dep)
			}
		}
	}
	return nil
}

// ResolveAndValidate is the convenience the deployment gate uses: resolve intent
// then validate, returning the concrete enabled set or the first error.
func ResolveAndValidate(profile string, explicit []FunctionalArea) ([]FunctionalArea, error) {
	enabled, err := ResolveEnabled(profile, explicit)
	if err != nil {
		return nil, err
	}
	if err := Validate(enabled); err != nil {
		return nil, err
	}
	return enabled, nil
}

func sortAreas(areas []FunctionalArea) {
	sort.Slice(areas, func(i, j int) bool { return areas[i] < areas[j] })
}

func joinProfiles() string {
	names := make([]string, 0, len(profiles))
	for p := range profiles {
		names = append(names, string(p))
	}
	sort.Strings(names)
	return strings.Join(names, ", ")
}
