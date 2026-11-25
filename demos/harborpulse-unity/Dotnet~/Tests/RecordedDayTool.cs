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
    /// what a cut's spans are authored against. HARBORPULSE_COMPACT=path also writes the day as one compact stream.
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
                var out2 = Environment.GetEnvironmentVariable("HARBORPULSE_TRACE_DIR");
                if (!string.IsNullOrEmpty(out2)) File.WriteAllText(Path.Combine(out2, cut + "-frame_check.json"), player.TraceJson());
            }
        }
    }
}
