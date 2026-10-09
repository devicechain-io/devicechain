// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

using System;
using System.Collections.Generic;
using System.Globalization;
using System.Linq;
using System.Text.Json;
using DeviceChain.Sim.Traffic;

namespace DeviceChain.Sitepulse.App
{
    /// <summary>
    /// Reads the site topology (<c>quarry_topology.json</c>, written by <c>ArtSource/terrain/quarry_topology.py</c>) into a
    /// <see cref="SiteTopology"/>. Hand-read with <see cref="JsonElement"/>, as <see cref="SiteGeometryReader"/> is, and as strict: a missing
    /// block, a key it does not know, a cell array of the wrong arity, a run of cells outside its lane or a reference to a lane, stand or
    /// junction the file does not hold is refused, loudly, with what and where.
    /// </summary>
    public static class SiteTopologyReader
    {
        static readonly string[] RootKeys =
        {
            "generator", "source", "cell_m", "slot_m", "rules", "fleet", "zones", "lanes", "exits", "entries", "junctions", "stands", "bay", "stations", "work_areas", "exempt",
        };

        public static SiteTopology Parse(string json)
        {
            if (string.IsNullOrWhiteSpace(json)) throw new FormatException("the topology file is empty");
            using var doc = JsonDocument.Parse(json);
            var root = doc.RootElement;
            if (root.ValueKind != JsonValueKind.Object) throw new FormatException("the topology file is not an object");
            Keys(root, "the topology", RootKeys, RootKeys);

            var src = root.GetProperty("source");
            Keys(src, "source", new[] { "features", "heights", "fleet", "fleet_preview" }, new[] { "features", "heights", "fleet", "fleet_preview" });
            var source = new SourceHashes(Str(src, "features"), Str(src, "heights"), Str(src, "fleet"), Str(src, "fleet_preview"));

            var rj = root.GetProperty("rules");
            var ruleKeys = new[] { "conflict_haul_m", "conflict_access_m", "conflict_same_route_m", "pose_step_m", "pose_step_deg", "diverge_window", "diverge_max", "stand_seen_m" };
            Keys(rj, "rules", ruleKeys, ruleKeys);
            var rules = new TopologyRules(Num(rj, "conflict_haul_m"), Num(rj, "conflict_access_m"), Num(rj, "conflict_same_route_m"), Num(rj, "pose_step_m"),
                Num(rj, "pose_step_deg"), Int(rj, "diverge_window"), Int(rj, "diverge_max"), Num(rj, "stand_seen_m"));
            if (rules.PoseStepM <= 0.0 || rules.PoseStepDegrees <= 0.0) throw new FormatException("rules: a pose step is not a positive number");

            var fl = root.GetProperty("fleet");
            Keys(fl, "fleet", new[] { "loop_trucks", "machines", "period_s" }, new[] { "loop_trucks", "machines", "period_s" });
            var fleet = new FleetBlock(Int(fl, "loop_trucks"), Int(fl, "machines"), Num(fl, "period_s"));

            var zones = new List<ZoneKinds>();
            foreach (var z in Array(root, "zones"))
            {
                Keys(z, "a zone", new[] { "token", "kinds" }, new[] { "token", "kinds" });
                zones.Add(new ZoneKinds(Str(z, "token"), Strings(z, "kinds")));
            }

            var lanes = new List<Lane>();
            var ids = new HashSet<string>(StringComparer.Ordinal);
            foreach (var l in Array(root, "lanes"))
            {
                Keys(l, "a lane", new[] { "id", "kind", "cyclic", "neighbour_span_m", "cells" }, new[] { "id", "kind", "cyclic", "neighbour_span_m", "cells", "track", "road" });
                var id = Str(l, "id");
                if (!ids.Add(id)) throw new FormatException("lane " + id + " is listed twice");
                var kind = Str(l, "kind");
                if (kind != "loop" && kind != "road" && kind != "access") throw new FormatException("lane " + id + " has an unknown kind \"" + kind + "\"");
                var arity = kind == "loop" ? 5 : 4;
                var cells = new List<Cell>();
                var k = 0;
                foreach (var c in Array(l, "cells"))
                {
                    if (c.ValueKind != JsonValueKind.Array || c.GetArrayLength() != arity)
                        throw new FormatException("lane " + id + " cell " + k + " is not " + arity + " numbers");
                    cells.Add(new Cell(c[0].GetDouble(), c[1].GetDouble(), c[2].GetDouble(), c[3].GetDouble(), arity == 5 ? c[4].GetDouble() : double.NaN));
                    k++;
                }

                if (cells.Count == 0) throw new FormatException("lane " + id + " has no cells");
                var cyclic = l.GetProperty("cyclic").GetBoolean();
                if (cyclic && kind != "loop") throw new FormatException("lane " + id + " is cyclic and is not the loop");
                if (!cyclic && kind == "loop") throw new FormatException("lane " + id + " is the loop and is not cyclic (the capacity checks would pass vacuously)");
                lanes.Add(new Lane(id, kind, cyclic, cells, l.TryGetProperty("track", out var tr) ? tr.GetInt32() : -1, l.TryGetProperty("road", out var rd) ? rd.GetString() : null,
                    Num(l, "neighbour_span_m")));
            }

            if (lanes.Count(l => l.Kind == "loop") != 1) throw new FormatException("the topology holds " + lanes.Count(l => l.Kind == "loop") + " loop lanes (it needs exactly one)");
            var counts = lanes.ToDictionary(l => l.Id, l => l.Cells.Count, StringComparer.Ordinal);

            var exits = new List<LaneExit>();
            foreach (var e in Array(root, "exits"))
            {
                Keys(e, "an exit", new[] { "id", "lane", "cell", "to", "shared" }, new[] { "id", "lane", "cell", "to", "shared" });
                var lane = LaneRef(e, "lane", counts, "exit " + Str(e, "id"));
                var to = LaneRef(e, "to", counts, "exit " + Str(e, "id"));
                exits.Add(new LaneExit(Str(e, "id"), lane, CellRef(e, "cell", counts[lane], "exit " + Str(e, "id")), to, Int(e, "shared")));
            }

            var junctions = new List<Junction>();
            var jids = new HashSet<string>(StringComparer.Ordinal);
            foreach (var j in Array(root, "junctions"))
            {
                Keys(j, "a junction", new[] { "id", "kind", "polygon", "members", "approach", "room" }, new[] { "id", "kind", "polygon", "members", "approach", "room" });
                var jid = Str(j, "id");
                if (!jids.Add(jid)) throw new FormatException("junction " + jid + " is listed twice");
                var kind = Str(j, "kind");
                if (kind != "merge" && kind != "crossing" && kind != "oncoming") throw new FormatException("junction " + jid + " has an unknown kind \"" + kind + "\"");
                junctions.Add(new Junction(jid, kind, ReadPolygon(j.GetProperty("polygon"), "junction " + jid), Runs(j, "members", counts, lanes, "junction " + jid),
                    Array(j, "approach").Select(a =>
                    {
                        Keys(a, "an approach", new[] { "lane", "cell" }, new[] { "lane", "cell" });
                        var lane = LaneRef(a, "lane", counts, "junction " + jid + " approach");
                        return new LaneCell(lane, CellRef(a, "cell", counts[lane], "junction " + jid + " approach"));
                    }).ToList(), Runs(j, "room", counts, lanes, "junction " + jid)));
            }

            var entries = new List<LaneEntry>();
            foreach (var e in Array(root, "entries"))
            {
                Keys(e, "an entry", new[] { "id", "from", "lane", "cell", "junction" }, new[] { "id", "from", "lane", "cell", "junction" });
                var lane = LaneRef(e, "lane", counts, "entry " + Str(e, "id"));
                var jn = e.GetProperty("junction");
                var jname = jn.ValueKind == JsonValueKind.Null ? null : jn.GetString();
                if (jname != null && !jids.Contains(jname)) throw new FormatException("entry " + Str(e, "id") + " names junction " + jname + ", which the file does not hold");
                entries.Add(new LaneEntry(Str(e, "id"), LaneRef(e, "from", counts, "entry " + Str(e, "id")), lane, CellRef(e, "cell", counts[lane], "entry " + Str(e, "id")), jname));
            }

            var stands = new List<StandPlace>();
            var sids = new HashSet<string>(StringComparer.Ordinal);
            foreach (var s in Array(root, "stands"))
            {
                Keys(s, "a stand", new[] { "id", "role", "zone", "kinds", "pose", "in", "out", "clearance_m" }, new[] { "id", "role", "zone", "kinds", "pose", "in", "out", "clearance_m" });
                var sid = Str(s, "id");
                if (!sids.Add(sid)) throw new FormatException("stand " + sid + " is listed twice");
                var role = Str(s, "role");
                if (role != "zone" && role != "service") throw new FormatException("stand " + sid + " has an unknown role \"" + role + "\"");
                var zone = Str(s, "zone");
                if (!zones.Any(z => z.Token == zone)) throw new FormatException("stand " + sid + " is in zone " + zone + ", which the file does not list");
                var pose = s.GetProperty("pose");
                if (pose.ValueKind != JsonValueKind.Array || pose.GetArrayLength() != 3) throw new FormatException("stand " + sid + " pose is not [x, z, heading]");
                stands.Add(new StandPlace(sid, role, zone, Strings(s, "kinds"), pose[0].GetDouble(), pose[1].GetDouble(), pose[2].GetDouble(),
                    LaneRef(s, "in", counts, "stand " + sid), LaneRef(s, "out", counts, "stand " + sid), Num(s, "clearance_m")));
            }

            Bay bay = null;
            var bj = root.GetProperty("bay");
            if (bj.ValueKind != JsonValueKind.Null)
            {
                Keys(bj, "the bay", new[] { "queue", "bay", "in", "hop", "out" }, new[] { "queue", "bay", "in", "hop", "out" });
                var queue = Str(bj, "queue");
                var bayId = Str(bj, "bay");
                if (!sids.Contains(queue) || !sids.Contains(bayId)) throw new FormatException("the bay names a stand the file does not hold");
                bay = new Bay(queue, bayId, LaneRef(bj, "in", counts, "the bay"), LaneRef(bj, "hop", counts, "the bay"), LaneRef(bj, "out", counts, "the bay"));
            }

            var stations = new List<StationSpec>();
            foreach (var s in Array(root, "stations"))
            {
                Keys(s, "a station", new[] { "id", "lane", "core", "buffer", "capacity", "window_s", "headway_s", "ride_through", "partner", "min_gap_m", "holds" },
                    new[] { "id", "lane", "core", "buffer", "capacity", "window_s", "headway_s", "ride_through", "partner", "min_gap_m", "holds" });
                var lane = LaneRef(s, "lane", counts, "station " + Str(s, "id"));
                StationPartner partner = null;
                var pj = s.GetProperty("partner");
                if (pj.ValueKind != JsonValueKind.Null)
                {
                    Keys(pj, "a station's partner", new[] { "machine", "track", "ready_s", "ready_pose", "min_gap_m" }, new[] { "machine", "track", "ready_s", "ready_pose", "min_gap_m" });
                    var rp = pj.GetProperty("ready_pose");
                    if (rp.ValueKind != JsonValueKind.Array || rp.GetArrayLength() != 3) throw new FormatException("station " + Str(s, "id") + " ready_pose is not [x, z, heading]");
                    partner = new StationPartner(Str(pj, "machine"), Int(pj, "track"), Num(pj, "ready_s"), rp[0].GetDouble(), rp[1].GetDouble(), rp[2].GetDouble(), Num(pj, "min_gap_m"));
                }

                stations.Add(new StationSpec(Str(s, "id"), lane, Run(s.GetProperty("core"), counts[lane], "station " + Str(s, "id") + " core"),
                    Run(s.GetProperty("buffer"), counts[lane], "station " + Str(s, "id") + " buffer"), Int(s, "capacity"), Num(s, "window_s"), Num(s, "headway_s"),
                    s.GetProperty("ride_through").GetBoolean(), partner, Num(s, "min_gap_m"), Array(s, "holds").Select(h => h.GetString()).ToList()));
            }

            var work = new List<WorkArea>();
            foreach (var w in Array(root, "work_areas"))
            {
                Keys(w, "a work area", new[] { "machine", "polygon" }, new[] { "machine", "polygon" });
                work.Add(new WorkArea(Str(w, "machine"), ReadPolygon(w.GetProperty("polygon"), "work area " + Str(w, "machine"))));
            }

            var exempt = new List<Exemption>();
            foreach (var e in Array(root, "exempt"))
            {
                Keys(e, "an exemption", new[] { "to" }, new[] { "to", "obstacle", "spot", "radius_m", "machine" });
                var to = Str(e, "to");
                if (!ids.Contains(to) && !sids.Contains(to)) throw new FormatException("an exemption is for " + to + ", which is neither a lane nor a stand of the file");
                var hasSpot = e.TryGetProperty("spot", out var spot) && spot.ValueKind == JsonValueKind.Array;
                exempt.Add(new Exemption(to, e.TryGetProperty("obstacle", out var ob) ? ob.GetString() : null, hasSpot, hasSpot ? spot[0].GetDouble() : 0.0,
                    hasSpot ? spot[1].GetDouble() : 0.0, e.TryGetProperty("radius_m", out var r) ? r.GetDouble() : 0.0, e.TryGetProperty("machine", out var m) ? m.GetString() : null));
            }

            return new SiteTopology(Str(root, "generator"), source, Num(root, "cell_m"), Num(root, "slot_m"), rules, fleet, zones, lanes, exits, entries, junctions, stands, bay, stations, work, exempt);
        }

