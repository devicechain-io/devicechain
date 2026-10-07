// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

using System;
using System.Collections.Generic;
using System.Globalization;
using System.Text;
using System.Text.Json;
using DeviceChain.Sitepulse.Domain;
using DeviceChain.Sitepulse.Recording;

namespace DeviceChain.Sitepulse.Replay
{
    /// <summary>A shots file, or a recording, that cannot be rendered from. The message says which shot and why; nothing is rendered on a guess.</summary>
    public sealed class ShotException : Exception
    {
        public ShotException(string message) : base(message)
        {
        }
    }

    /// <summary>
    /// A recorded event a shot starts from, by what it IS rather than when it happened:
    /// <list type="bullet">
    /// <item><c>{"kind":"alarm","key":"low-fuel","state":"ACTIVE","device":"SP-HL-0006"}</c> an alarm line the observer received;</item>
    /// <item><c>{"kind":"command","name":"goto-refuel","status":"SENT","device":"SP-HL-0006"}</c> a command row the observer received;</item>
    /// <item><c>{"kind":"measurement","name":"fuel_pct","below":15,"device":"SP-HL-0006"}</c> the first sample on the far side of a line (<c>above</c> too);</item>
    /// <item><c>{"kind":"timeline","device":"SP-HL-0006","rowKind":"arrived","contains":"bay"}</c> a row of the machine's own timeline;</item>
    /// <item><c>{"kind":"runStart"}</c> the first moment of the recording.</item>
    /// </list>
    /// <c>occurrence</c> (default 0) picks the n-th match. <c>device</c> is the scene id (SP-HL-0006), as the recording's header maps it.
    /// A selector that matches nothing is an error that names what the recording does hold.
    /// </summary>
    public sealed class EventSelector
    {
        public string Kind { get; set; }
        public string Device { get; set; }
        public string Key { get; set; }
        public string State { get; set; }
        public string Name { get; set; }
        public string Status { get; set; }
        public string RowKind { get; set; }
        public string Contains { get; set; }
        public double? Below { get; set; }
        public double? Above { get; set; }
        public int Occurrence { get; set; }

        public static EventSelector Read(JsonElement e, string where)
        {
            if (e.ValueKind != JsonValueKind.Object) throw new ShotException(where + ": startEvent must be an object");
            var s = new EventSelector
            {
                Kind = JsonIo.Str(e, "kind"),
                Device = JsonIo.Str(e, "device"),
                Key = JsonIo.Str(e, "key"),
                State = JsonIo.Str(e, "state"),
                Name = JsonIo.Str(e, "name"),
                Status = JsonIo.Str(e, "status"),
                RowKind = JsonIo.Str(e, "rowKind"),
                Contains = JsonIo.Str(e, "contains"),
                Below = JsonIo.NumOrNull(e, "below"),
                Above = JsonIo.NumOrNull(e, "above"),
                Occurrence = (int)JsonIo.Long(e, "occurrence"),
            };
            var known = new HashSet<string> { "kind", "device", "key", "state", "name", "status", "rowKind", "contains", "below", "above", "occurrence" };
            foreach (var p in e.EnumerateObject())
                if (!known.Contains(p.Name)) throw new ShotException($"{where}: startEvent has an unknown field \"{p.Name}\" (a misspelt field would select the wrong event)");
            if (s.Occurrence < 0) throw new ShotException(where + ": occurrence cannot be negative");
            switch (s.Kind)
            {
                case "alarm":
                    if (s.Key == null) throw new ShotException(where + ": an alarm event needs \"key\"");
                    break;
                case "command":
                    if (s.Name == null) throw new ShotException(where + ": a command event needs \"name\"");
                    break;
                case "measurement":
                    if (s.Name == null || (!s.Below.HasValue && !s.Above.HasValue)) throw new ShotException(where + ": a measurement event needs \"name\" and \"below\" or \"above\"");
                    break;
                case "timeline":
                    if (s.Device == null) throw new ShotException(where + ": a timeline event needs \"device\"");
                    break;
                case "runStart":
                    break;
                default:
                    throw new ShotException($"{where}: startEvent kind \"{s.Kind}\" is not one of alarm, command, measurement, timeline, runStart");
            }

            return s;
        }

        public override string ToString()
        {
            var sb = new StringBuilder(Kind);
            void Add(string k, string v)
            {
                if (v != null) sb.Append(' ').Append(k).Append('=').Append(v);
            }

            Add("device", Device); Add("key", Key); Add("state", State); Add("name", Name); Add("status", Status); Add("rowKind", RowKind); Add("contains", Contains);
            if (Below.HasValue) Add("below", Below.Value.ToString(CultureInfo.InvariantCulture));
            if (Above.HasValue) Add("above", Above.Value.ToString(CultureInfo.InvariantCulture));
            if (Occurrence > 0) Add("occurrence", Occurrence.ToString(CultureInfo.InvariantCulture));
            return sb.ToString();
        }

