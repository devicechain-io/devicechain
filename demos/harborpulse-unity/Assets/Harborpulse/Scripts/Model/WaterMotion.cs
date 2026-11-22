// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

using System;
using System.Collections.Generic;

namespace DeviceChain.Harborpulse.Model
{
    /// <summary>The axis-aligned pump rig's outer well footprint, shared by geometry and the harbour-water mask.</summary>
    public readonly struct WellFootprint
    {
        // The attached well clears North Quay lamp 2; the pump's PresentationMap anchor stays fixed.
        public const double LocalX = 13, LocalZ = 6, RadiusM = 2.45;
        public WellFootprint(Ground pump) { Center = new Ground(pump.X + LocalX, pump.Z + LocalZ); }

        /// <summary>A camera frames a well when the well is within this distance and this far off the camera's axis, in degrees.</summary>
        public const double TagRangeM = 24, TagHalfAngleDeg = 20;

        /// <summary>
        /// Whether the well's level tags are drawn: only for a camera that frames the well. The tags are text, which the font's own material
        /// draws over whatever stands in front, and they sit below the deck inside the cut-away; from a camera that merely stood beside a well
        /// (a lamp shot, a gauge shot) they showed through the quay at the frame's edge. <paramref name="dx"/>.. is the well's centre at deck
        /// level less the camera's position, <paramref name="fx"/>.. the camera's unit forward.
        /// </summary>
        public static bool TagsInView(double dx, double dy, double dz, double fx, double fy, double fz)
        {
            var d = Math.Sqrt(dx * dx + dy * dy + dz * dz);
            if (d > TagRangeM) return false;
            if (d < 1e-6) return true;
            return (dx * fx + dy * fy + dz * fz) / d >= Math.Cos(TagHalfAngleDeg * Math.PI / 180);
        }
        public Ground Center { get; }
        public bool Contains(Ground point) => (point.X - Center.X) * (point.X - Center.X) + (point.Z - Center.Z) * (point.Z - Center.Z) < RadiusM * RadiusM;
    }

    /// <summary>Visual spray mapping from WORLD storm and the observed regional tide derivative. No synthetic tide.</summary>
    public sealed class WaterMotion
    {
        readonly Dictionary<string, (double Level, DateTimeOffset At)> previous = new Dictionary<string, (double, DateTimeOffset)>();
        readonly Dictionary<string, double> rates = new Dictionary<string, double>();
        public IReadOnlyDictionary<string, double> Update(IEnumerable<RegionLevel> regions, double storm)
        {
            var result = new Dictionary<string, double>();
            foreach (var r in regions)
            {
                if (!r.LevelM.HasValue || !r.NewestAt.HasValue || r.Held) { result[r.Quay] = 0; continue; }
                var at = r.NewestAt.Value;
                if (previous.TryGetValue(r.Quay, out var p) && at > p.At)
                    rates[r.Quay] = Math.Max(0, (r.LevelM.Value - p.Level) / (at - p.At).TotalSeconds);
                if (!previous.TryGetValue(r.Quay, out p) || at > p.At) previous[r.Quay] = (r.LevelM.Value, at);
                rates.TryGetValue(r.Quay, out var rate);
                result[r.Quay] = Rgb.Clamp01(storm) * (0.2 + 0.8 * Rgb.Clamp01(rate / 0.02));
            }
            return new System.Collections.ObjectModel.ReadOnlyDictionary<string, double>(result);
        }
        public static IReadOnlyDictionary<string, double> Overtopping(IEnumerable<RegionLevel> regions, double floodLevel)
        {
            var result = new Dictionary<string, double>();
            foreach (var r in regions) result[r.Quay] = r.LevelM.HasValue ? Rgb.Clamp01((r.LevelM.Value - floodLevel) / 0.3) : 0;
            return new System.Collections.ObjectModel.ReadOnlyDictionary<string, double>(result);
        }
        public static double ReflectionLevel(IEnumerable<RegionLevel> regions)
        {
            var levels = new List<double>();
            foreach (var r in regions) if (r.LevelM.HasValue) levels.Add(r.LevelM.Value);
            return levels.Count == 0 ? 0 : TideBoard.Median(levels);
        }
    }
}
