// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

using System;
using System.Net;
using System.Net.Http;
using System.Text.Json;
using System.Threading.Tasks;
using DeviceChain.Sdk.Ingest;
using Xunit;

namespace DeviceChain.Sdk.Tests;

// The Location emit leg. The location spine had NO producer in this SDK at all until now, and a
// wire shape with no producer is a shape nobody has ever had to be right about — so these assert
// the emitted bytes field by field rather than round-tripping through a deserializer, which would
// only prove the SDK agrees with itself.
public class DeviceLocationPublisherTests
{
    private static readonly DateTimeOffset Occurred = new(2026, 8, 7, 12, 0, 0, TimeSpan.Zero);
    private const string OccurredJson = "2026-08-07T12:00:00.0000000Z";

    private static DeviceEventPublisher Publisher(StubHandler handler)
    {
        var http = new HttpClient(handler);
        return new DeviceEventPublisher(http, new Uri("https://ingress.local"), "dc", "acme");
    }

    private static StubHandler Accepting() => new((_, _) => (HttpStatusCode.Accepted, ""));

    private static JsonElement Entry(StubHandler.Call call)
    {
        using JsonDocument document = JsonDocument.Parse(call.Body);
        JsonElement entries = document.RootElement.GetProperty("payload").GetProperty("entries");
        Assert.Equal(JsonValueKind.Array, entries.ValueKind);
        // ONE entry per event: the per-entry occurred time is discarded at persistence, so a batch
        // collapses onto the envelope's time and identical positions in it collide and are dropped.
        Assert.Equal(1, entries.GetArrayLength());
        return entries[0].Clone();
    }

    // Every value a JSON STRING, including the numbers — the decoder's entry type holds a string
    // per field, so a bare number fails the WHOLE decode, not just its own field. Values chosen
    // deliberately non-round so a swapped pair cannot pass.
    [Fact]
    public async Task Emits_the_location_wire_shape_with_every_value_as_a_json_string()
    {
        StubHandler handler = Accepting();
        DeviceEventPublisher pub = Publisher(handler);

        var fix = new LocationFix(33.74912345, -84.38812345)
        {
            Elevation = 320.53,
            Accuracy = 4.27,
            Speed = 1.83,
            Heading = 271.53,
        };
        await pub.EmitLocationAsync("dev-1", "cred-9", fix, Occurred);

        StubHandler.Call call = Assert.Single(handler.Calls);
        Assert.Equal("/dc/acme/events", call.Path);

        using JsonDocument document = JsonDocument.Parse(call.Body);
        JsonElement root = document.RootElement;
        Assert.Equal("dev-1", root.GetProperty("device").GetString());
        Assert.Equal("Location", root.GetProperty("eventType").GetString());
        Assert.Equal("ACCESS_TOKEN", root.GetProperty("credentialType").GetString());
        Assert.Equal("cred-9", root.GetProperty("credentialId").GetString());
        Assert.Equal(OccurredJson, root.GetProperty("occurredTime").GetString());
        Assert.False(root.TryGetProperty("credentialSecret", out _));

        JsonElement entry = Entry(call);
        foreach ((string field, string expected) in new[]
                 {
                     ("latitude", "33.74912345"),
                     ("longitude", "-84.38812345"),
                     ("elevation", "320.53"),
                     ("accuracy", "4.27"),
                     ("speed", "1.83"),
                     ("heading", "271.53"),
                     ("occurredTime", OccurredJson),
                 })
        {
            JsonElement value = entry.GetProperty(field);
            Assert.Equal(JsonValueKind.String, value.ValueKind);
            Assert.Equal(expected, value.GetString());
        }
    }

    // 🔴 A COORDINATE MUST NOT REACH THE WIRE AS A JSON NUMBER. Asserted on the raw text as well as
    // the value kind, because a deserializer would happily coerce either.
    [Fact]
    public async Task Coordinates_are_quoted_on_the_wire_not_bare_numbers()
    {
        StubHandler handler = Accepting();
        await Publisher(handler).EmitLocationAsync("dev-1", "cred-9", new LocationFix(33.749, -84.388), Occurred);

        string body = Assert.Single(handler.Calls).Body;
        Assert.Contains("\"latitude\":\"33.749\"", body);
        Assert.Contains("\"longitude\":\"-84.388\"", body);
        Assert.DoesNotContain("\"latitude\":33.749", body);
        Assert.DoesNotContain("\"longitude\":-84.388", body);
    }

