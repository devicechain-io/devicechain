// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

using System;
using System.Collections.Generic;

namespace DeviceChain.Harborpulse.Model
{
    /// <summary>A conservative oriented ground rectangle, including overhangs. Yaw follows Unity's +Y convention.</summary>
    public sealed class PropFootprint
    {
        public string Id { get; }
        public Ground At { get; }
        public double Width { get; }
        public double Depth { get; }
        public double Yaw { get; }
        public PropFootprint(string id, Ground at, double width, double depth, double yaw = 0)
        {
            if (string.IsNullOrEmpty(id) || !double.IsFinite(at.X + at.Z + width + depth + yaw) || width <= 0 || depth <= 0)
                throw new ArgumentException("A footprint needs an identity, finite coordinates and positive dimensions");
            Id = id; At = at; Width = width; Depth = depth; Yaw = yaw;
        }
        public Ground Local(double x, double z)
        {
            var a = Yaw * Math.PI / 180;
            return new Ground(At.X + x * Math.Cos(a) + z * Math.Sin(a), At.Z - x * Math.Sin(a) + z * Math.Cos(a));
        }
        public PropFootprint Move(Ground at) => new PropFootprint(Id, at, Width, Depth, Yaw);
        public bool Intersects(PropFootprint b, double clearance = .15)
        {
            var a0 = Yaw * Math.PI / 180; var b0 = b.Yaw * Math.PI / 180;
            var ax = new Ground(Math.Cos(a0), -Math.Sin(a0)); var az = new Ground(Math.Sin(a0), Math.Cos(a0));
            var bx = new Ground(Math.Cos(b0), -Math.Sin(b0)); var bz = new Ground(Math.Sin(b0), Math.Cos(b0));
            bool Separated(Ground n)
            {
                double Dot(Ground u, Ground v) => u.X * v.X + u.Z * v.Z;
                var ra = Width * .5 * Math.Abs(Dot(ax,n)) + Depth * .5 * Math.Abs(Dot(az,n));
                var rb = b.Width * .5 * Math.Abs(Dot(bx,n)) + b.Depth * .5 * Math.Abs(Dot(bz,n));
                return Math.Abs((At.X-b.At.X)*n.X + (At.Z-b.At.Z)*n.Z) >= ra + rb + clearance;
            }
            return !(Separated(ax) || Separated(az) || Separated(bx) || Separated(bz));
        }
        internal double Radius => .5 * Math.Sqrt(Width * Width + Depth * Depth) + .2;
    }

    /// <summary>Build-time spatial hash, shared by the model plan and all visual builders. No per-frame work.</summary>
    public sealed class FootprintRegistry
    {
        readonly List<PropFootprint> entries = new List<PropFootprint>();
        readonly HashSet<string> ids = new HashSet<string>(StringComparer.Ordinal);
        readonly Dictionary<(int,int), List<PropFootprint>> cells = new Dictionary<(int,int), List<PropFootprint>>();
        public IReadOnlyList<PropFootprint> Entries => entries.AsReadOnly();
        IEnumerable<(int,int)> Cells(PropFootprint p)
        {
            for (var x=(int)Math.Floor((p.At.X-p.Radius)/32); x<=(int)Math.Floor((p.At.X+p.Radius)/32); x++)
                for (var z=(int)Math.Floor((p.At.Z-p.Radius)/32); z<=(int)Math.Floor((p.At.Z+p.Radius)/32); z++) yield return (x,z);
        }
        public bool IsFree(PropFootprint p)
        {
            foreach (var cell in Cells(p)) if (cells.TryGetValue(cell,out var near))
                foreach (var other in near) if (p.Intersects(other)) return false;
            return true;
        }
        public bool TryAdd(PropFootprint p)
        {
            if (ids.Contains(p.Id)) throw new ArgumentException("Duplicate footprint: " + p.Id);
            if (!IsFree(p)) return false;
            ids.Add(p.Id); entries.Add(p);
            foreach (var cell in Cells(p))
            {
                if (!cells.TryGetValue(cell,out var list)) cells[cell] = list = new List<PropFootprint>();
                list.Add(p);
            }
            return true;
        }
        public void AddFixed(PropFootprint p)
        {
            if (!TryAdd(p))
            {
                var conflicts = new List<string>();
                foreach (var other in entries) if (p.Intersects(other)) conflicts.Add(other.Id);
                throw new InvalidOperationException("Fixed footprint collision: " + p.Id + " / " + string.Join(", ",conflicts));
            }
        }
        /// <summary>Independent audit also accepts unregistered input, so planted collisions cannot pass by being filtered out.</summary>
        public static IReadOnlyList<string> FindOverlaps(IReadOnlyList<PropFootprint> input)
        {
            var result = new List<string>();
            for (var i=0;i<input.Count;i++) for (var j=0;j<i;j++)
                if (input[i].Intersects(input[j])) result.Add(input[j].Id + " / " + input[i].Id);
            return result.AsReadOnly();
        }
    }
}
