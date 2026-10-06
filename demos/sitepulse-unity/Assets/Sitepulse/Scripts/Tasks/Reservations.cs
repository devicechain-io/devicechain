// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

using System;
using System.Collections.Generic;
using DeviceChain.Sitepulse.Simulation;

namespace DeviceChain.Sitepulse.Tasks
{
    /// <summary>
    /// The refuel bay: one machine at a time, the others in the order they reached the queue. A machine
    /// asks when it arrives at the queue spot, is granted the bay when it is first in line, the bay is
    /// free and nothing stands in it, and gives it up when its task ends in any way (a supersede, a
    /// failure and a reset included). A holder whose task is gone is released by the director each step,
    /// so a bay cannot be held by a machine that is no longer refuelling.
    /// </summary>
    public sealed class BayReservations
    {
        readonly List<string> waiting = new List<string>();
        string holder;

        public string Holder => holder;
        public int Waiting => waiting.Count;

        public IReadOnlyList<string> Queue => waiting;

        /// <summary>Joins the line (once; a second ask keeps its place).</summary>
        public void Request(string id)
        {
            if (id == null) throw new ArgumentNullException(nameof(id));
            if (holder == id || waiting.Contains(id)) return;
            waiting.Add(id);
        }

        /// <summary>Takes the bay if it is this machine's turn and the bay is clear.</summary>
        public bool TryGrant(string id, bool bayClear)
        {
            if (holder == id) return true;
            if (holder != null || !bayClear || waiting.Count == 0 || waiting[0] != id) return false;
            waiting.RemoveAt(0);
            holder = id;
            return true;
        }

        /// <summary>Leaves the line or gives up the bay.</summary>
        public void Release(string id)
        {
            if (holder == id) holder = null;
            waiting.Remove(id);
        }

        public bool Holds(string id) => holder == id;

        public bool Has(string id) => holder == id || waiting.Contains(id);
    }

    /// <summary>A place to park inside a zone.</summary>
    public readonly struct ParkingSlot
    {
        public ParkingSlot(string zone, int index, double x, double z)
        {
            Zone = zone;
            Index = index;
            X = x;
            Z = z;
        }

        public string Zone { get; }
        public int Index { get; }
        public double X { get; }
        public double Z { get; }
    }

    /// <summary>
    /// Parking slots inside a zone: a grid inset from the zone's edge so that the machine's whole footprint
    /// (<see cref="Margin"/>) stays inside it, nearest the way in first, claimed by one machine each. A machine
    /// that leaves (a new command, resumed work) gives its slot back.
    /// </summary>
    public sealed class ParkingLot
    {
        public const double Spacing = 14.0, Inset = 8.0, ClearRadius = 6.0;

        /// <summary>How long a machine is, in metres (its pose is its middle), for how far from a zone's edge it must park.</summary>
        public static double LengthOf(EquipmentKind kind)
        {
            switch (kind)
            {
                case EquipmentKind.Hauler: return 12.0;
                case EquipmentKind.Loader: return 10.0;
                default: return 8.0;
            }
        }

        /// <summary>
        /// The radius of the circle a machine of this kind sweeps about its pose, in metres: from the footprint the fleet
        /// choreography is generated from (ArtSource/terrain/quarry_fleet.py <c>FOOT</c>: half width, half length front
        /// and rear, measured from the models), the corner farthest from the pose. A circle, because a parked machine may
        /// face any way.
        /// </summary>
        public static double FootprintRadius(EquipmentKind kind)
        {
            switch (kind)
            {
                case EquipmentKind.Hauler: return 6.4;  // hypot(5.7, 2.85)
                case EquipmentKind.Loader: return 5.4;  // hypot(5.2, 1.55)
                default: return 4.1;                    // hypot(3.7, 1.75)
            }
        }

