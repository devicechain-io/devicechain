// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

using System;
using System.Collections.Generic;
using System.Globalization;
using System.Net.Http;
using System.Text;
using System.Text.Json;
using System.Text.Json.Serialization.Metadata;
using System.Threading;
using System.Threading.Tasks;
using DeviceChain.Sdk.Json;
using DeviceChain.Sdk.Transport;

namespace DeviceChain.Sdk.Ingest;

/// <summary>
/// Publishes telemetry over the DEVICE-plane HTTP ingress — <c>POST /{instanceId}/{tenant}/events</c>
/// — exactly as a real device does (ADR-025/048): the device authenticates by presenting its
/// credential IN the body (credentialType + credentialId, ADR-014), not a Bearer header. This is
/// the emit leg an interactive twin uses (contract §3c). The ingress accepts into the pipeline
/// asynchronously and returns 202.
/// </summary>
public sealed class DeviceEventPublisher
{
    /// <summary>The device-credential type a token-authenticated device presents (matches the sim + event-sources).</summary>
    public const string AccessTokenCredential = "ACCESS_TOKEN";

    private readonly IDeviceEventCarrier _carrier;

    /// <param name="transport">The HTTP transport (default <see cref="HttpClientTransport"/>; Unity WebGL injects its own).</param>
    /// <param name="ingressOrigin">
    /// The device-plane ingress origin. This is a SEPARATE listener from the GraphQL/`/api` ingress
    /// (event-sources' HTTP port) — the cluster ingress does not route `/{instanceId}/{tenant}/events`,
    /// so this must point at wherever that endpoint is exposed, not the GraphQL origin.
    /// </param>
    /// <param name="instanceId">The instance segment (ADR-048).</param>
    /// <param name="tenant">The tenant segment.</param>
    public DeviceEventPublisher(IHttpTransport transport, Uri ingressOrigin, string instanceId, string tenant)
        : this(new HttpDeviceEventCarrier(transport, ingressOrigin, instanceId, tenant))
    {
    }

    /// <summary>
    /// Publishes over an explicit carrier — HTTP (the default above) or MQTT.
    /// </summary>
    /// <remarks>
    /// The publisher builds the body once and hands it to whichever carrier moves it, so the two
    /// wires cannot drift. Only the MQTT carrier reaches the durability capture stream.
    /// </remarks>
    /// <param name="carrier">The carrier that moves the serialized event.</param>
    public DeviceEventPublisher(IDeviceEventCarrier carrier)
    {
        _carrier = carrier ?? throw new ArgumentNullException(nameof(carrier));
    }

    /// <summary>Convenience overload for the plain-.NET path: wraps a shared <see cref="HttpClient"/>.</summary>
    /// <param name="http">A shared HttpClient (BaseAddress not read/mutated — absolute URIs are built).</param>
    /// <param name="ingressOrigin">The device-plane ingress origin (see the transport overload).</param>
    /// <param name="instanceId">The instance segment (ADR-048).</param>
    /// <param name="tenant">The tenant segment.</param>
    public DeviceEventPublisher(HttpClient http, Uri ingressOrigin, string instanceId, string tenant)
        : this(new HttpClientTransport(http), ingressOrigin, instanceId, tenant)
    {
    }

    /// <summary>
    /// Emits one Measurement event carrying every entry in <paramref name="metrics"/> as a single
    /// measurements map (the rich-emit shape). Values go on the wire as invariant strings
    /// (self-describing decode downstream, ADR-016). Throws <see cref="GraphQlRequestException"/>
    /// (as the SDK's transport error) on a non-202 response.
    /// </summary>
    public Task EmitMeasurementsAsync(
        string deviceToken,
        string credentialId,
        IReadOnlyDictionary<string, double> metrics,
        DateTimeOffset? occurredTime = null,
        CancellationToken cancellationToken = default)
    {
        if (string.IsNullOrEmpty(deviceToken)) throw new ArgumentException("device token required", nameof(deviceToken));
        if (string.IsNullOrEmpty(credentialId)) throw new ArgumentException("credential id required", nameof(credentialId));

        string now = FormatRfc3339(occurredTime ?? DateTimeOffset.UtcNow);
        var values = new Dictionary<string, string>(metrics.Count);
        foreach (KeyValuePair<string, double> m in metrics)
        {
            values[m.Key] = m.Value.ToString(CultureInfo.InvariantCulture);
        }

        var evt = new MeasurementEvent
        {
            Device = deviceToken,
            EventType = "Measurement",
            OccurredTime = now,
            Payload = new MeasurementPayload
            {
                Entries = { new MeasurementEntry { Measurements = values, OccurredTime = now } },
            },
            CredentialType = AccessTokenCredential,
            CredentialId = credentialId,
        };
        return SendAsync(deviceToken, evt, SdkJson.Default.MeasurementEvent, cancellationToken);
    }