        /// <summary>The recorded moment the event happened for the viewer (seconds into the run), and its time of day.</summary>
        public ResolvedEvent Resolve(RecordingData data)
        {
            var h = data.Header;
            string token = null;
            if (Device != null && Kind != "timeline")
            {
                token = h.TokenOf(Device);
                if (token == null) throw new ShotException($"no recorded event: the recording has no device \"{Device}\" (it holds: {string.Join(", ", DeviceIds(h))})");
            }

            var seen = 0;
            switch (Kind)
            {
                case "runStart":
                    return new ResolvedEvent(0.0, h.StartedAtUtc, "the start of the recording");
                case "alarm":
                    foreach (var l in data.Observed)
                    {
                        if (l.K != ObservedKinds.Alarm || l.Name != Key || (State != null && l.State != State) || (token != null && l.Device != token)) continue;
                        if (seen++ == Occurrence) return new ResolvedEvent(l.T, l.Utc, $"alarm {l.Name} {l.State} on {h.IdOf(l.Device) ?? l.Device}");
                    }

                    break;
                case "command":
                    foreach (var l in data.Observed)
                    {
                        if (l.K != ObservedKinds.Command || l.Name != Name || (Status != null && l.State != Status) || (token != null && l.Device != token)) continue;
                        if (seen++ == Occurrence) return new ResolvedEvent(l.T, l.Utc, $"command {l.Name} {l.State} on {h.IdOf(l.Device) ?? l.Device}");
                    }

                    break;
                case "measurement":
                    foreach (var l in data.Observed)
                    {
                        if (l.K != ObservedKinds.Measurement || l.FromSnapshot || l.Name != Name || (token != null && l.Device != token)) continue;
                        if (Below.HasValue && !(l.Value < Below.Value)) continue;
                        if (Above.HasValue && !(l.Value > Above.Value)) continue;
                        if (seen++ == Occurrence) return new ResolvedEvent(l.T, l.Utc, $"{l.Name} {l.Value.ToString("0.##", CultureInfo.InvariantCulture)} on {h.IdOf(l.Device) ?? l.Device}");
                    }

                    break;
                case "timeline":
                    foreach (var l in data.Device)
                    {
                        if (l.K != DeviceKinds.Timeline || l.Device != Device || (RowKind != null && l.RowKind != RowKind) || (Contains != null && l.Text.IndexOf(Contains, StringComparison.OrdinalIgnoreCase) < 0)) continue;
                        if (seen++ == Occurrence) return new ResolvedEvent(l.T, l.Utc, $"{l.Device} timeline {l.RowKind}: {l.Text}");
                    }

                    break;
            }

            throw new ShotException($"no recorded event matches [{this}]" + (seen > 0 ? $": it matches {seen} time(s), occurrence {Occurrence} does not exist" : ": " + Holds(data)));
        }

        string Holds(RecordingData data)
        {
            var kinds = new SortedSet<string>(StringComparer.Ordinal);
            switch (Kind)
            {
                case "alarm":
                    foreach (var l in data.Observed)
                        if (l.K == ObservedKinds.Alarm) kinds.Add(l.Name + " " + l.State);
                    return "the recording holds alarm events: " + (kinds.Count == 0 ? "none" : string.Join(", ", kinds));
                case "command":
                    foreach (var l in data.Observed)
                        if (l.K == ObservedKinds.Command) kinds.Add(l.Name + " " + l.State);
                    return "the recording holds command rows: " + (kinds.Count == 0 ? "none" : string.Join(", ", kinds));
                case "measurement":
                    foreach (var l in data.Observed)
                        if (l.K == ObservedKinds.Measurement) kinds.Add(l.Name);
                    return "the recording holds measurements: " + (kinds.Count == 0 ? "none" : string.Join(", ", kinds));
                default:
                    foreach (var l in data.Device)
                        if (l.K == DeviceKinds.Timeline && l.Device == Device) kinds.Add(l.RowKind);
                    return $"{Device}'s timeline holds rows of kind: " + (kinds.Count == 0 ? "none" : string.Join(", ", kinds));
            }
        }

        static IEnumerable<string> DeviceIds(RunHeader h)
        {
            foreach (var d in h.Devices) yield return d.Id;
        }
    }