        /// <summary>
        /// The radius a machine of this kind sweeps while it is DRIVING somewhere: it points along its path, so it is its half width
        /// and a little over (the footprint's half width, ArtSource/terrain/quarry_fleet.py <c>FOOT</c>), not the circle a parked machine
        /// that may face any way needs.
        /// </summary>
        public static double TravelRadius(EquipmentKind kind)
        {
            switch (kind)
            {
                case EquipmentKind.Hauler: return 3.0;
                default: return 2.0;
            }
        }

        /// <summary>The air kept between a DRIVING machine and anything standing near it: the refuel lane is laid out between cones and a tank with a little over this to spare.</summary>
        public const double TravelClearance = 0.2;

        /// <summary>The air kept between a parked machine and anything standing near it, in metres.</summary>
        public const double Clearance = 0.5;

        /// <summary>The nearest a machine of this kind parks to its zone's edge: half its length and a metre more, and never nearer than <see cref="Inset"/>.</summary>
        public static double Margin(EquipmentKind kind) => Math.Max(Inset, LengthOf(kind) / 2.0 + 1.0);

        readonly Dictionary<string, string> claims = new Dictionary<string, string>(StringComparer.Ordinal); // "zone#index" -> owner
        readonly Dictionary<string, string> byOwner = new Dictionary<string, string>(StringComparer.Ordinal);

        /// <summary>
        /// Every slot of a zone, nearest (<paramref name="gateX"/>, <paramref name="gateZ"/>) first, each at least
        /// <paramref name="margin"/> from the zone's edge and (given <paramref name="obstacles"/>) with a footprint of
        /// <paramref name="footprint"/> metres' radius clear of every one of them; a grid point that is not is dropped. A zone too small for that has no slots: nothing is parked half outside it.
        /// </summary>
        public static List<ParkingSlot> Slots(Rect2 zone, double gateX, double gateZ, double margin = Inset, IReadOnlyList<Obstacle> obstacles = null, double footprint = 0)
        {
            var slots = new List<ParkingSlot>();
            var x0 = zone.X0 + margin;
            var x1 = zone.X1 - margin;
            var z0 = zone.Z0 + margin;
            var z1 = zone.Z1 - margin;
            if (x1 < x0 || z1 < z0) return slots;
            if (obstacles != null && obstacles.Count > 0)
                return ObstructedSlots(zone, x0, x1, z0, z1, gateX, gateZ, obstacles, footprint);
            var nx = (int)Math.Floor((x1 - x0) / Spacing) + 1;
            var nz = (int)Math.Floor((z1 - z0) / Spacing) + 1;
            // spread the grid across the inset area, so a zone of one slot parks in its middle
            for (var i = 0; i < nx; i++)
                for (var j = 0; j < nz; j++)
                {
                    var x = nx == 1 ? (x0 + x1) / 2 : x0 + (x1 - x0) * i / (nx - 1);
                    var z = nz == 1 ? (z0 + z1) / 2 : z0 + (z1 - z0) * j / (nz - 1);
                    slots.Add(new ParkingSlot(zone.Name, i * nz + j, x, z));
                }

            SortByGate(slots, gateX, gateZ);
            return slots;
        }

        static void SortByGate(List<ParkingSlot> slots, double gateX, double gateZ) =>
            slots.Sort((a, b) =>
                ((a.X - gateX) * (a.X - gateX) + (a.Z - gateZ) * (a.Z - gateZ)).CompareTo((b.X - gateX) * (b.X - gateX) + (b.Z - gateZ) * (b.Z - gateZ)));