    /// <summary>
    /// Emits one Location event carrying exactly one position report. Coordinates go on the wire as
    /// invariant strings in the platform's fixed units (see <see cref="LocationFix"/>); an
    /// unreported optional is omitted from the JSON entirely rather than sent as zero. Throws
    /// <see cref="ArgumentOutOfRangeException"/> for a value the platform would refuse, and
    /// <see cref="GraphQlRequestException"/> (as the SDK's transport error) on a non-202 response.
    /// </summary>
    /// <remarks>
    /// ONE ENTRY PER EVENT, and batching is a trap rather than an optimization: the per-entry
    /// occurred time is discarded at persistence, so every entry in a batch lands at the envelope's
    /// time — after which two identical positions in that batch collide on their payload id and one
    /// is dropped. A batch is lossy in a way a single entry cannot be.
    /// </remarks>
    /// <param name="deviceToken">The device the fix belongs to.</param>
    /// <param name="credentialId">The device credential id presented in the body.</param>
    /// <param name="fix">Where the device is.</param>
    /// <param name="occurredTime">When the fix was taken (defaults to now, UTC).</param>
    /// <param name="cancellationToken">Cancels the send.</param>
    public Task EmitLocationAsync(
        string deviceToken,
        string credentialId,
        LocationFix fix,
        DateTimeOffset? occurredTime = null,
        CancellationToken cancellationToken = default)
    {
        if (string.IsNullOrEmpty(deviceToken)) throw new ArgumentException("device token required", nameof(deviceToken));
        if (string.IsNullOrEmpty(credentialId)) throw new ArgumentException("credential id required", nameof(credentialId));

        string now = FormatRfc3339(occurredTime ?? DateTimeOffset.UtcNow);
        var evt = new LocationEvent
        {
            Device = deviceToken,
            EventType = "Location",
            OccurredTime = now,
            Payload = new LocationPayload
            {
                Entries =
                {
                    new LocationEntry
                    {
                        Latitude = Checked(fix.Latitude, -MaxAbsLatitude, MaxAbsLatitude, false, "latitude"),
                        Longitude = Checked(fix.Longitude, -MaxAbsLongitude, MaxAbsLongitude, false, "longitude"),
                        // 🔴 ABSENT, not "0". A null optional stays null all the way to the
                        // serializer, whose context ignores nulls when writing.
                        Elevation = Checked(fix.Elevation, -MaxAbsMetres, MaxAbsMetres, false, "elevation"),
                        Accuracy = Checked(fix.Accuracy, 0, MaxAbsMetres, false, "accuracy"),
                        Speed = Checked(fix.Speed, 0, MaxAbsMetres, false, "speed"),
                        Heading = Checked(fix.Heading, 0, MaxHeadingExclusive, true, "heading"),
                        OccurredTime = now,
                    },
                },
            },
            CredentialType = AccessTokenCredential,
            CredentialId = credentialId,
        };
        return SendAsync(deviceToken, evt, SdkJson.Default.LocationEvent, cancellationToken);
    }

    // The body is built HERE, once, and handed to whichever carrier moves it — which is what makes
    // "identical payload, different carrier" true by construction rather than by discipline. The
    // equality is pinned by a test that captures what reached each carrier and compares the bytes.
    //
    // Generic over the serializer's JsonTypeInfo rather than over a shared base type, so every event
    // kind reaches the wire through this ONE path while still being serialized by the SOURCE
    // GENERATOR — no reflection-based overload, which the AOT analyzer would refuse outright
    // (IsAotCompatible + TreatWarningsAsErrors makes that a build error, not a warning).
    private Task SendAsync<TEvent>(
        string deviceToken,
        TEvent evt,
        JsonTypeInfo<TEvent> typeInfo,
        CancellationToken cancellationToken) =>
        _carrier.SendAsync(
            deviceToken,
            JsonSerializer.SerializeToUtf8Bytes(evt, typeInfo),
            cancellationToken);

    // RFC3339 in UTC WITH fractional seconds ("O" is .NET's round-trip form: always Z,
    // always seven fractional digits, and Go's time.Parse accepts it).
    //
    // 🔴 The fractional part is load-bearing, not cosmetic, and this previously emitted
    // whole seconds. A base event is keyed by (tenant, device, event_type, occurred_time),
    // so two readings this SDK published inside one wall-clock second carried an identical
    // key — and the second one's envelope was silently discarded on insert. A device
    // sampling faster than once per second lost every reading after the first in each
    // second, and lost it quietly: the publish returned 202.
    //
    // The old comment justified the truncation as matching "the Go sim's time.RFC3339
    // emit". That had stopped being true — sims/dc-simulator/sim/emit.go moved to
    // RFC3339Nano precisely because second-identical emits were collapsing, and its
    // comment explains the mechanism in full. The simulator was corrected; the SDK real
    // devices actually use was not. Emitting sub-second time is also just honest: a device
    // sampling sub-second HAS a sub-second timestamp.
    private static string FormatRfc3339(DateTimeOffset when) =>
        when.UtcDateTime.ToString("O", CultureInfo.InvariantCulture);