    /// <summary>Where a shot's event was, in the recording.</summary>
    public readonly struct ResolvedEvent
    {
        public ResolvedEvent(double t, DateTimeOffset utc, string description)
        {
            T = t;
            Utc = utc;
            Description = description;
        }

        public double T { get; }
        public DateTimeOffset Utc { get; }
        public string Description { get; }
    }

    public enum RigKind { Fixed, Orbit, Follow }

    /// <summary>
    /// A camera, authored as parameters. <c>fixed</c>: <c>pos</c> and <c>lookAt</c> (a point, or a machine id as <c>lookAtTarget</c>), with an
    /// optional <c>to</c> (a second <c>pos</c>/<c>lookAt</c>/<c>fov</c> it eases to over the shot: a crane). <c>orbit</c>: round <c>target</c> or <c>point</c>
    /// at <c>radius</c> and <c>height</c>, <c>degreesPerSecond</c> from <c>startDegrees</c>. <c>follow</c>: behind <c>target</c> by <c>back</c> metres, <c>up</c> above and
    /// <c>side</c> to the right (negative: the left), looking at it. Positions are Unity metres (x east, y up, z north). Every pose is a function of
    /// the recorded machine pose and the time into the shot, so a render is repeatable.
    /// </summary>
    public sealed class CameraSpec
    {
        public RigKind Rig { get; set; }
        public float Fov { get; set; } = 50f;
        public float[] Pos { get; set; }
        public float[] LookAt { get; set; }
        public string LookAtTarget { get; set; }
        public float[] ToPos { get; set; }
        public float[] ToLookAt { get; set; }
        public float? ToFov { get; set; }

        // a follow camera's own easing: where it ends up behind, above and beside the machine (a pull-back, a swing to the top)
        public float? ToBack { get; set; }
        public float? ToUp { get; set; }
        public float? ToSide { get; set; }
        public float? ToLookHeight { get; set; }
        public string Target { get; set; }
        public float[] Point { get; set; }
        public float Radius { get; set; } = 25f;
        public float Height { get; set; } = 12f;
        public float DegreesPerSecond { get; set; } = 10f;
        public float StartDegrees { get; set; }
        public float Back { get; set; } = 12f;
        public float Up { get; set; } = 4f;
        public float Side { get; set; } = -3f;
        public float LookHeight { get; set; } = 1.5f;

        /// <summary>A follow camera looks this many metres to the right of the machine (negative: the left), so the machine sits left of centre in the frame and a panel can take the right.</summary>
        public float LookSide { get; set; }

        public static CameraSpec Read(JsonElement e, string where)
        {
            if (e.ValueKind != JsonValueKind.Object) throw new ShotException(where + ": camera must be an object");
            var rig = JsonIo.Str(e, "rig");
            var c = new CameraSpec();
            switch (rig)
            {
                case "fixed": c.Rig = RigKind.Fixed; break;
                case "orbit": c.Rig = RigKind.Orbit; break;
                case "follow": c.Rig = RigKind.Follow; break;
                default: throw new ShotException($"{where}: camera rig \"{rig}\" is not one of fixed, orbit, follow");
            }

            var known = new HashSet<string> { "rig", "fov", "pos", "lookAt", "lookAtTarget", "to", "target", "point", "radius", "height", "degreesPerSecond", "startDegrees", "back", "up", "side", "lookHeight", "lookSide" };
            foreach (var p in e.EnumerateObject())
                if (!known.Contains(p.Name)) throw new ShotException($"{where}: camera has an unknown field \"{p.Name}\"");
            c.Fov = (float)JsonIo.Num(e, "fov", 50.0);
            c.Pos = Vec(e, "pos", where);
            c.LookAt = Vec(e, "lookAt", where);
            c.LookAtTarget = JsonIo.Str(e, "lookAtTarget");
            c.Target = JsonIo.Str(e, "target");
            c.Point = Vec(e, "point", where);
            c.Radius = (float)JsonIo.Num(e, "radius", c.Radius);
            c.Height = (float)JsonIo.Num(e, "height", c.Height);
            c.DegreesPerSecond = (float)JsonIo.Num(e, "degreesPerSecond", c.DegreesPerSecond);
            c.StartDegrees = (float)JsonIo.Num(e, "startDegrees", 0.0);
            c.Back = (float)JsonIo.Num(e, "back", c.Back);
            c.Up = (float)JsonIo.Num(e, "up", c.Up);
            c.Side = (float)JsonIo.Num(e, "side", c.Side);
            c.LookHeight = (float)JsonIo.Num(e, "lookHeight", c.LookHeight);
            c.LookSide = (float)JsonIo.Num(e, "lookSide", 0.0);
            if (e.TryGetProperty("to", out var to))
            {
                if (to.ValueKind != JsonValueKind.Object) throw new ShotException(where + ": camera.to must be an object");
                foreach (var p in to.EnumerateObject())
                    if (p.Name != "pos" && p.Name != "lookAt" && p.Name != "fov" && p.Name != "back" && p.Name != "up" && p.Name != "side" && p.Name != "lookHeight")
                        throw new ShotException($"{where}: camera.to has an unknown field \"{p.Name}\"");
                c.ToPos = Vec(to, "pos", where);
                c.ToLookAt = Vec(to, "lookAt", where);
                var f = JsonIo.NumOrNull(to, "fov");
                c.ToFov = f.HasValue ? (float)f.Value : (float?)null;
                c.ToBack = Opt(to, "back");
                c.ToUp = Opt(to, "up");
                c.ToSide = Opt(to, "side");
                c.ToLookHeight = Opt(to, "lookHeight");
                if (c.Rig != RigKind.Follow && (c.ToBack.HasValue || c.ToUp.HasValue || c.ToSide.HasValue || c.ToLookHeight.HasValue))
                    throw new ShotException($"{where}: camera.to back, up, side and lookHeight ease a follow camera; this one is {rig}");
            }

            switch (c.Rig)
            {
                case RigKind.Fixed:
                    if (c.Pos == null || (c.LookAt == null && c.LookAtTarget == null)) throw new ShotException(where + ": a fixed camera needs \"pos\" and \"lookAt\" (or \"lookAtTarget\")");
                    break;
                case RigKind.Orbit:
                    if (c.Target == null && c.Point == null) throw new ShotException(where + ": an orbit camera needs \"target\" or \"point\"");
                    break;
                case RigKind.Follow:
                    if (c.Target == null) throw new ShotException(where + ": a follow camera needs \"target\"");
                    break;
            }

            if (c.Fov < 5f || c.Fov > 120f) throw new ShotException($"{where}: fov {c.Fov} is outside 5 to 120");
            return c;
        }

