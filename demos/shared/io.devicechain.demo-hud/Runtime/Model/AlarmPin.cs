// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

using System;
using System.Collections.Generic;
using System.Linq;

namespace DeviceChain.Demos.Hud
{
    /// <summary>The platform's alarm severities, weakest first.</summary>
    public enum PinSeverity { Warning = 0, Minor = 1, Major = 2, Critical = 3 }

    /// <summary>
    /// How one severity is drawn. Severity is never colour alone: a warning is an outline, a minor alarm is filled, a major one is
    /// filled with a light edge, and a critical one is filled, larger and breathing.
    /// </summary>
    public readonly struct PinStyle
    {
        public PinStyle(HudColor fill, HudColor edge, HudColor glyph, bool outlined, float scale, bool breathes)
        { Fill = fill; Edge = edge; Glyph = glyph; Outlined = outlined; Scale = scale; Breathes = breathes; }
        public HudColor Fill { get; }
        public HudColor Edge { get; }
        public HudColor Glyph { get; }
        /// <summary>
        /// The body is dark and the severity's colour is the line and the glyph. Every pin, outlined or filled, also carries a thin dark
        /// outline outside its edge (<see cref="AlarmPins.OutlinePx"/>), so it reads on a bright sky and on a night grade alike.
        /// </summary>
        public bool Outlined { get; }
        public float Scale { get; }
        public bool Breathes { get; }
    }

    /// <summary>One device's alarms, as the caller observed them: the worst severity and how many are active.</summary>
    public sealed class PinInput
    {
        public PinInput(string key, PinSeverity severity, int count, bool steady = false) { Key = key; Severity = severity; Count = count; Steady = steady; }
        /// <summary>The device (one pin per device, however many alarms it has).</summary>
        public string Key { get; }
        public PinSeverity Severity { get; }
        public int Count { get; }
        /// <summary>Every alarm on the device is acknowledged: the pin does not breathe.</summary>
        public bool Steady { get; }
    }

    public sealed class PinDraw
    {
        public string Key { get; internal set; }
        public PinSeverity Severity { get; internal set; }
        /// <summary>Shown as a badge when above one.</summary>
        public int Count { get; internal set; }
        /// <summary>0 .. 1: the pin itself. Falls as a cleared alarm cross-fades to its tick.</summary>
        public float PinAlpha { get; internal set; }
        /// <summary>The pin's size against its nominal one: the raise's scale-in, the severity's scale and a critical pin's breath.</summary>
        public float Scale { get; internal set; }
        /// <summary>0 .. 1 while the one ring of a raise expands, else -1.</summary>
        public float Ring { get; internal set; }
        /// <summary>0 .. 1: the green tick of a cleared alarm.</summary>
        public float TickAlpha { get; internal set; }
    }

    /// <summary>The alarm pin's fixed rules: its size on screen, its styles and what a severity word means.</summary>
    public static class AlarmPins
    {
        /// <summary>Screen-space height at 1080p: the nominal size and the clamp. A device stays the largest thing near its pin.</summary>
        public const float NominalPx = 30, MinPx = 22, MaxPx = 38;

        /// <summary>The world height the pin stands for before the clamp, and how far it hovers over the device's highest point.</summary>
        public const double WorldHeightM = 1.6, HoverM = 0.5;

        /// <summary>The one thing kept for distance: a soft glow on the ground under the pin.</summary>
        public const double GlowRadiusM = 0.6;
        public const float GlowOpacity = 0.25f;

        public const double RaiseS = 0.25, RingS = 0.6, ClearFadeS = 0.4, TickHoldS = 3, TickFadeS = 1, BreathS = 2;
        public const float CriticalScale = 1.15f, BreathDepth = 0.05f;

        /// <summary>The width of the pin's dark outline in pixels at the nominal size.</summary>
        public const float OutlinePx = 1.5f;

        static readonly HudColor MinorFill = new HudColor(0.98f, 0.80f, 0.22f), MajorFill = new HudColor(0.96f, 0.50f, 0.10f), CriticalFill = new HudColor(0.80f, 0.16f, 0.08f);
        static readonly HudColor LightEdge = new HudColor(1f, 0.95f, 0.88f), WarningLine = new HudColor(0.99f, 0.84f, 0.36f);

        /// <summary>
        /// A recorded severity word. Anything else (a severity the recording did not sample, a word this build does not know) is drawn
        /// as the weakest alarm: never a guess upwards.
        /// </summary>
        public static PinSeverity Severity(string word)
        {
            switch (word)
            {
                case "CRITICAL": return PinSeverity.Critical;
                case "MAJOR": return PinSeverity.Major;
                case "MINOR": return PinSeverity.Minor;
                default: return PinSeverity.Warning;
            }
        }

        public static PinStyle Style(PinSeverity severity)
        {
            switch (severity)
            {
                case PinSeverity.Critical: return new PinStyle(CriticalFill, LightEdge, new HudColor(1, 1, 1), false, CriticalScale, true);
                case PinSeverity.Major: return new PinStyle(MajorFill, LightEdge, HudTheme.Edge, false, 1, false);
                case PinSeverity.Minor: return new PinStyle(MinorFill, HudTheme.Edge, HudTheme.Edge, false, 1, false);
                default: return new PinStyle(HudTheme.Edge, WarningLine, WarningLine, true, 1, false);
            }
        }

        /// <summary>
        /// The pin's height in reference pixels (1080p) for a device <paramref name="distanceM"/> from a camera of that vertical field
        /// of view: what <see cref="WorldHeightM"/> would cover, held between the far floor and the close ceiling.
        /// </summary>
        public static float Pixels(double distanceM, double verticalFovDeg)
        {
            if (distanceM <= 0 || verticalFovDeg <= 0 || verticalFovDeg >= 180) return MaxPx;
            var px = WorldHeightM / (2 * distanceM * Math.Tan(verticalFovDeg * Math.PI / 360)) * HudTheme.ReferenceHeight;
            return (float)Math.Max(MinPx, Math.Min(MaxPx, px));
        }

