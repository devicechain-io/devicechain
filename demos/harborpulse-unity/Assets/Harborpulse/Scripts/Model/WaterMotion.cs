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