        static float? Opt(JsonElement e, string name)
        {
            var v = JsonIo.NumOrNull(e, name);
            return v.HasValue ? (float)v.Value : (float?)null;
        }

        static float[] Vec(JsonElement e, string name, string where)
        {
            if (!e.TryGetProperty(name, out var v)) return null;
            if (v.ValueKind != JsonValueKind.Array || v.GetArrayLength() != 3) throw new ShotException($"{where}: \"{name}\" must be [x, y, z]");
            var r = new float[3];
            var i = 0;
            foreach (var n in v.EnumerateArray())
            {
                if (n.ValueKind != JsonValueKind.Number) throw new ShotException($"{where}: \"{name}\" must be numbers");
                r[i++] = (float)n.GetDouble();
            }

            return r;
        }

        /// <summary>The machines this camera looks at or follows (they must exist in the recording).</summary>
        public IEnumerable<string> Machines()
        {
            if (Target != null) yield return Target;
            if (LookAtTarget != null) yield return LookAtTarget;
        }
    }

    /// <summary>
    /// A line of small print drawn into a shot's frames: <c>{"text":"Command sent","at":4,"duration":3}</c> (seconds into the shot) or
    /// <c>{"text":"Rule fires: low fuel","event":{...},"offset":0,"duration":3}</c>, which starts when a recorded event did, so it can never say
    /// something happened before it did. Render-only.
    /// </summary>
    public sealed class ChipSpec
    {
        public const int MaxText = 60;

        public string Text { get; set; }
        public double At { get; set; }
        public double Duration { get; set; }
        public EventSelector Event { get; set; }
        public double Offset { get; set; }

        public static ChipSpec Read(JsonElement e, string where)
        {
            if (e.ValueKind != JsonValueKind.Object) throw new ShotException(where + ": a chip must be an object");
            foreach (var p in e.EnumerateObject())
                if (p.Name != "text" && p.Name != "at" && p.Name != "event" && p.Name != "offset" && p.Name != "duration")
                    throw new ShotException($"{where}: a chip has an unknown field \"{p.Name}\"");
            var c = new ChipSpec { Text = JsonIo.Str(e, "text"), At = JsonIo.Num(e, "at"), Offset = JsonIo.Num(e, "offset"), Duration = JsonIo.Num(e, "duration", -1) };
            if (string.IsNullOrWhiteSpace(c.Text) || c.Text.Length > MaxText) throw new ShotException($"{where}: a chip needs \"text\" of 1 to {MaxText} characters");
            if (!(c.Duration > 0)) throw new ShotException($"{where}: chip \"{c.Text}\" needs a \"duration\" in seconds");
            var hasEvent = e.TryGetProperty("event", out var ev);
            var hasAt = e.TryGetProperty("at", out _);
            if (hasEvent == hasAt) throw new ShotException($"{where}: chip \"{c.Text}\" needs exactly one of \"at\" (seconds into the shot) or \"event\" (a recorded event)");
            if (hasEvent)
            {
                // a chip tied to an event says it happened: it can start with the event or after it, never before
                if (c.Offset < 0) throw new ShotException($"{where}: chip \"{c.Text}\" starts before its event (\"offset\" cannot be negative)");
                c.Event = EventSelector.Read(ev, where + ", chip \"" + c.Text + "\"");
            }
            else if (c.At < 0) throw new ShotException($"{where}: chip \"{c.Text}\" starts before the shot does");
            return c;
        }
    }

