// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

using System;
using System.IO;
using System.Linq;
using DeviceChain.Harborpulse.Model;
using DeviceChain.Harborpulse.Model.Director;
using NUnit.Framework;

namespace DeviceChain.Harborpulse.Tests
{
    /// <summary>
    /// A maintainer's tool, run as a test: point HARBORPULSE_RECORDING at a live day's recording directory and it plays both
    /// cuts over it and prints each caption's fate (shown, or unmet and when the day would first have supported it), which is
    /// what a cut's spans are authored against, and each voice-over line's take with the claims that failed a stronger one.
    /// HARBORPULSE_COMPACT=path also writes the day as one compact stream; HARBORPULSE_TRACE_DIR=dir writes each cut's
    /// frame_check.json and vo_cues.json; HARBORPULSE_VO_CUES=dir checks an assembly's takes against the day's gates and fails on a
    /// mismatch; HARBORPULSE_FIRST="page:claim;..." prints the first recorded second each claim holds;
    /// HARBORPULSE_EXPLAIN="page:claim@second" says why a claim holds or does not at a recorded second.
    /// </summary>
    public class RecordedDayTool
    {
        public static RecordedDay Load(string dir)
        {
            if (File.Exists(dir)) using (var one = new StreamReader(dir)) return RecordedDay.Load(one);
            using (var observed = new StreamReader(Path.Combine(dir, "observed.ndjson"))) using (var world = new StreamReader(Path.Combine(dir, "world.ndjson")))
                return RecordedDay.Load(observed, world);
        }
        [Test]
        public void TraceBothCutsOverARecordedDay()
        {
            var dir = Environment.GetEnvironmentVariable("HARBORPULSE_RECORDING");
            if (string.IsNullOrEmpty(dir)) Assert.Ignore("set HARBORPULSE_RECORDING to a recording directory");
            var day = Load(dir); var map = new PresentationMap(Kit.Layout()); var story = Storyboard.Parse(DirectorKit.StoryText(), map);
            var compact = Environment.GetEnvironmentVariable("HARBORPULSE_COMPACT");
            if (!string.IsNullOrEmpty(compact)) using (var output = new StreamWriter(compact) { NewLine = "\n" }) day.WriteCompact(output);
            Console.WriteLine("run " + day.Run + " · " + day.EndS.ToString("0.0") + " s · sim " + day.SimAt(0).ToString("0.00") + " to " + day.SimAt(day.EndS).ToString("0.00"));
            foreach (var cut in new[] { "c90", "c300" })
            {
                var player = new CutPlayer(story, cut, day, map);
                for (var t = 0.0; t < player.DurationS; t += 1.0 / 30) player.Frame(t);
                Console.WriteLine("== " + cut);
                foreach (var e in player.Trace)
                    Console.WriteLine(e.ShowS.ToString("000.0") + " " + e.Page.PadRight(3) + e.Shot.PadRight(6) + e.Caption.PadRight(15) + (e.Shown ? "SHOWN  " + e.Text.Replace("\n", " / ") : "--     " + e.Reason + (e.FirstHoldsAtS.HasValue ? " (first holds at " + e.FirstHoldsAtS + ")" : e.Reason.StartsWith("the recording") ? " (never in this day)" : "")));
                foreach (var c in player.Vo)
                {
                    Console.WriteLine("vo " + c.ShowS.ToString("000.0") + " " + c.Line.PadRight(10) + c.Take.PadRight(18) + c.Text);
                    foreach (var t in c.Takes.Where(t => !t.Licensed)) foreach (var claim in t.Claims.Where(x => !x.Held)) Console.WriteLine("     not " + t.Take + ": " + claim.Claim + ": " + claim.Reason);
                }
                var problems = player.VoProblems(); Console.WriteLine("vo check: " + (problems.Count == 0 ? "pass" : string.Join(" | ", problems)));
                var out2 = Environment.GetEnvironmentVariable("HARBORPULSE_TRACE_DIR");
                if (!string.IsNullOrEmpty(out2)) { File.WriteAllText(Path.Combine(out2, cut + "-frame_check.json"), player.TraceJson()); File.WriteAllText(Path.Combine(out2, cut + "-vo_cues.json"), player.VoCuesJson()); }
                // HARBORPULSE_VO_CUES=dir: check an assembly's cues (dir/<cut>-vo_cues.json, or dir/vo_cues.json for one cut) against the day
                var played = Environment.GetEnvironmentVariable("HARBORPULSE_VO_CUES");
                if (!string.IsNullOrEmpty(played))
                {
                    var file = File.Exists(Path.Combine(played, cut + "-vo_cues.json")) ? Path.Combine(played, cut + "-vo_cues.json") : Path.Combine(played, "vo_cues.json");
                    var found = player.VoProblems(CutPlayer.ReadVoCues(File.ReadAllText(file)));
                    Console.WriteLine("vo cues " + file + ": " + (found.Count == 0 ? "pass" : string.Join(" | ", found)));
                    Assert.That(found, Is.Empty, "the takes played do not match the gate");
                }
                // HARBORPULSE_FIRST="page:claim;...": the first recorded second (to a quarter) at which each claim holds: what a shot's span is set from
                foreach (var ask in (Environment.GetEnvironmentVariable("HARBORPULSE_FIRST") ?? "").Split(new[] { ';' }, StringSplitOptions.RemoveEmptyEntries))
                {
                    if (cut != "c300") continue;
                    var parts = ask.Split(':'); double? at = null;
                    for (var t = 0.0; t <= day.EndS && at == null; t += 0.25) if (player.Explain(parts[0], parts[1], t).Split((char)10)[0].Contains(" HOLDS")) at = t;
                    Console.WriteLine("first " + ask.PadRight(28) + (at.HasValue ? at.Value.ToString("0.00", System.Globalization.CultureInfo.InvariantCulture) : "never"));
                }
                // HARBORPULSE_EXPLAIN="page:claim@wallS;...": why a claim holds or does not at a recorded second
                foreach (var ask in (Environment.GetEnvironmentVariable("HARBORPULSE_EXPLAIN") ?? "").Split(new[] { ';' }, StringSplitOptions.RemoveEmptyEntries))
                {
                    if (cut != "c300") continue;
                    var parts = ask.Split(':', '@');
                    Console.WriteLine("explain " + ask + "\n  " + player.Explain(parts[0], parts[1], double.Parse(parts[2], System.Globalization.CultureInfo.InvariantCulture)).Replace("\n", "\n  "));
                }
            }
        }
    }
}