    // 🔴 AN UNREPORTED OPTIONAL IS ABSENT FROM THE JSON, NOT "0". Asserted on the raw KEYS: a
    // deserialized object cannot tell "absent" from "present and zero", which is precisely the
    // distinction under test. A stored zero heading is indistinguishable from a measured one, so a
    // placeholder turns "this machine has no compass" into "this machine points due north".
    [Fact]
    public async Task An_unreported_optional_is_absent_from_the_json_rather_than_zero()
    {
        StubHandler handler = Accepting();
        await Publisher(handler).EmitLocationAsync("dev-1", "cred-9", new LocationFix(33.749, -84.388), Occurred);

        StubHandler.Call call = Assert.Single(handler.Calls);
        JsonElement entry = Entry(call);
        foreach (string field in new[] { "elevation", "accuracy", "speed", "heading" })
        {
            Assert.False(entry.TryGetProperty(field, out _), $"{field} must be absent when unreported");
            Assert.DoesNotContain($"\"{field}\"", call.Body);
        }

        Assert.Equal("33.749", entry.GetProperty("latitude").GetString());
        Assert.Equal("-84.388", entry.GetProperty("longitude").GetString());
    }

    // The counterweight to the test above: an optional that WAS measured as zero is a fact the
    // device reported and must be on the wire. A publisher that dropped every zero would satisfy
    // the absence test completely while losing "stationary" and "due north".
    [Fact]
    public async Task A_measured_zero_optional_is_emitted_rather_than_dropped()
    {
        StubHandler handler = Accepting();
        var fix = new LocationFix(33.749, -84.388) { Speed = 0, Heading = 0, Accuracy = 0, Elevation = 0 };
        await Publisher(handler).EmitLocationAsync("dev-1", "cred-9", fix, Occurred);

        JsonElement entry = Entry(Assert.Single(handler.Calls));
        foreach (string field in new[] { "elevation", "accuracy", "speed", "heading" })
        {
            Assert.Equal("0", entry.GetProperty(field).GetString());
        }
    }

    // The entries wrapper is mandatory: a payload carrying its fields directly decodes to ZERO
    // entries, which the platform refuses. This asserts the wrapper exists AND that nothing leaked
    // out of it — a flat body would fail both halves.
    [Fact]
    public async Task The_content_lives_under_the_entries_wrapper()
    {
        StubHandler handler = Accepting();
        await Publisher(handler).EmitLocationAsync("dev-1", "cred-9", new LocationFix(33.749, -84.388), Occurred);

        using JsonDocument document = JsonDocument.Parse(Assert.Single(handler.Calls).Body);
        JsonElement payload = document.RootElement.GetProperty("payload");
        Assert.False(payload.TryGetProperty("latitude", out _));
        Assert.False(payload.TryGetProperty("longitude", out _));
        JsonElement entries = payload.GetProperty("entries");
        Assert.Equal(JsonValueKind.Array, entries.ValueKind);
        Assert.Equal(1, entries.GetArrayLength());
        Assert.Equal("33.749", entries[0].GetProperty("latitude").GetString());
    }

    // 🔴 NO EXPONENT NOTATION. .NET renders 1e-7 as "1E-07", which the platform's parser accepts
    // happily — so the value would be CORRECT and unreadable, and would read as a producer bug in
    // every log, dead-letter and debugger it reached. The first assertion is the negative control:
    // it pins that .NET really does produce the exponent form for these inputs, so the rest is
    // measured against a hazard that is actually there.
    [Fact]
    public async Task Small_magnitudes_are_emitted_in_plain_decimal_not_exponent_notation()
    {
        Assert.Contains("E", (1e-7).ToString("R", System.Globalization.CultureInfo.InvariantCulture));

        StubHandler handler = Accepting();
        var fix = new LocationFix(1e-7, -1.234e-7) { Speed = 2.5e-6, Elevation = -3e-5 };
        await Publisher(handler).EmitLocationAsync("dev-1", "cred-9", fix, Occurred);

        StubHandler.Call call = Assert.Single(handler.Calls);
        JsonElement entry = Entry(call);
        Assert.Equal("0.0000001", entry.GetProperty("latitude").GetString());
        Assert.Equal("-0.0000001234", entry.GetProperty("longitude").GetString());
        Assert.Equal("0.0000025", entry.GetProperty("speed").GetString());
        Assert.Equal("-0.00003", entry.GetProperty("elevation").GetString());
        foreach (string field in new[] { "latitude", "longitude", "speed", "elevation" })
        {
            string rendered = entry.GetProperty(field).GetString()!;
            Assert.DoesNotContain("E", rendered);
            Assert.DoesNotContain("e", rendered);
        }
    }

