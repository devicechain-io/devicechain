// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

using System;
using System.Collections.Generic;
using System.Globalization;
using System.Text.Json;

namespace DeviceChain.Harborpulse.Model
{
    /// <summary>The scene's names for the kinds of device, as the layout file writes them (the simulator's harborpulse_scene.go).</summary>
    public static class DeviceKind
    {
        public const string Gauge = "gauge", LoraGauge = "lora-gauge", LegacyGauge = "legacy-gauge", Pump = "pump", Light = "light", Charger = "charger",
            Meter = "meter", Truck = "truck";

        /// <summary>
        /// The kinds that hold their own session with the platform or ask it for their orders: a charger on MQTT under a device token, one on
        /// MQTT under a client certificate, and a lamp that fetches its commands over HTTP. Each has its own device type and reports less than
        /// the district's chargers and street lights (a charging power; a dim level), so none of them is drawn, or read, as one of those.
        /// </summary>
        public const string MqttCharger = "mqtt-charger", CertCharger = "cert-charger", PullLight = "pull-light";

        static readonly string[] Known = { Gauge, LoraGauge, LegacyGauge, Pump, Light, Charger, Meter, Truck, MqttCharger, CertCharger, PullLight };

        public static IReadOnlyList<string> All => Known;

        public static bool IsGauge(string kind) => kind == Gauge || kind == LoraGauge || kind == LegacyGauge;

        /// <summary>A charger that reports its charging power over its own MQTT session and nothing else.</summary>
        public static bool IsLinkedCharger(string kind) => kind == MqttCharger || kind == CertCharger;

        /// <summary>Any charger: it stands at the head of a parking bay.</summary>
        public static bool HasBay(string kind) => kind == Charger || IsLinkedCharger(kind);

        public static bool IsKnown(string kind) => Array.IndexOf(Known, kind) >= 0;
    }

    /// <summary>A WGS84 position.</summary>
    public readonly struct LatLon
    {
        public LatLon(double lat, double lon)
        {
            Lat = lat;
            Lon = lon;
        }

        public double Lat { get; }
        public double Lon { get; }

        public override string ToString() =>
            Lat.ToString("0.0000", CultureInfo.InvariantCulture) + "," + Lon.ToString("0.0000", CultureInfo.InvariantCulture);
    }

    public sealed class LayoutQuay
    {
        public string Token { get; internal set; }
        public string Name { get; internal set; }
    }

    public sealed class LayoutDevice
    {
        public string Token { get; internal set; }
        public string ExternalId { get; internal set; }
        public string DeviceType { get; internal set; }
        public string Kind { get; internal set; }
        public string Quay { get; internal set; }
        public int Index { get; internal set; }

        /// <summary>The manifest's surveyed position; null for every device the manifest gives none (all but the street lights).</summary>
        public LatLon? Position { get; internal set; }
    }

    public sealed class LayoutBeat
    {
        public string Name { get; internal set; }
        public double From { get; internal set; }
        public double To { get; internal set; }
    }

    public sealed class LayoutFence
    {
        public string Token { get; internal set; }
        public string Name { get; internal set; }
        public IReadOnlyList<LatLon> Ring { get; internal set; }
    }

    /// <summary>The water levels the scene marks on its gauge boards, metres above quay datum.</summary>
    public sealed class LayoutLevels
    {
        public double FloodMinorM { get; internal set; }
        public double FloodMajorM { get; internal set; }
        public double FloodCriticalM { get; internal set; }
        public double QuayFloodingM { get; internal set; }
        public double WetWellHighM { get; internal set; }
    }

    /// <summary>
    /// The scenario's planned cues: hints for a director's pre-roll, never evidence. Keys ending Sim are sim hours from the run's first midnight
    /// (day 1 at 02:00 is 26); keys ending S are wall seconds.
    /// </summary>
    public sealed class LayoutCues
    {
        public double TruckDepartSim { get; internal set; }
        public double TruckInPlainSim { get; internal set; }
        public double TruckOutPlainSim { get; internal set; }
        public double TruckHomeSim { get; internal set; }
        public double ReplacementSim { get; internal set; }
        public double RolloutSim { get; internal set; }
        public double LampServiceFromSim { get; internal set; }
        public double LampServiceToSim { get; internal set; }
        public double AirLockFromSim { get; internal set; }
        public double AirLockToSim { get; internal set; }
        public double SilenceS { get; internal set; }
        public double AirLockHoldS { get; internal set; }
        public double EscalateAfterS { get; internal set; }
    }

