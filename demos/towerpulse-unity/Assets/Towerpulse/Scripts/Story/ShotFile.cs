// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

using System;
using System.Collections.Generic;
using System.Globalization;

namespace DeviceChain.Towerpulse.Story
{
    /// <summary>A camera pose: where it stands, what it looks at, and its vertical field of view in degrees.</summary>
    public readonly struct CameraPose
    {
        public CameraPose(double px, double py, double pz, double lx, double ly, double lz, double fov)
        {
            Px = px; Py = py; Pz = pz; Lx = lx; Ly = ly; Lz = lz; Fov = fov;
        }

        public double Px { get; }
        public double Py { get; }
        public double Pz { get; }
        public double Lx { get; }
        public double Ly { get; }
        public double Lz { get; }
        public double Fov { get; }

        public static CameraPose Lerp(CameraPose a, CameraPose b, double t)
        {
            return new CameraPose(
                a.Px + (b.Px - a.Px) * t, a.Py + (b.Py - a.Py) * t, a.Pz + (b.Pz - a.Pz) * t,
                a.Lx + (b.Lx - a.Lx) * t, a.Ly + (b.Ly - a.Ly) * t, a.Lz + (b.Lz - a.Lz) * t,
                a.Fov + (b.Fov - a.Fov) * t);
        }
    }

    /// <summary>One beat of the storyboard: a camera move over a span of show time, and the cards that stand in the frame while it plays.</summary>
    public sealed class Shot
    {
        public string Id;
        /// <summary>The line of the storyboard this shot serves.</summary>
        public string Beat;
        public string Caption;
        public double StartS;
        public double DurationS;
        public CameraPose From;
        public CameraPose To;
        public bool Smooth;
        public List<string> Cards = new List<string>();

        public double EndS { get { return StartS + DurationS; } }
    }

    public sealed class ShotList
    {
        public const int Version = 1;
        /// <summary>The cards a shot may show, by the name the shot file gives them.</summary>
        public static readonly string[] KnownCards = { "zone", "alarm", "command", "chiller" };

        public List<Shot> Shots = new List<Shot>();

        public double TotalS { get { return Shots.Count == 0 ? 0 : Shots[Shots.Count - 1].EndS; } }

        /// <summary>The shot playing at a show time (the last one's final frame is held past the end), or null before the first.</summary>
        public Shot At(double showS)
        {
            if (Shots.Count == 0 || showS < Shots[0].StartS) return null;
            for (int i = 0; i < Shots.Count; i++) if (showS < Shots[i].EndS) return showS >= Shots[i].StartS ? Shots[i] : Shots[i > 0 ? i - 1 : 0];
            return Shots[Shots.Count - 1];
        }

        /// <summary>The camera at a show time: inside a shot, its eased move; in a gap, the previous shot's final pose (it never jumps).</summary>
        public bool TryPose(double showS, out CameraPose pose, out Shot shot)
        {
            shot = At(showS);
            if (shot == null) { pose = default; return false; }
            double t = (showS - shot.StartS) / shot.DurationS;
            if (t < 0) t = 0;
            if (t > 1) t = 1;
            if (shot.Smooth) t = t * t * (3 - 2 * t);
            pose = CameraPose.Lerp(shot.From, shot.To, t);
            return true;
        }
    }

    /// <summary>
    /// The shot file: JSON, version 1, one array of shots. It is read strictly. Every field that is present must be of the right type, every unknown key
    /// is an error (a misspelled key must not quietly mean "default"), and the whole file is checked before any of it is used.
    ///
    ///   { "version": 1, "shots": [ { "id": "...", "beat": "...", "caption": "...", "startS": 0, "durationS": 8,
    ///       "ease": "smooth|linear", "cards": ["zone"], "from": POSE, "to": POSE } ] }
    ///   POSE = { "pos": [x, y, z], "look": [x, y, z], "fov": 40 }
    /// </summary>
    public static class ShotFile
    {
        public const double MinFov = 10, MaxFov = 100, MaxShowSeconds = 600;

        public sealed class Result
        {
            public ShotList List;
            public readonly List<string> Errors = new List<string>();
            public bool Ok { get { return Errors.Count == 0 && List != null; } }
            public string Report() { return string.Join("; ", Errors); }
        }

        static readonly HashSet<string> RootKeys = new HashSet<string> { "version", "shots", "note" };
        static readonly HashSet<string> ShotKeys = new HashSet<string> { "id", "beat", "caption", "startS", "durationS", "ease", "cards", "from", "to" };
        static readonly HashSet<string> PoseKeys = new HashSet<string> { "pos", "look", "fov" };

        public static Result Parse(string json)
        {
            var res = new Result();
            object root;
            try { root = MiniJson.Parse(json); }
            catch (MiniJson.ParseException e) { res.Errors.Add("not valid JSON: " + e.Message); return res; }

            if (!(root is Dictionary<string, object> obj)) { res.Errors.Add("the shot file must be a JSON object"); return res; }
            Unknown(obj, RootKeys, "the file", res);
            if (!obj.TryGetValue("version", out var v) || !(v is double dv) || dv != ShotList.Version)
                res.Errors.Add("\"version\" must be " + ShotList.Version);
            if (!obj.TryGetValue("shots", out var s) || !(s is List<object> shots)) { res.Errors.Add("\"shots\" must be an array"); return res; }
            if (shots.Count == 0) res.Errors.Add("\"shots\" is empty: there is nothing to play");

            var list = new ShotList();
            var ids = new HashSet<string>(StringComparer.Ordinal);
            for (int i = 0; i < shots.Count; i++)
            {
                string where = "shot " + i;
                if (!(shots[i] is Dictionary<string, object> so)) { res.Errors.Add(where + " must be an object"); continue; }
                var shot = ReadShot(so, where, res);
                if (shot == null) continue;
                where = "shot \"" + shot.Id + "\"";
                if (!ids.Add(shot.Id)) res.Errors.Add(where + ": the id is used twice");
                if (list.Shots.Count > 0)
                {
                    var prev = list.Shots[list.Shots.Count - 1];
                    if (shot.StartS < prev.EndS - 1e-9) res.Errors.Add(where + ": starts at " + Fmt(shot.StartS) + " s, before \"" + prev.Id + "\" ends at " + Fmt(prev.EndS) + " s");
                }
                list.Shots.Add(shot);
            }
            if (list.TotalS > MaxShowSeconds) res.Errors.Add("the shots run " + Fmt(list.TotalS) + " s, over the " + MaxShowSeconds + " s limit");
            res.List = list;
            return res;
        }