        // ---- reading

        static IEnumerable<JsonElement> Array(JsonElement parent, string name)
        {
            if (!parent.TryGetProperty(name, out var el) || el.ValueKind != JsonValueKind.Array) throw new FormatException("the topology has no \"" + name + "\" array");
            return el.EnumerateArray();
        }

        static string Str(JsonElement parent, string name)
        {
            if (!parent.TryGetProperty(name, out var el) || el.ValueKind != JsonValueKind.String) throw new FormatException("\"" + name + "\" is not a string");
            return el.GetString();
        }

        static double Num(JsonElement parent, string name)
        {
            if (!parent.TryGetProperty(name, out var el) || el.ValueKind != JsonValueKind.Number) throw new FormatException("\"" + name + "\" is not a number");
            return el.GetDouble();
        }

        static int Int(JsonElement parent, string name)
        {
            if (!parent.TryGetProperty(name, out var el) || el.ValueKind != JsonValueKind.Number || !el.TryGetInt32(out var v)) throw new FormatException("\"" + name + "\" is not an integer");
            return v;
        }

        static List<string> Strings(JsonElement parent, string name)
        {
            var list = new List<string>();
            foreach (var e in Array(parent, name))
            {
                if (e.ValueKind != JsonValueKind.String) throw new FormatException("\"" + name + "\" holds something that is not a string");
                list.Add(e.GetString());
            }

            return list;
        }