    /// <summary>One shot of a shots file.</summary>
    public sealed class Shot
    {
        /// <summary>The parts of the data layer drawn in the shot (cards, drawer, panel, route, zone labels). Never the badge, readiness panel, key help or a replay tag: a render has none.</summary>
        public OverlayLayers Layers { get; set; } = OverlayLayers.RenderDefault;

        /// <summary>Machines whose cards show for the shot besides alarms, commands and the selected one (<c>"cards": ["SP-HL-0003", ...]</c>).</summary>
        public List<string> PinnedCards { get; } = new List<string>();

        /// <summary>Seconds between one pinned card appearing and the next; 0 shows them all at once.</summary>
        public double CardStagger { get; set; }

        /// <summary>Seconds into the shot the zone labels start to fade in (<c>"zoneLabels": 3</c>); 0 with <c>"zoneLabels": true</c> is from the first frame.</summary>
        public double ZoneLabelsDelay { get; set; }

        /// <summary>A measurement drawn first on the selected machine's card (<c>"featured": "tyre_pressure_kpa"</c>), alarm or none.</summary>
        public string Featured { get; set; }

        /// <summary>Card size relative to a 1080-pixel-high frame; a portrait frame is 1920 high, so its cards are drawn smaller.</summary>
        public float CardScale { get; set; } = 1f;

        public List<ChipSpec> Chips { get; } = new List<ChipSpec>();

        public string Name { get; set; }
        public EventSelector StartEvent { get; set; }
        public double Offset { get; set; }
        public double Duration { get; set; }
        public double Preroll { get; set; }
        public int Width { get; set; }
        public int Height { get; set; }
        public string Aspect { get; set; }
        public CameraSpec Camera { get; set; }

        /// <summary>The machine whose card is shown for the shot, besides alarms and commands; null: the follow camera's target, if there is one.</summary>
        public string Focus { get; set; }

        /// <summary>The machine selected while the shot renders.</summary>
        public string Selected => Focus ?? (Camera != null && Camera.Rig == RigKind.Follow ? Camera.Target : null);
    }

    /// <summary>A shots file: the frame rate and the shots.</summary>
    public sealed class ShotFile
    {
        public const double DefaultPreroll = 3.0;
        public int Fps { get; set; } = 60;

        /// <summary>
        /// <c>"continuous": true</c>: the shots are one story told forward, so each starts at or after the previous one's end in recorded time.
        /// A cut that overlaps its predecessor shows the same moment twice (it reads as a scene repeating), and the planner refuses it by name.
        /// </summary>
        public bool Continuous { get; set; }

        public List<Shot> Shots { get; } = new List<Shot>();