        static Shot ReadShot(Dictionary<string, object> o, string where, Result res)
        {
            int before = res.Errors.Count;
            Unknown(o, ShotKeys, where, res);
            var shot = new Shot();
            shot.Id = Str(o, "id", where, res, true);
            if (!string.IsNullOrEmpty(shot.Id)) where = "shot \"" + shot.Id + "\"";
            shot.Beat = Str(o, "beat", where, res, false);
            shot.Caption = Str(o, "caption", where, res, false);
            shot.StartS = Num(o, "startS", where, res);
            shot.DurationS = Num(o, "durationS", where, res);
            if (!double.IsNaN(shot.StartS) && shot.StartS < 0) res.Errors.Add(where + ": \"startS\" must not be negative");
            if (!double.IsNaN(shot.DurationS) && shot.DurationS <= 0) res.Errors.Add(where + ": \"durationS\" must be greater than zero");

            var ease = Str(o, "ease", where, res, false);
            if (ease == null || ease == "smooth") shot.Smooth = true;
            else if (ease == "linear") shot.Smooth = false;
            else res.Errors.Add(where + ": \"ease\" must be \"smooth\" or \"linear\", not \"" + ease + "\"");

            if (o.TryGetValue("cards", out var c))
            {
                if (!(c is List<object> cl)) res.Errors.Add(where + ": \"cards\" must be an array");
                else
                    foreach (var item in cl)
                    {
                        var name = item as string;
                        if (name == null || Array.IndexOf(ShotList.KnownCards, name) < 0)
                            res.Errors.Add(where + ": unknown card " + (name == null ? "(not a string)" : "\"" + name + "\"") + "; known: " + string.Join(", ", ShotList.KnownCards));
                        else if (shot.Cards.Contains(name)) res.Errors.Add(where + ": card \"" + name + "\" is listed twice");
                        else shot.Cards.Add(name);
                    }
            }

            shot.From = Pose(o, "from", where, res);
            shot.To = Pose(o, "to", where, res);
            return res.Errors.Count == before ? shot : null;
        }

        static CameraPose Pose(Dictionary<string, object> o, string key, string where, Result res)
        {
            string w = where + " \"" + key + "\"";
            if (!o.TryGetValue(key, out var p) || !(p is Dictionary<string, object> po)) { res.Errors.Add(w + " must be an object"); return default; }
            Unknown(po, PoseKeys, w, res);
            var pos = Vec(po, "pos", w, res);
            var look = Vec(po, "look", w, res);
            double fov = Num(po, "fov", w, res);
            if (!double.IsNaN(fov) && (fov < MinFov || fov > MaxFov)) res.Errors.Add(w + ": \"fov\" must be between " + MinFov + " and " + MaxFov + " degrees");
            if (pos != null && look != null && Math.Abs(pos[0] - look[0]) < 1e-9 && Math.Abs(pos[1] - look[1]) < 1e-9 && Math.Abs(pos[2] - look[2]) < 1e-9)
                res.Errors.Add(w + ": the camera looks at its own position");
            if (pos == null || look == null) return default;
            return new CameraPose(pos[0], pos[1], pos[2], look[0], look[1], look[2], fov);
        }

        static double[] Vec(Dictionary<string, object> o, string key, string where, Result res)
        {
            if (!o.TryGetValue(key, out var v) || !(v is List<object> l) || l.Count != 3) { res.Errors.Add(where + ": \"" + key + "\" must be three numbers"); return null; }
            var d = new double[3];
            for (int i = 0; i < 3; i++)
            {
                if (!(l[i] is double x)) { res.Errors.Add(where + ": \"" + key + "\" must be three numbers"); return null; }
                d[i] = x;
            }
            return d;
        }

        static double Num(Dictionary<string, object> o, string key, string where, Result res)
        {
            if (!o.TryGetValue(key, out var v) || !(v is double d)) { res.Errors.Add(where + ": \"" + key + "\" must be a number"); return double.NaN; }
            return d;
        }

        static string Str(Dictionary<string, object> o, string key, string where, Result res, bool required)
        {
            if (!o.TryGetValue(key, out var v))
            {
                if (required) res.Errors.Add(where + ": \"" + key + "\" is required");
                return null;
            }
            if (!(v is string s) || (required && s.Length == 0)) { res.Errors.Add(where + ": \"" + key + "\" must be a" + (required ? " non-empty" : "") + " string"); return null; }
            return s;
        }

        static void Unknown(Dictionary<string, object> o, HashSet<string> known, string where, Result res)
        {
            foreach (var k in o.Keys) if (!known.Contains(k)) res.Errors.Add(where + ": unknown key \"" + k + "\"");
        }

        static string Fmt(double d) { return d.ToString("0.###", CultureInfo.InvariantCulture); }
    }
}