        /// <summary>Refuses an object that lacks a required key or holds one that is not allowed.</summary>
        static void Keys(JsonElement obj, string what, string[] required, string[] allowed)
        {
            if (obj.ValueKind != JsonValueKind.Object) throw new FormatException(what + " is not an object");
            foreach (var p in obj.EnumerateObject())
                if (!allowed.Contains(p.Name)) throw new FormatException(what + " has a key this reader does not know: \"" + p.Name + "\"");
            foreach (var key in required)
                if (!obj.TryGetProperty(key, out _)) throw new FormatException(what + " has no \"" + key + "\"");
        }

        static string LaneRef(JsonElement parent, string name, Dictionary<string, int> counts, string what)
        {
            var id = Str(parent, name);
            if (!counts.ContainsKey(id)) throw new FormatException(what + " names lane " + id + ", which the file does not hold");
            return id;
        }

        static int CellRef(JsonElement parent, string name, int count, string what)
        {
            var c = Int(parent, name);
            if (c < 0 || c >= count) throw new FormatException(what + " names cell " + c.ToString(CultureInfo.InvariantCulture) + " of a lane of " + count + " cells");
            return c;
        }

        static CellRun Run(JsonElement el, int count, string what)
        {
            if (el.ValueKind != JsonValueKind.Array || el.GetArrayLength() != 2) throw new FormatException(what + " is not [first cell, last cell]");
            int a = el[0].GetInt32(), b = el[1].GetInt32();
            if (a < 0 || b < 0 || a >= count || b >= count) throw new FormatException(what + " names cells " + a + " to " + b + " of a lane of " + count + " cells");
            return new CellRun(a, b);
        }