    /// <summary>
    /// The harbour district as the simulator's manifest declares it: the committed <c>harbor_layout.json</c>, which the simulator's own test
    /// regenerates from the manifest and holds equal to it. Parsing is strict: an unknown version, kind or quay, a duplicate device or a missing
    /// field is a <see cref="FormatException"/> naming what is wrong, never a district drawn from a guess.
    /// </summary>
    public sealed class HarborLayout
    {
        public const int SupportedVersion = 1;

        public int Version { get; private set; }
        public string Scenario { get; private set; }
        public double DayLengthSeconds { get; private set; }
        public double StartHour { get; private set; }
        public double PromenadeLat { get; private set; }
        public IReadOnlyList<LayoutQuay> Quays { get; private set; }
        public IReadOnlyList<LayoutDevice> Devices { get; private set; }
        public IReadOnlyList<LayoutFence> Fences { get; private set; }
        public LatLon Depot { get; private set; }
        public IReadOnlyList<LayoutBeat> Beats { get; private set; }
        public LayoutLevels Levels { get; private set; }
        public LayoutCues Cues { get; private set; }

        /// <summary>The truck's route, depot first: the manifest's waypoints.</summary>
        public IReadOnlyList<LatLon> TruckRoute { get; private set; }

        /// <summary>A rectangle, south-west corner first and anticlockwise, strictly containing every manifest position in the file.</summary>
        public IReadOnlyList<LatLon> ControlFrame { get; private set; }

        public LayoutDevice Find(string externalId)
        {
            foreach (var d in Devices)
                if (d.ExternalId == externalId) return d;
            return null;
        }

        public LayoutQuay Quay(string token)
        {
            foreach (var q in Quays)
                if (q.Token == token) return q;
            return null;
        }

        public LayoutBeat Beat(string name)
        {
            foreach (var b in Beats)
                if (b.Name == name) return b;
            return null;
        }