    // Full double precision survives the formatter. A fixed-decimal custom format ("0.####…") caps
    // at 15 significant digits and would silently drop the last two here — a metre-scale error
    // introduced by the producer's own formatting.
    [Fact]
    public async Task Full_double_precision_survives_the_formatter()
    {
        StubHandler handler = Accepting();
        var fix = new LocationFix(33.74912345678901, -84.38812345678901) { Elevation = 12345678.90123 };
        await Publisher(handler).EmitLocationAsync("dev-1", "cred-9", fix, Occurred);

        JsonElement entry = Entry(Assert.Single(handler.Calls));
        // 17 significant digits, exact: the 15-digit cap would render this "33.749123456789".
        Assert.Equal("33.74912345678901", entry.GetProperty("latitude").GetString());
        Assert.Equal("12345678.90123", entry.GetProperty("elevation").GetString());

        // …and every emitted value parses back to the exact double it came from. Shortest
        // round-trippable is allowed to be SHORTER than the literal (it is for the longitude here);
        // it is never allowed to be a different number.
        foreach ((string field, double original) in new[]
                 {
                     ("latitude", 33.74912345678901),
                     ("longitude", -84.38812345678901),
                     ("elevation", 12345678.90123),
                 })
        {
            string rendered = entry.GetProperty(field).GetString()!;
            Assert.Equal(
                original,
                double.Parse(rendered, System.Globalization.CultureInfo.InvariantCulture));
        }
    }

    // A device SDK that let an out-of-range coordinate through would not have avoided the failure,
    // it would have MOVED it — to a dead-letter queue or a 400 the caller cannot see. Nothing is
    // sent: the throw happens before the carrier is reached.
    [Theory]
    [InlineData("latitude", 90.0000001, 0.0)]
    [InlineData("latitude", -90.0000001, 0.0)]
    [InlineData("longitude", 0.0, 180.0000001)]
    [InlineData("longitude", 0.0, -180.0000001)]
    [InlineData("latitude", double.NaN, 0.0)]
    [InlineData("longitude", 0.0, double.PositiveInfinity)]
    // Degrees scaled by 1e7 is the classic units bug (the NMEA/LwM2M convention); 337490000 is
    // not a latitude at any scale.
    [InlineData("latitude", 337490000.0, 0.0)]
    public async Task An_out_of_range_coordinate_throws_and_emits_nothing(
        string field, double latitude, double longitude)
    {
        StubHandler handler = Accepting();
        ArgumentOutOfRangeException ex = await Assert.ThrowsAsync<ArgumentOutOfRangeException>(
            () => Publisher(handler).EmitLocationAsync(
                "dev-1", "cred-9", new LocationFix(latitude, longitude), Occurred));

        Assert.Contains(field, ex.ParamName);
        Assert.Empty(handler.Calls);
    }

    [Theory]
    [InlineData("heading", 360.0)]
    // Half a rounding quantum inside 360: the platform stores four decimals and ROUNDS, so this
    // would land as exactly 360.0000 — the second spelling of north the exclusive bound refuses.
    [InlineData("heading", 359.99995)]
    [InlineData("heading", 359.99999)]
    [InlineData("heading", -0.0001)]
    [InlineData("accuracy", -0.5)]
    [InlineData("speed", -0.5)]
    [InlineData("elevation", 100000000.0)]
    [InlineData("elevation", -100000000.0)]
    [InlineData("accuracy", 100000000.0)]
    [InlineData("speed", double.NaN)]
    public async Task An_out_of_range_optional_throws_and_emits_nothing(string field, double value)
    {
        StubHandler handler = Accepting();
        var fix = new LocationFix(33.749, -84.388)
        {
            Elevation = field == "elevation" ? value : null,
            Accuracy = field == "accuracy" ? value : null,
            Speed = field == "speed" ? value : null,
            Heading = field == "heading" ? value : null,
        };

        ArgumentOutOfRangeException ex = await Assert.ThrowsAsync<ArgumentOutOfRangeException>(
            () => Publisher(handler).EmitLocationAsync("dev-1", "cred-9", fix, Occurred));

        Assert.Contains(field, ex.ParamName);
        Assert.Empty(handler.Calls);
    }