        static IReadOnlyList<LaneRun> Runs(JsonElement parent, string name, Dictionary<string, int> counts, List<Lane> lanes, string what)
        {
            var list = new List<LaneRun>();
            foreach (var m in Array(parent, name))
            {
                Keys(m, what + " " + name, new[] { "lane", "cells" }, new[] { "lane", "cells" });
                var lane = LaneRef(m, "lane", counts, what + " " + name);
                var run = Run(m.GetProperty("cells"), counts[lane], what + " " + name + " cells");
                if (run.A > run.B && !lanes.First(l => l.Id == lane).Cyclic) throw new FormatException(what + " " + name + " wraps on a lane that is not cyclic");
                list.Add(new LaneRun(lane, run));
            }

            return list;
        }

        static Polygon ReadPolygon(JsonElement el, string what)
        {
            if (el.ValueKind != JsonValueKind.Array || el.GetArrayLength() < 3) throw new FormatException(what + " polygon has fewer than three corners");
            var xs = new double[el.GetArrayLength()];
            var zs = new double[xs.Length];
            var i = 0;
            foreach (var p in el.EnumerateArray())
            {
                if (p.ValueKind != JsonValueKind.Array || p.GetArrayLength() != 2) throw new FormatException(what + " polygon corner is not [x, z]");
                xs[i] = p[0].GetDouble();
                zs[i] = p[1].GetDouble();
                i++;
            }

            return new Polygon(xs, zs);
        }
    }
}