        /// <summary>The glow's width in reference pixels: the ground disc as the camera sees it, never thinner than the pin's floor.</summary>
        public static float GlowPixels(double distanceM, double verticalFovDeg)
        {
            if (distanceM <= 0 || verticalFovDeg <= 0 || verticalFovDeg >= 180) return MaxPx * 2;
            var px = 2 * GlowRadiusM / (2 * distanceM * Math.Tan(verticalFovDeg * Math.PI / 360)) * HudTheme.ReferenceHeight;
            return (float)Math.Max(MinPx, Math.Min(MaxPx * 3, px));
        }
    }

    /// <summary>
    /// Every pin on screen, and how each is drawn now. The caller passes the devices with an active alarm; the board gives each a
    /// raise (a quick scale-in and one expanding ring), and when its alarms are gone a clear (the pin cross-fades to a green tick,
    /// which holds and fades). Nothing spins and nothing repeats. Time is the caller's clock, moving forward.
    /// </summary>
    public sealed class AlarmPinBoard
    {
        sealed class Entry { public PinSeverity Severity; public int Count; public bool Steady; public double RaisedAtS; public double? ClearedAtS; }
        readonly Dictionary<string, Entry> entries = new Dictionary<string, Entry>(StringComparer.Ordinal);
        bool settle = true;

        /// <summary>
        /// The next update is a new shot (or the first frame): alarms already there are simply drawn, with no raise, and the ticks of
        /// alarms that cleared before it are not shown. A viewer is only shown a change that happens in front of them.
        /// </summary>
        public void Settle() { settle = true; entries.Clear(); }

        public IReadOnlyList<PinDraw> Update(IEnumerable<PinInput> active, double nowS)
        {
            var seen = new HashSet<string>(StringComparer.Ordinal);
            foreach (var input in active ?? Enumerable.Empty<PinInput>())
            {
                if (input == null || string.IsNullOrEmpty(input.Key) || input.Count < 1 || !seen.Add(input.Key)) continue;
                if (!entries.TryGetValue(input.Key, out var e) || e.ClearedAtS.HasValue)
                    entries[input.Key] = e = new Entry { RaisedAtS = settle ? double.NegativeInfinity : nowS };
                // a severity that moves (an escalation) changes the same pin: it is not raised again
                e.Severity = input.Severity; e.Count = input.Count; e.Steady = input.Steady;
            }
            foreach (var pair in entries.ToArray())
            {
                if (seen.Contains(pair.Key)) continue;
                if (!pair.Value.ClearedAtS.HasValue) pair.Value.ClearedAtS = nowS;
                if (nowS - pair.Value.ClearedAtS.Value >= AlarmPins.ClearFadeS + AlarmPins.TickHoldS + AlarmPins.TickFadeS) entries.Remove(pair.Key);
            }
            settle = false;
            var draws = new List<PinDraw>();
            foreach (var pair in entries.OrderBy(p => p.Key, StringComparer.Ordinal))
            {
                var e = pair.Value; var style = AlarmPins.Style(e.Severity);
                var raise = Math.Max(0, Math.Min(1, (nowS - e.RaisedAtS) / AlarmPins.RaiseS));
                var scale = (float)(1 - Math.Pow(1 - raise, 3)) * style.Scale;
                if (style.Breathes && !e.Steady) scale *= 1 + AlarmPins.BreathDepth * (float)Math.Sin(nowS * 2 * Math.PI / AlarmPins.BreathS);
                var ring = (nowS - e.RaisedAtS) / AlarmPins.RingS;
                var draw = new PinDraw { Key = pair.Key, Severity = e.Severity, Count = e.Count, PinAlpha = 1, Scale = scale, Ring = ring >= 0 && ring < 1 ? (float)ring : -1 };
                if (e.ClearedAtS.HasValue)
                {
                    var t = nowS - e.ClearedAtS.Value; var fade = (float)Math.Max(0, Math.Min(1, t / AlarmPins.ClearFadeS));
                    draw.PinAlpha = 1 - fade; draw.Ring = -1;
                    draw.TickAlpha = t < AlarmPins.ClearFadeS + AlarmPins.TickHoldS ? fade : (float)Math.Max(0, 1 - (t - AlarmPins.ClearFadeS - AlarmPins.TickHoldS) / AlarmPins.TickFadeS);
                }
                draws.Add(draw);
            }
            return draws;
        }
    }

    /// <summary>
    /// Where each device's pin stands. A device that does not move has ONE anchor: the place it was first given, whatever is asked
    /// later, so a static pin cannot drift, jitter or flip between two places from frame to frame. A moving subject (a vehicle) is
    /// followed: its anchor is the place given this frame, which the caller must have settled for the frame before the HUD reads it.
    /// </summary>
    public sealed class PinAnchors
    {
        readonly Dictionary<string, double[]> fixedAt = new Dictionary<string, double[]>(StringComparer.Ordinal);

        /// <summary>The anchor for a device: (x, y, z) as given for a moving one, as first given for a static one.</summary>
        public double[] Place(string key, bool moving, double x, double y, double z)
        {
            if (moving) return new[] { x, y, z };
            if (!fixedAt.TryGetValue(key, out var at)) fixedAt.Add(key, at = new[] { x, y, z });
            return at;
        }

        /// <summary>The world was rebuilt (devices may stand elsewhere): every anchor is taken afresh.</summary>
        public void Clear() => fixedAt.Clear();
    }
}