    // 🔑 THE COUNTERWEIGHT. A validator that rejected EVERYTHING would satisfy both reject tables
    // above completely, so the legal boundary values must be shown to pass — and to arrive intact.
    [Fact]
    public async Task Legal_boundary_values_are_accepted_and_reach_the_wire()
    {
        StubHandler handler = Accepting();
        DeviceEventPublisher pub = Publisher(handler);

        var north = new LocationFix(90, 180) { Heading = 0, Accuracy = 0, Speed = 0, Elevation = 99999999 };
        var south = new LocationFix(-90, -180) { Heading = 359.9999, Elevation = -99999999 };

        await pub.EmitLocationAsync("dev-1", "cred-9", north, Occurred);
        await pub.EmitLocationAsync("dev-1", "cred-9", south, Occurred);

        Assert.Equal(2, handler.Calls.Count);

        JsonElement first = Entry(handler.Calls[0]);
        Assert.Equal("90", first.GetProperty("latitude").GetString());
        Assert.Equal("180", first.GetProperty("longitude").GetString());
        Assert.Equal("0", first.GetProperty("heading").GetString());
        Assert.Equal("0", first.GetProperty("accuracy").GetString());
        Assert.Equal("0", first.GetProperty("speed").GetString());
        Assert.Equal("99999999", first.GetProperty("elevation").GetString());

        JsonElement second = Entry(handler.Calls[1]);
        Assert.Equal("-90", second.GetProperty("latitude").GetString());
        Assert.Equal("-180", second.GetProperty("longitude").GetString());
        Assert.Equal("359.9999", second.GetProperty("heading").GetString());
        Assert.Equal("-99999999", second.GetProperty("elevation").GetString());
    }

    // The entry's occurred time is the envelope's, and it carries fractional seconds. A base event
    // is keyed by (tenant, device, event_type, occurred_time), so two fixes inside one wall-clock
    // second under a whole-second stamp would collide and the second one would be silently dropped
    // — with a 202 returned either way. A device tracking a moving machine samples far faster.
    [Fact]
    public async Task Two_fixes_in_one_second_stay_distinct_and_the_entry_shares_the_envelope_stamp()
    {
        StubHandler handler = Accepting();
        DeviceEventPublisher pub = Publisher(handler);

        await pub.EmitLocationAsync(
            "dev-1", "cred-9", new LocationFix(33.749, -84.388), Occurred.AddMilliseconds(250));
        await pub.EmitLocationAsync(
            "dev-1", "cred-9", new LocationFix(33.750, -84.389), Occurred.AddMilliseconds(500));

        Assert.Equal(2, handler.Calls.Count);
        Assert.Contains("\"occurredTime\":\"2026-08-07T12:00:00.2500000Z\"", handler.Calls[0].Body);
        Assert.Contains("\"occurredTime\":\"2026-08-07T12:00:00.5000000Z\"", handler.Calls[1].Body);

        using JsonDocument document = JsonDocument.Parse(handler.Calls[0].Body);
        Assert.Equal(
            document.RootElement.GetProperty("occurredTime").GetString(),
            Entry(handler.Calls[0]).GetProperty("occurredTime").GetString());
    }

    [Fact]
    public async Task Throws_when_the_ingress_rejects_the_location_event()
    {
        var handler = new StubHandler((_, _) => (HttpStatusCode.TooManyRequests, "rate limited"));

        GraphQlRequestException ex = await Assert.ThrowsAsync<GraphQlRequestException>(
            () => Publisher(handler).EmitLocationAsync("dev-1", "cred-9", new LocationFix(33.749, -84.388)));
        Assert.Equal(429, ex.Status);
    }

    // The fold a producer runs a raw bearing through before it reaches a fix. 360 and 0 are the
    // same bearing, so folding one onto the other loses nothing — but it has to happen a quantum
    // INSIDE 360, or a raw atan2 result trips the range check occasionally rather than never.
    [Theory]
    [InlineData(0.0, 0.0)]
    [InlineData(271.5, 271.5)]
    [InlineData(360.0, 0.0)]
    [InlineData(359.99999, 0.0)]
    [InlineData(359.9999, 0.0)]
    [InlineData(359.9998, 359.9998)]
    [InlineData(-90.0, 270.0)]
    [InlineData(720.5, 0.5)]
    [InlineData(-360.0, 0.0)]
    public void CanonicalHeading_folds_a_raw_bearing_into_the_stored_range(double raw, double expected)
    {
        double folded = LocationFix.CanonicalHeading(raw);
        Assert.Equal(expected, folded, 6);
        Assert.InRange(folded, 0.0, 359.9999);
    }

    // …and what it produces is always emittable, which is the only reason it exists.
    [Fact]
    public async Task A_canonicalised_heading_is_always_accepted()
    {
        StubHandler handler = Accepting();
        DeviceEventPublisher pub = Publisher(handler);

        foreach (double raw in new[] { 0.0, 359.99999, 360.0, 720.0, -0.00001, -720.5 })
        {
            var fix = new LocationFix(33.749, -84.388) { Heading = LocationFix.CanonicalHeading(raw) };
            await pub.EmitLocationAsync("dev-1", "cred-9", fix, Occurred);
        }

        Assert.Equal(6, handler.Calls.Count);
    }
}
