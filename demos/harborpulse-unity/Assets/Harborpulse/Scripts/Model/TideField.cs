// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

using System;
using System.Collections.Generic;
using System.Linq;

namespace DeviceChain.Harborpulse.Model
{
    /// <summary>Rectangle distance field, mirrored in HarborWaves.hlsl. Heights remain metres above datum.</summary>
    public sealed class TideField
    {
        public const double Epsilon = 0.0001;
        public readonly struct Region
        {
            public Region(IEnumerable<Ground> polygon)
            {
                var p = polygon.ToArray();
                MinX = p.Min(v => v.X); MaxX = p.Max(v => v.X);
                MinZ = p.Min(v => v.Z); MaxZ = p.Max(v => v.Z);
            }
            public double MinX { get; }
            public double MaxX { get; }
            public double MinZ { get; }
            public double MaxZ { get; }
            public double DistanceSquared(double x, double z)
            {
                var dx = Math.Max(Math.Max(MinX - x, 0), x - MaxX);
                var dz = Math.Max(Math.Max(MinZ - z, 0), z - MaxZ);
                return dx * dx + dz * dz;
            }
        }
        readonly Region[] regions;
        public TideField(IEnumerable<Region> regions) { this.regions = regions.ToArray(); if (this.regions.Length == 0) throw new ArgumentException("no tide regions"); }
        public double LevelAt(double x, double z, IReadOnlyList<double> levels)
        {
            if (levels.Count != regions.Length) throw new ArgumentException("one level per region required");
            double sum = 0, weights = 0;
            for (var i = 0; i < regions.Length; i++)
            {
                var d2 = regions[i].DistanceSquared(x, z);
                // The d=0 limit pins the tide board exactly to its own quay median. Finite epsilon
                // alone leaves a micrometre bias there. The same limit is used by the shader.
                if (d2 == 0) return levels[i];
                var w = 1 / (d2 + Epsilon);
                sum += w * levels[i]; weights += w;
            }
            return sum / weights;
        }
    }
}
