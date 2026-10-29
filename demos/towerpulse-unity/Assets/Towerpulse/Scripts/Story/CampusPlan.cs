// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

using System;
using System.Collections.Generic;

namespace DeviceChain.Towerpulse.Story
{
    public sealed class PropSpot { public string Prop; public double X, Z, Yaw; }

    public sealed class ZonePlan
    {
        public string Id, Node, Anchor, Building;
        public int Floor, Capacity;
        public double FloorY, CentreX, CentreZ;
        public List<PropSpot> Furniture = new List<PropSpot>();
        public List<PropSpot> Seats = new List<PropSpot>();
    }

    public sealed class BuildingPlan
    {
        public string Id, Model;
        public double X, Y, Z;
        public int Floors;
        public double RoofY;
    }

    /// <summary>A rooftop air handler or a chiller: where it stands, and the nodes of its model that move.</summary>
    public sealed class UnitPlan
    {
        public string Id, Model, Building, Mount, LampNode;
        public double X, Y, Z, Yaw;
        public List<string> FanNodes = new List<string>();
    }

    /// <summary>
    /// The campus on paper, read from the layout file the art build writes (<c>Data/campus_layout.json</c>): where each building, zone, piece of furniture, seat
    /// and unit goes. Pure data, read strictly, so the scene builder and the tests see the same thing.
    /// </summary>
    public sealed class CampusPlan
    {
        public double FloorHeight;
        public string GroundModel;
        public List<BuildingPlan> Buildings = new List<BuildingPlan>();
        public List<ZonePlan> Zones = new List<ZonePlan>();
        public List<UnitPlan> RooftopUnits = new List<UnitPlan>();
        public List<UnitPlan> Chillers = new List<UnitPlan>();

        public ZonePlan Zone(string id)
        {
            foreach (var z in Zones) if (z.Id == id) return z;
            return null;
        }

        public BuildingPlan Building(string id)
        {
            foreach (var b in Buildings) if (b.Id == id) return b;
            return null;
        }

        public static CampusPlan Parse(string json)
        {
            var root = MiniJson.Parse(json) as Dictionary<string, object>;
            if (root == null) throw new FormatException("the layout must be a JSON object");
            var plan = new CampusPlan { FloorHeight = N(root, "floorHeight") };
            foreach (var o in Arr(root, "buildings"))
            {
                var d = Obj(o, "a building");
                var pos = Vec(d, "position", 3);
                plan.Buildings.Add(new BuildingPlan { Id = S(d, "id"), Model = S(d, "model"), X = pos[0], Y = pos[1], Z = pos[2], Floors = (int)N(d, "floors"), RoofY = N(d, "roofY") });
            }
            foreach (var o in Arr(root, "zones"))
            {
                var d = Obj(o, "a zone");
                var c = Vec(d, "centre", 2);
                var z = new ZonePlan
                {
                    Id = S(d, "id"), Node = S(d, "node"), Anchor = S(d, "anchor"), Building = S(d, "building"), Floor = (int)N(d, "floor"),
                    FloorY = N(d, "floorY"), Capacity = (int)N(d, "capacity"), CentreX = c[0], CentreZ = c[1],
                };
                foreach (var f in Arr(d, "furniture")) z.Furniture.Add(Spot(f));
                foreach (var f in Arr(d, "seats")) z.Seats.Add(Spot(f));
                plan.Zones.Add(z);
            }
            foreach (var o in Arr(root, "rooftopUnits"))
            {
                var d = Obj(o, "a rooftop unit");
                var u = Unit(d);
                u.Building = S(d, "building");
                u.Mount = S(d, "mount");
                u.FanNodes.Add(S(d, "fanNode"));
                plan.RooftopUnits.Add(u);
            }
            foreach (var o in Arr(root, "chillers"))
            {
                var d = Obj(o, "a chiller");
                var u = Unit(d);
                u.LampNode = S(d, "lampNode");
                foreach (var f in Arr(d, "fanNodes")) u.FanNodes.Add(f as string ?? throw new FormatException("a fan node must be a string"));
                plan.Chillers.Add(u);
            }
            var g = Obj(root.TryGetValue("ground", out var gv) ? gv : null, "the ground");
            plan.GroundModel = S(g, "model");
            if (plan.Zones.Count == 0) throw new FormatException("the layout has no zones");
            return plan;
        }

        /// <summary>
        /// Where people stand when a room holds more than it has seats for: spread round the zone's centre, facing in, deterministic. The caller bounds
        /// how many it asks for by what the room can plausibly hold.
        /// </summary>
        public static List<PropSpot> StandingSpots(ZonePlan zone, int count, double radius = 2.9)
        {
            var list = new List<PropSpot>();
            for (int i = 0; i < count; i++)
            {
                double a = (i + 0.5) * (2 * Math.PI / Math.Max(count, 1)) + 0.35;
                double x = zone.CentreX + Math.Cos(a) * radius, z = zone.CentreZ + Math.Sin(a) * radius;
                double yaw = Math.Atan2(zone.CentreX - x, zone.CentreZ - z) * 180.0 / Math.PI; // Unity yaw: 0 faces +Z
                list.Add(new PropSpot { Prop = "person_standing", X = x, Z = z, Yaw = yaw });
            }
            return list;
        }

        static UnitPlan Unit(Dictionary<string, object> d)
        {
            var pos = Vec(d, "position", 3);
            return new UnitPlan { Id = S(d, "id"), Model = S(d, "model"), X = pos[0], Y = pos[1], Z = pos[2], Yaw = N(d, "yaw") };
        }

        static PropSpot Spot(object o)
        {
            var d = Obj(o, "a prop");
            return new PropSpot { Prop = S(d, "prop"), X = N(d, "x"), Z = N(d, "z"), Yaw = N(d, "yaw") };
        }

        static Dictionary<string, object> Obj(object o, string what)
        {
            return o as Dictionary<string, object> ?? throw new FormatException(what + " must be an object");
        }

        static List<object> Arr(Dictionary<string, object> d, string key)
        {
            return d.TryGetValue(key, out var v) && v is List<object> l ? l : throw new FormatException("\"" + key + "\" must be an array");
        }

        static string S(Dictionary<string, object> d, string key)
        {
            return d.TryGetValue(key, out var v) && v is string s && s.Length > 0 ? s : throw new FormatException("\"" + key + "\" must be a non-empty string");
        }

        static double N(Dictionary<string, object> d, string key)
        {
            return d.TryGetValue(key, out var v) && v is double x ? x : throw new FormatException("\"" + key + "\" must be a number");
        }

        static double[] Vec(Dictionary<string, object> d, string key, int n)
        {
            if (!d.TryGetValue(key, out var v) || !(v is List<object> l) || l.Count != n) throw new FormatException("\"" + key + "\" must be " + n + " numbers");
            var r = new double[n];
            for (int i = 0; i < n; i++) r[i] = l[i] is double x ? x : throw new FormatException("\"" + key + "\" must be " + n + " numbers");
            return r;
        }
    }
}