        public static ShotFile Parse(string json)
        {
            JsonDocument doc;
            try { doc = JsonDocument.Parse(json); }
            catch (JsonException e) { throw new ShotException("the shots file is not JSON: " + e.Message); }
            using (doc)
            {
                var root = doc.RootElement;
                if (root.ValueKind != JsonValueKind.Object) throw new ShotException("the shots file must be an object with a \"shots\" array");
                foreach (var p in root.EnumerateObject())
                    if (p.Name != "fps" && p.Name != "shots" && p.Name != "description" && p.Name != "continuous") throw new ShotException($"the shots file has an unknown field \"{p.Name}\"");
                var file = new ShotFile { Fps = (int)JsonIo.Long(root, "fps", 60) };
                if (file.Fps != 30 && file.Fps != 60) throw new ShotException($"fps {file.Fps} is not 30 or 60");
                if (root.TryGetProperty("continuous", out var cont))
                {
                    if (cont.ValueKind != JsonValueKind.True && cont.ValueKind != JsonValueKind.False) throw new ShotException("\"continuous\" must be true or false");
                    file.Continuous = cont.GetBoolean();
                }
                if (!root.TryGetProperty("shots", out var shots) || shots.ValueKind != JsonValueKind.Array || shots.GetArrayLength() == 0)
                    throw new ShotException("the shots file has no \"shots\"");
                var names = new HashSet<string>(StringComparer.Ordinal);
                var n = 0;
                foreach (var s in shots.EnumerateArray())
                {
                    n++;
                    if (s.ValueKind != JsonValueKind.Object) throw new ShotException($"shot {n} is not an object");
                    var name = JsonIo.Str(s, "name");
                    var where = $"shot {n}" + (name != null ? $" ({name})" : "");
                    if (string.IsNullOrEmpty(name) || !IsSafeName(name)) throw new ShotException(where + ": \"name\" is required and may hold only letters, digits, - and _ (it is a directory name)");
                    if (!names.Add(name)) throw new ShotException(where + ": the name is used twice");
                    foreach (var p in s.EnumerateObject())
                        if (!ShotFields.Contains(p.Name))
                            throw new ShotException($"{where}: unknown field \"{p.Name}\"" + (BannedFrameFields.Contains(p.Name) ? ": a render never carries the mode badge, the readiness panel, the key help or a replay tag" : ""));
                    if (!s.TryGetProperty("startEvent", out var se)) throw new ShotException(where + ": \"startEvent\" is required: a shot starts from a recorded event, never from a time");
                    if (!s.TryGetProperty("camera", out var cam)) throw new ShotException(where + ": \"camera\" is required");
                    var shot = new Shot
                    {
                        Name = name,
                        StartEvent = EventSelector.Read(se, where),
                        Offset = JsonIo.Num(s, "offset"),
                        Duration = JsonIo.Num(s, "duration", -1),
                        Preroll = JsonIo.Num(s, "preroll", DefaultPreroll),
                        Aspect = JsonIo.Str(s, "aspect", "16:9"),
                        Camera = CameraSpec.Read(cam, where),
                        Focus = JsonIo.Str(s, "focus"),
                    };
                    if (!(shot.Duration > 0 && shot.Duration <= 600)) throw new ShotException(where + ": \"duration\" is required, in seconds, up to 600");
                    ReadLayers(s, shot, where);
                    if (shot.Preroll < 0 || shot.Preroll > 60) throw new ShotException(where + ": \"preroll\" is 0 to 60 seconds");
                    var w = (int)JsonIo.Long(s, "width");
                    var h = (int)JsonIo.Long(s, "height");
                    if (w == 0 && h == 0)
                    {
                        switch (shot.Aspect)
                        {
                            case "16:9": w = 1920; h = 1080; break;
                            case "9:16": w = 1080; h = 1920; break;
                            default: throw new ShotException($"{where}: aspect \"{shot.Aspect}\" is not 16:9 or 9:16");
                        }
                    }
                    else if (w < 16 || h < 16 || w > 8192 || h > 8192 || (w & 1) != 0 || (h & 1) != 0)
                        throw new ShotException($"{where}: width and height must both be given, even, and 16 to 8192");
                    shot.Width = w;
                    shot.Height = h;
                    file.Shots.Add(shot);
                }

                return file;
            }
        }

        static readonly HashSet<string> ShotFields = new HashSet<string>
        {
            "name", "startEvent", "offset", "duration", "preroll", "aspect", "width", "height", "camera", "focus",
            "cards", "cardStagger", "cardScale", "drawer", "panel", "route", "zoneLabels", "chips", "featured",
        };

        // a shot file that asks for one of these is told why it cannot have it
        static readonly HashSet<string> BannedFrameFields = new HashSet<string>
        {
            "badge", "hud", "readiness", "help", "keyHelp", "replayTag", "replay", "tag", "timeline",
        };

        static bool? FlagOf(JsonElement s, string name, string where)
        {
            if (!s.TryGetProperty(name, out var v)) return null;
            if (v.ValueKind == JsonValueKind.True) return true;
            if (v.ValueKind == JsonValueKind.False) return false;
            throw new ShotException($"{where}: \"{name}\" must be true or false");
        }

