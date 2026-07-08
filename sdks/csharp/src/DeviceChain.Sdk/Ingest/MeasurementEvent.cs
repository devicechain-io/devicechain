// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

using System.Collections.Generic;

namespace DeviceChain.Sdk.Ingest;

// The device-plane event body (event-sources' JsonEvent, ADR-014/025): a device
// authenticates by presenting its credential IN the body (credentialType + credentialId),
// not a Bearer header — the ingress resolver authenticates the device rather than trusting
// the bare token. These concrete types serialize to the exact wire shape a real device (and
// the Go sim's emit.go) posts, so a twin emits with no sim-only backdoor.

/// <summary>One measurement reading set at a point in time (the payload's <c>entries[i]</c>).</summary>
internal sealed class MeasurementEntry
{
    // Values are strings on the wire (self-describing decode downstream, ADR-016).
    public Dictionary<string, string> Measurements { get; set; } = new();
    public string? OccurredTime { get; set; }
}

/// <summary>A Measurement event's payload: one or more entries.</summary>
internal sealed class MeasurementPayload
{
    public List<MeasurementEntry> Entries { get; set; } = new();
}

/// <summary>A device-plane Measurement event (serializes to event-sources' JsonEvent).</summary>
internal sealed class MeasurementEvent
{
    public string Device { get; set; } = "";
    public string EventType { get; set; } = "Measurement";
    public string? OccurredTime { get; set; }
    public MeasurementPayload Payload { get; set; } = new();
    public string? CredentialType { get; set; }
    public string? CredentialId { get; set; }
    public string? CredentialSecret { get; set; }
}