        /// <summary>
        /// With things standing in the zone a fixed grid would lose whole rows to one prop, so slots are picked from a
        /// finer lattice: every point clear of every obstacle, taken in cell order, each at least <see cref="Spacing"/>
        /// from the slots already picked. A slot's index is its lattice cell, so the same zone always numbers its slots the same way.
        /// </summary>
        static List<ParkingSlot> ObstructedSlots(Rect2 zone, double x0, double x1, double z0, double z1, double gateX, double gateZ, IReadOnlyList<Obstacle> obstacles, double footprint)
        {
            const double step = Spacing / 4.0;
            var nx = (int)Math.Floor((x1 - x0) / step) + 1;
            var nz = (int)Math.Floor((z1 - z0) / step) + 1;
            var candidates = new List<ParkingSlot>();
            for (var i = 0; i < nx; i++)
                for (var j = 0; j < nz; j++)
                {
                    var x = x0 + i * step;
                    var z = z0 + j * step;
                    if (ClearOf(obstacles, x, z, footprint)) candidates.Add(new ParkingSlot(zone.Name, i * nz + j, x, z));
                }

            var picked = new List<ParkingSlot>();
            foreach (var c in candidates)
            {
                var apart = true;
                foreach (var p in picked)
                    if ((p.X - c.X) * (p.X - c.X) + (p.Z - c.Z) * (p.Z - c.Z) < Spacing * Spacing - 1e-9) { apart = false; break; }
                if (apart) picked.Add(c);
            }

            // The pick above is the same whoever asks (cell order, not the gate's), so two machines can never be handed
            // overlapping slots; only the ORDER they are offered in follows the gate.
            picked.Sort((a, b) =>
            {
                var da = (a.X - gateX) * (a.X - gateX) + (a.Z - gateZ) * (a.Z - gateZ);
                var db = (b.X - gateX) * (b.X - gateX) + (b.Z - gateZ) * (b.Z - gateZ);
                var c = da.CompareTo(db);
                return c != 0 ? c : a.Index.CompareTo(b.Index);
            });
            return picked;
        }

        /// <summary>True when a machine sweeping a circle of <paramref name="footprint"/> about (x, z) stays <see cref="Clearance"/> from every obstacle.</summary>
        public static bool ClearOf(IReadOnlyList<Obstacle> obstacles, double x, double z, double footprint)
        {
            if (obstacles == null) return true;
            for (var i = 0; i < obstacles.Count; i++)
                if (obstacles[i].Distance(x, z) < footprint + Clearance) return false;
            return true;
        }

        static string Key(string zone, int index) => zone + "#" + index;

        /// <summary>
        /// The first slot of the zone nobody holds and nothing stands on (<paramref name="occupied"/> says whether
        /// something stands within <see cref="ClearRadius"/> of a point), claimed for <paramref name="owner"/>;
        /// null when the zone is full. An owner holds one slot: a claim that succeeds replaces its last, and one
        /// that fails leaves it where it was.
        /// </summary>
        public ParkingSlot? Claim(string owner, Rect2 zone, double gateX, double gateZ, Func<double, double, bool> occupied, double margin = Inset, IReadOnlyList<Obstacle> obstacles = null, double footprint = 0)
        {
            foreach (var slot in Slots(zone, gateX, gateZ, margin, obstacles, footprint))
            {
                var key = Key(slot.Zone, slot.Index);
                if (claims.TryGetValue(key, out var holder) && holder != owner) continue;
                if (occupied != null && occupied(slot.X, slot.Z)) continue;
                Release(owner);
                claims[key] = owner;
                byOwner[owner] = key;
                return slot;
            }

            return null;
        }

        /// <summary>Gives an owner back exactly this slot (when nobody else holds it), after a claim it could not use.</summary>
        public void Hold(string owner, ParkingSlot slot)
        {
            var key = Key(slot.Zone, slot.Index);
            if (claims.TryGetValue(key, out var holder) && holder != owner) return;
            Release(owner);
            claims[key] = owner;
            byOwner[owner] = key;
        }

        public void Release(string owner)
        {
            if (!byOwner.TryGetValue(owner, out var key)) return;
            byOwner.Remove(owner);
            claims.Remove(key);
        }

        public bool Holds(string owner) => byOwner.ContainsKey(owner);
    }
}