        static void ReadLayers(JsonElement s, Shot shot, string where)
        {
            var layers = OverlayLayers.RenderDefault;
            bool? cards = null;
            if (s.TryGetProperty("cards", out var c))
            {
                if (c.ValueKind == JsonValueKind.True || c.ValueKind == JsonValueKind.False) cards = c.GetBoolean();
                else if (c.ValueKind == JsonValueKind.Array)
                {
                    cards = true;
                    foreach (var id in c.EnumerateArray())
                    {
                        if (id.ValueKind != JsonValueKind.String || string.IsNullOrEmpty(id.GetString())) throw new ShotException($"{where}: \"cards\" lists machine ids");
                        if (shot.PinnedCards.Contains(id.GetString())) throw new ShotException($"{where}: \"cards\" lists {id.GetString()} twice");
                        shot.PinnedCards.Add(id.GetString());
                    }
                }
                else throw new ShotException($"{where}: \"cards\" must be true, false or a list of machine ids");
            }

            DrawerMode? drawer = null;
            if (s.TryGetProperty("drawer", out var d))
            {
                if (d.ValueKind == JsonValueKind.True) drawer = DrawerMode.Side;
                else if (d.ValueKind == JsonValueKind.False) drawer = DrawerMode.Off;
                else if (d.ValueKind == JsonValueKind.String && d.GetString() == "side") drawer = DrawerMode.Side;
                else if (d.ValueKind == JsonValueKind.String && d.GetString() == "full") drawer = DrawerMode.Full;
                else throw new ShotException($"{where}: \"drawer\" must be true, false, \"side\" or \"full\"");
            }

            bool? zone = null;
            if (s.TryGetProperty("zoneLabels", out var z))
            {
                if (z.ValueKind == JsonValueKind.True) zone = true;
                else if (z.ValueKind == JsonValueKind.False) zone = false;
                else if (z.ValueKind == JsonValueKind.Number && z.GetDouble() >= 0)
                {
                    zone = true;
                    shot.ZoneLabelsDelay = z.GetDouble();
                }
                else throw new ShotException($"{where}: \"zoneLabels\" must be true, false or the seconds into the shot they fade in");
            }

            shot.Layers = layers.With(cards, drawer, FlagOf(s, "panel", where), FlagOf(s, "route", where), zone);
            shot.CardStagger = JsonIo.Num(s, "cardStagger");
            if (shot.CardStagger < 0 || shot.CardStagger > 30) throw new ShotException(where + ": \"cardStagger\" is 0 to 30 seconds");
            if (s.TryGetProperty("cardScale", out _))
            {
                shot.CardScale = (float)JsonIo.Num(s, "cardScale", 1.0);
                if (shot.CardScale < 0.4f || shot.CardScale > 2f) throw new ShotException(where + ": \"cardScale\" is 0.4 to 2");
            }

            shot.Featured = JsonIo.Str(s, "featured");
            if (shot.Featured != null && !IsEquipmentKey(shot.Featured)) throw new ShotException($"{where}: \"featured\" \"{shot.Featured}\" is not an equipment measurement key");
            if (s.TryGetProperty("chips", out var chips))
            {
                if (chips.ValueKind != JsonValueKind.Array) throw new ShotException(where + ": \"chips\" must be a list");
                foreach (var ch in chips.EnumerateArray())
                {
                    var spec = ChipSpec.Read(ch, where);
                    if (spec.Event == null && spec.At + spec.Duration > shot.Duration + 1e-6)
                        throw new ShotException($"{where}: chip \"{spec.Text}\" runs past the end of the {shot.Duration:0.##} s shot");
                    shot.Chips.Add(spec);
                }
            }
        }

        static bool IsEquipmentKey(string key)
        {
            foreach (var k in MeasurementKeys.Equipment)
                if (k == key) return true;
            return false;
        }

        static bool IsSafeName(string s)
        {
            foreach (var c in s)
                if (!(char.IsLetterOrDigit(c) && c < 128 || c == '-' || c == '_')) return false;
            return s.Length <= 64;
        }
    }

    /// <summary>What a shot has on screen at a moment of it: a pure function of the shot and the seconds into it, so a render is repeatable.</summary>
    public static class ShotDressing
    {
        /// <summary>The layers at <paramref name="t"/> seconds into the shot: the zone names fade in after their delay.</summary>
        public static OverlayLayers LayersAt(Shot shot, double t) =>
            shot.Layers.ZoneLabels && t < shot.ZoneLabelsDelay ? shot.Layers.With(zoneLabels: false) : shot.Layers;

        /// <summary>How many of the shot's pinned cards show at <paramref name="t"/>: all of them, or one more each stagger from the first frame (none in the lead-in).</summary>
        public static int PinnedCount(Shot shot, double t)
        {
            var all = shot.PinnedCards.Count;
            if (shot.CardStagger <= 0) return all;
            if (t < 0) return 0;
            return Math.Min(all, (int)(t / shot.CardStagger) + 1);
        }
    }

    /// <summary>A shot after its event was found: where it starts and ends in the recording, and how many frames it has.</summary>
    public sealed class PlannedShot
    {
        public Shot Shot { get; set; }
        public ResolvedEvent Event { get; set; }

