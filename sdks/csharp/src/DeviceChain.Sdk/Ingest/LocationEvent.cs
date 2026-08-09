// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

using System.Collections.Generic;

namespace DeviceChain.Sdk.Ingest;

// The Location half of the device-plane event body (event-sources' JsonEvent). Same envelope
// as MeasurementEvent — only the payload differs — so both go on the wire through the one
// build-once-then-hand-to-a-carrier path in DeviceEventPublisher.

/// <summary>One position report at a point in time (the payload's <c>entries[i]</c>).</summary>
/// <remarks>
/// <para>
/// 🔴 EVERY VALUE IS A STRING ON THE WIRE, INCLUDING THE NUMBERS. The decoder's entry type holds
/// a string per field, so a bare JSON number (<c>33.749</c> rather than <c>"33.749"</c>) does not
/// unmarshal and the WHOLE event fails to decode — not just the offending field.
/// </para>
/// <para>
/// <see cref="Latitude"/> and <see cref="Longitude"/> are non-nullable because the platform
/// REQUIRES them: an entry without them is refused, not stored blank. The other four are nullable
/// because "not reported" and "reported as zero" are different facts about a device, and only
/// null can carry the difference — a machine with no compass has not reported due north. The
/// serializer context ignores nulls when writing, so an unreported optional is ABSENT from the
/// JSON rather than sent as <c>"0"</c>.
/// </para>
/// </remarks>
internal sealed class LocationEntry
{
    public string Latitude { get; set; } = "";
    public string Longitude { get; set; } = "";
    public string? Elevation { get; set; }
    public string? Accuracy { get; set; }
    public string? Speed { get; set; }
    public string? Heading { get; set; }
    public string? OccurredTime { get; set; }
}

/// <summary>A Location event's payload.</summary>
/// <remarks>
/// The <c>entries</c> wrapper is mandatory: a payload carrying its fields directly, with no
/// wrapper, decodes to ZERO entries — which the platform refuses rather than accepting and
/// storing nothing.
/// </remarks>
internal sealed class LocationPayload
{
    public List<LocationEntry> Entries { get; set; } = new();
}

/// <summary>A device-plane Location event (serializes to event-sources' JsonEvent).</summary>
internal sealed class LocationEvent
{
    public string Device { get; set; } = "";
    public string EventType { get; set; } = "Location";
    public string? OccurredTime { get; set; }
    public LocationPayload Payload { get; set; } = new();
    public string? CredentialType { get; set; }
    public string? CredentialId { get; set; }
    public string? CredentialSecret { get; set; }
}