    // ---- The coordinate contract, mirrored client-side ----------------------------------------
    //
    // These bounds are the platform's, restated here on purpose. A device SDK that lets a caller
    // ship an out-of-range coordinate has not avoided the failure, it has MOVED it: the event is
    // refused at decode, dead-lettered on the broker path or 400'd on HTTP, and the caller learns
    // nothing it can act on. The most common mistake is degrees scaled by 1e7 (the NMEA/LwM2M
    // convention), and 337490000 is not a latitude at any scale.
    private const double MaxAbsLatitude = 90.0;
    private const double MaxAbsLongitude = 180.0;

    // elevation, accuracy and speed land in decimal(12,4) — eight digits before the point. The
    // bound is a whole metre inside the true ceiling because excess SCALE rounds silently where
    // excess integer digits raise: 99999999.99999 is under 99999999.9999 as a double and would
    // round UP to 100000000.0000 at the column, reintroducing the overflow the check exists to
    // prevent.
    private const double MaxAbsMetres = 99_999_999.0;

    // heading is a compass bearing in [0, 360) — 360 is the same bearing as 0, and storing both
    // spellings of north is how a consumer ends up with two headings that compare unequal. The
    // bound sits half a rounding quantum inside 360 for the same reason as the metre ceiling: the
    // column stores four decimals and ROUNDS, so 359.99999 passes an at-the-boundary check and is
    // then stored as exactly 360.0000. Use LocationFix.CanonicalHeading to fold a raw bearing.
    private const double HeadingQuantum = 0.0001;
    private const double MaxHeadingExclusive = 360.0 - (HeadingQuantum / 2);

    private static string Checked(double value, double min, double max, bool maxExclusive, string field)
    {
        // NaN fails every comparison, so it would pass a naive range check — and it formats as
        // "NaN", which is not a number to the decoder either.
        if (double.IsNaN(value) || double.IsInfinity(value))
        {
            throw new ArgumentOutOfRangeException(
                $"fix.{field}", value, $"{field} must be a finite number");
        }

        bool tooBig = maxExclusive ? value >= max : value > max;
        if (value < min || tooBig)
        {
            throw new ArgumentOutOfRangeException(
                $"fix.{field}", value,
                $"{field} must be in [{min.ToString(CultureInfo.InvariantCulture)}, " +
                $"{max.ToString(CultureInfo.InvariantCulture)}{(maxExclusive ? ")" : "]")}");
        }

        return FormatFixValue(value);
    }

    // The nullable overload is what keeps "unreported" expressible end to end: null in, null out,
    // and the serializer context drops it from the JSON. It never substitutes a zero.
    private static string? Checked(double? value, double min, double max, bool maxExclusive, string field) =>
        value.HasValue ? Checked(value.Value, min, max, maxExclusive, field) : null;

    /// <summary>Renders a coordinate component as the decimal string the wire carries.</summary>
    /// <remarks>
    /// Shortest round-trippable form, then EXPANDED out of exponent notation if it landed there.
    /// The expansion is not cosmetic tidying: .NET formats 1e-7 as <c>"1E-07"</c>, which the
    /// platform's parser accepts happily — so the value is CORRECT and unreadable. A latitude that
    /// reaches a log, a dead-letter or a debugger as <c>1E-07</c> reads as a bug in the producer,
    /// and someone spends an afternoon on it. Fixed-decimal custom formats are not an alternative:
    /// they cap at 15 significant digits, so <c>33.74912345678901</c> silently loses its last two —
    /// a metre-scale error introduced by the formatter, measured, not assumed.
    /// </remarks>
    internal static string FormatFixValue(double value)
    {
        string rendered = value.ToString("R", CultureInfo.InvariantCulture);
        int marker = rendered.IndexOf('E');
        if (marker < 0)
        {
            marker = rendered.IndexOf('e');
        }
        if (marker < 0)
        {
            return rendered;
        }

        string mantissa = rendered.Substring(0, marker);
        int exponent = int.Parse(
            rendered.Substring(marker + 1), NumberStyles.AllowLeadingSign, CultureInfo.InvariantCulture);

        string sign = "";
        if (mantissa.Length > 0 && (mantissa[0] == '-' || mantissa[0] == '+'))
        {
            sign = mantissa[0] == '-' ? "-" : "";
            mantissa = mantissa.Substring(1);
        }

        int point = mantissa.IndexOf('.');
        string digits;
        if (point < 0)
        {
            digits = mantissa;
            point = mantissa.Length;
        }
        else
        {
            digits = mantissa.Substring(0, point) + mantissa.Substring(point + 1);
        }

        point += exponent;
        if (point <= 0)
        {
            return sign + "0." + new string('0', -point) + digits;
        }
        if (point >= digits.Length)
        {
            return sign + digits + new string('0', point - digits.Length);
        }
        return sign + digits.Substring(0, point) + "." + digits.Substring(point);
    }
}