        public static HarborLayout Parse(string json)
        {
            JsonDocument doc;
            try { doc = JsonDocument.Parse(json ?? ""); }
            catch (JsonException e) { throw new FormatException("harbor layout: not valid JSON: " + e.Message, e); }

            using (doc)
            {
                var root = doc.RootElement;
                if (root.ValueKind != JsonValueKind.Object) throw new FormatException("harbor layout: must be a JSON object");
                var l = new HarborLayout { Version = Int(root, "version", "") };
                if (l.Version != SupportedVersion)
                    throw new FormatException("harbor layout: version " + l.Version + " is not the version this scene reads (" + SupportedVersion +
                                              "); regenerate it with the simulator it came from");
                l.Scenario = Str(root, "scenario", "");
                if (l.Scenario != "harborpulse") throw new FormatException("harbor layout: scenario is \"" + l.Scenario + "\", not harborpulse");
                l.DayLengthSeconds = Num(root, "dayLengthSeconds", "");
                l.StartHour = Num(root, "startHour", "");
                l.PromenadeLat = Num(root, "promenadeLat", "");
                l.Depot = Point(Obj(root, "depot", ""), "depot");

                var quays = new List<LayoutQuay>();
                foreach (var q in Arr(root, "quays", ""))
                    quays.Add(new LayoutQuay { Token = Str(q, "token", "quays[]"), Name = Str(q, "name", "quays[]") });
                if (quays.Count == 0) throw new FormatException("harbor layout: no quays");
                l.Quays = quays;

                var seen = new HashSet<string>(StringComparer.Ordinal);
                var devices = new List<LayoutDevice>();
                foreach (var e in Arr(root, "devices", ""))
                {
                    var d = new LayoutDevice
                    {
                        Token = Str(e, "token", "devices[]"), ExternalId = Str(e, "externalId", "devices[]"), DeviceType = Str(e, "deviceType", "devices[]"),
                        Kind = Str(e, "kind", "devices[]"), Quay = Str(e, "quay", "devices[]"), Index = Int(e, "index", "devices[]"),
                    };
                    var where = "device " + d.ExternalId;
                    if (!seen.Add(d.ExternalId)) throw new FormatException("harbor layout: " + where + " is listed twice");
                    if (!DeviceKind.IsKnown(d.Kind)) throw new FormatException("harbor layout: " + where + " has kind \"" + d.Kind + "\", which this scene cannot draw");
                    if (l.Quay(d.Quay) == null) throw new FormatException("harbor layout: " + where + " stands on quay \"" + d.Quay + "\", which the layout does not declare");
                    if (e.TryGetProperty("position", out var p) && p.ValueKind != JsonValueKind.Null) d.Position = Point(p, where + " position");
                    devices.Add(d);
                }

                if (devices.Count == 0) throw new FormatException("harbor layout: no devices");
                l.Devices = devices;

                var fences = new List<LayoutFence>();
                foreach (var f in Arr(root, "fences", ""))
                {
                    var ring = new List<LatLon>();
                    foreach (var pair in Arr(f, "ring", "fences[]"))
                    {
                        if (pair.ValueKind != JsonValueKind.Array || pair.GetArrayLength() != 2) throw new FormatException("harbor layout: a fence ring position is not [lon, lat]");
                        ring.Add(new LatLon(pair[1].GetDouble(), pair[0].GetDouble()));
                    }

                    fences.Add(new LayoutFence { Token = Str(f, "token", "fences[]"), Name = Str(f, "name", "fences[]"), Ring = ring });
                }

                l.Fences = fences;

                var beats = new List<LayoutBeat>();
                foreach (var b in Arr(root, "beats", ""))
                {
                    var beat = new LayoutBeat { Name = Str(b, "name", "beats[]"), From = Num(b, "from", "beats[]"), To = Num(b, "to", "beats[]") };
                    if (!(beat.To > beat.From)) throw new FormatException("harbor layout: beat " + beat.Name + " ends before it starts");
                    beats.Add(beat);
                }

                l.Beats = beats;

                var lv = Obj(root, "levels", "");
                l.Levels = new LayoutLevels
                {
                    FloodMinorM = Num(lv, "floodMinorM", "levels"), FloodMajorM = Num(lv, "floodMajorM", "levels"),
                    FloodCriticalM = Num(lv, "floodCriticalM", "levels"), QuayFloodingM = Num(lv, "quayFloodingM", "levels"),
                    WetWellHighM = Num(lv, "wetWellHighM", "levels"),
                };

                var c = Obj(root, "cues", "");
                l.Cues = new LayoutCues
                {
                    TruckDepartSim = Num(c, "truckDepartSim", "cues"), TruckInPlainSim = Num(c, "truckInPlainSim", "cues"),
                    TruckOutPlainSim = Num(c, "truckOutPlainSim", "cues"), TruckHomeSim = Num(c, "truckHomeSim", "cues"),
                    ReplacementSim = Num(c, "replacementSim", "cues"), RolloutSim = Num(c, "rolloutSim", "cues"),
                    LampServiceFromSim = Num(c, "lampServiceFromSim", "cues"), LampServiceToSim = Num(c, "lampServiceToSim", "cues"),
                    AirLockFromSim = Num(c, "airLockFromSim", "cues"), AirLockToSim = Num(c, "airLockToSim", "cues"),
                    SilenceS = Num(c, "silenceS", "cues"), AirLockHoldS = Num(c, "airLockHoldS", "cues"), EscalateAfterS = Num(c, "escalateAfterS", "cues"),
                };
                if (!(l.Cues.TruckDepartSim < l.Cues.TruckInPlainSim && l.Cues.TruckInPlainSim < l.Cues.TruckOutPlainSim && l.Cues.TruckOutPlainSim < l.Cues.TruckHomeSim))
                    throw new FormatException("harbor layout: the truck cues are not in order (depart, in, out, home)");
                if (!(l.Cues.LampServiceFromSim < l.Cues.LampServiceToSim) || !(l.Cues.AirLockFromSim < l.Cues.AirLockToSim))
                    throw new FormatException("harbor layout: a cue window ends before it starts");
                if (!(l.Cues.SilenceS > 0) || !(l.Cues.AirLockHoldS > 0) || !(l.Cues.EscalateAfterS > 0))
                    throw new FormatException("harbor layout: a cue's wall duration is not positive");

                var route = new List<LatLon>();
                foreach (var p in Arr(root, "truckRoute", "")) route.Add(Point(p, "truckRoute[]"));
                if (route.Count < 2) throw new FormatException("harbor layout: the truck route needs at least two waypoints");
                if (route[0].Lat != l.Depot.Lat || route[0].Lon != l.Depot.Lon) throw new FormatException("harbor layout: the truck route does not start at the depot");
                l.TruckRoute = route;

                var frame = new List<LatLon>();
                foreach (var p in Arr(root, "controlFrame", "")) frame.Add(Point(p, "controlFrame[]"));
                if (frame.Count != 4 || frame[0].Lat != frame[1].Lat || frame[2].Lat != frame[3].Lat || frame[0].Lon != frame[3].Lon || frame[1].Lon != frame[2].Lon ||
                    !(frame[2].Lat > frame[0].Lat) || !(frame[1].Lon > frame[0].Lon))
                    throw new FormatException("harbor layout: the control frame is not a rectangle, south-west corner first and anticlockwise");
                void Inside(LatLon p, string what)
                {
                    if (!(p.Lat > frame[0].Lat && p.Lat < frame[2].Lat && p.Lon > frame[0].Lon && p.Lon < frame[1].Lon))
                        throw new FormatException("harbor layout: " + what + " " + p + " is not strictly inside the control frame");
                }
                foreach (var d in devices) if (d.Position.HasValue) Inside(d.Position.Value, d.ExternalId);
                foreach (var f in fences) foreach (var p in f.Ring) Inside(p, "fence " + f.Token);
                Inside(l.Depot, "the depot");
                foreach (var p in route) Inside(p, "a truck route waypoint");
                l.ControlFrame = frame;
                return l;
            }
        }