        /// <summary>Seconds into the recording the first frame shows.</summary>
        public double Start { get; set; }

        public int Frames { get; set; }

        /// <summary>The shot's chips, with the moment each starts and ends in seconds into the shot (an event's chip starts when the event did).</summary>
        public List<ChipView> Chips { get; } = new List<ChipView>();

        /// <summary>The event plus the offset fell before the recording began, so the shot starts at 0 instead; <see cref="ClampedBySeconds"/> is how much earlier it asked to start.</summary>
        public bool StartClamped { get; set; }

        public double ClampedBySeconds { get; set; }

        /// <summary>Where the pre-roll (frames simulated but not saved, so dust and exhaust have a history) begins.</summary>
        public double PrerollFrom { get; set; }
    }

    public static class ShotPlanner
    {
        /// <summary>
        /// Resolves every shot against the recording before anything is rendered: a shot whose event is absent, whose machine is not in the
        /// recording, or that would run past its end stops the whole render, so a half-rendered set never passes for a whole one.
        /// </summary>
        public static List<PlannedShot> Plan(ShotFile file, RecordingData data, Action<string> warn = null)
        {
            var plan = new List<PlannedShot>();
            foreach (var shot in file.Shots)
            {
                var ev = shot.StartEvent.Resolve(data);
                if (shot.Focus != null && !data.Sim.TryIndexOf(shot.Focus, out _)) throw new ShotException($"shot {shot.Name}: focus names machine \"{shot.Focus}\", which the recording does not hold");
                foreach (var m in shot.Camera.Machines())
                    if (!data.Sim.TryIndexOf(m, out _)) throw new ShotException($"shot {shot.Name}: the camera names machine \"{m}\", which the recording does not hold");
                var clip = CameraRigs.ClippingReason(shot.Camera);
                if (clip != null) throw new ShotException($"shot {shot.Name}: {clip}");
                foreach (var id in shot.PinnedCards)
                    if (data.Header.TokenOf(id) == null) throw new ShotException($"shot {shot.Name}: cards names device \"{id}\", which the recording does not hold");
                var start = ev.T + shot.Offset;
                var clamped = start < 0;
                var clampedBy = clamped ? -start : 0.0;
                if (clamped)
                {
                    start = 0;
                    warn?.Invoke($"shot {shot.Name}: its event is at {ev.T:0.0} s and the offset is {shot.Offset:0.0} s, which is {clampedBy:0.0} s before the recording began: the shot starts at 0 s (render.json says so)");
                }

                var end = start + shot.Duration;
                if (file.Continuous && plan.Count > 0)
                {
                    var before = plan[plan.Count - 1];
                    var beforeEnd = before.Start + before.Shot.Duration;
                    if (start < beforeEnd - 1e-6)
                        throw new ShotException($"shot {shot.Name}: starts at {start:0.0} s of the recording, {beforeEnd - start:0.0} s before {before.Shot.Name} ends ({beforeEnd:0.0} s), but the file is \"continuous\": every shot must start at or after the end of the one before it, or the cut shows the same moment twice");
                }

                if (end > data.Duration + 1e-6)
                    throw new ShotException($"shot {shot.Name}: starts at {start:0.0} s and lasts {shot.Duration:0.0} s, but the recording is {data.Duration:0.0} s long");
                var intrusion = CameraRigs.IntrusionReason(shot.Camera, data.Sim, start, shot.Duration);
                if (intrusion != null) throw new ShotException($"shot {shot.Name}: {intrusion}");
                var planned = new PlannedShot
                {
                    Shot = shot,
                    Event = ev,
                    Start = start,
                    StartClamped = clamped,
                    ClampedBySeconds = clampedBy,
                    Frames = (int)Math.Round(shot.Duration * file.Fps),
                    PrerollFrom = Math.Max(0.0, start - shot.Preroll),
                };
                foreach (var chip in shot.Chips)
                {
                    var from = chip.Event == null ? chip.At : chip.Event.Resolve(data).T + chip.Offset - start;
                    var until = from + chip.Duration;
                    if (until <= 0 || from >= shot.Duration)
                        throw new ShotException($"shot {shot.Name}: chip \"{chip.Text}\" falls outside the shot ({from:0.0} to {until:0.0} s into a {shot.Duration:0.0} s shot): its event is not where the shot is");
                    planned.Chips.Add(new ChipView(chip.Text, Math.Max(0.0, from), Math.Min(shot.Duration, until)));
                }

                plan.Add(planned);
            }

            return plan;
        }
    }
}