        static LatLon Point(JsonElement o, string where)
        {
            if (o.ValueKind != JsonValueKind.Object) throw new FormatException("harbor layout: " + where + " is not an object");
            return new LatLon(Num(o, "lat", where), Num(o, "lon", where));
        }

        static string At(string where) => where.Length > 0 ? where + ": " : "";

        static JsonElement Prop(JsonElement o, string name, string where)
        {
            if (!o.TryGetProperty(name, out var v) || v.ValueKind == JsonValueKind.Null)
                throw new FormatException("harbor layout: " + At(where) + "missing \"" + name + "\"");
            return v;
        }

        static string Str(JsonElement o, string name, string where)
        {
            var v = Prop(o, name, where);
            if (v.ValueKind != JsonValueKind.String || string.IsNullOrWhiteSpace(v.GetString()))
                throw new FormatException("harbor layout: " + At(where) + "\"" + name + "\" must be a non-blank string");
            return v.GetString();
        }

        static double Num(JsonElement o, string name, string where)
        {
            var v = Prop(o, name, where);
            if (v.ValueKind != JsonValueKind.Number || !v.TryGetDouble(out var d) || double.IsNaN(d) || double.IsInfinity(d))
                throw new FormatException("harbor layout: " + At(where) + "\"" + name + "\" must be a number");
            return d;
        }

        static int Int(JsonElement o, string name, string where)
        {
            var v = Prop(o, name, where);
            if (v.ValueKind != JsonValueKind.Number || !v.TryGetInt32(out var i))
                throw new FormatException("harbor layout: " + At(where) + "\"" + name + "\" must be an integer");
            return i;
        }

        static JsonElement Obj(JsonElement o, string name, string where)
        {
            var v = Prop(o, name, where);
            if (v.ValueKind != JsonValueKind.Object) throw new FormatException("harbor layout: " + At(where) + "\"" + name + "\" must be an object");
            return v;
        }

        static JsonElement.ArrayEnumerator Arr(JsonElement o, string name, string where)
        {
            var v = Prop(o, name, where);
            if (v.ValueKind != JsonValueKind.Array) throw new FormatException("harbor layout: " + At(where) + "\"" + name + "\" must be an array");
            return v.EnumerateArray();
        }
    }
}
