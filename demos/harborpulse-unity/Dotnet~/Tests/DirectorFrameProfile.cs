// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

using System;
using System.Collections.Generic;
using System.Diagnostics;
using System.Globalization;
using System.IO;
using DeviceChain.Harborpulse.Model;
using DeviceChain.Harborpulse.Model.Director;
using NUnit.Framework;

namespace DeviceChain.Harborpulse.Tests
{
    /// <summary>
    /// Not a gate: a stopwatch over the fixture day, for whoever next asks where a live frame's model time goes. It ticks the
    /// observed model, the observer, and the scheduler with its recorder the way the player does, and prints the mean cost of
    /// one tick of each, minute by minute, with the bytes a tick allocates. It is skipped unless HP_FRAME_PROFILE=1, because
    /// wall time on a shared machine is no assertion (the gate is the record count in DirectorObserverChangeTests):
    /// <c>HP_FRAME_PROFILE=1 dotnet test -c Release --filter FullyQualifiedName~DirectorFrameProfile --logger "console;verbosity=detailed"</c>.
    /// HP_FRAME_PROFILE_FPS sets the frame rate (20), HP_FRAME_PROFILE_EVERY_JOIN=1 runs every join on every frame.
    /// </summary>
    public class DirectorFrameProfile
    {
        sealed class Timed : IDirectorFeed
        {
            readonly IDirectorFeed inner; readonly Stopwatch clock = new Stopwatch();
            public double Ms;
            public Timed(IDirectorFeed inner) { this.inner = inner; }
            public IReadOnlyList<string> Problems => inner.Problems;
            public void Update(DirectorInputs inputs, Action<Observation> observe, Action<CoverageInterval> cover)
            { clock.Restart(); try { inner.Update(inputs, observe, cover); } finally { Ms += clock.Elapsed.TotalMilliseconds; } }
        }
        [Test]
        public void PrintsPerTickCostByMinute()
        {
            if (Environment.GetEnvironmentVariable("HP_FRAME_PROFILE") != "1") Assert.Ignore("set HP_FRAME_PROFILE=1 to print the profile");
            var fps = double.Parse(Environment.GetEnvironmentVariable("HP_FRAME_PROFILE_FPS") ?? "20", CultureInfo.InvariantCulture);
            var day = new DirectorFixtureDay(); DirectorReadSnapshot snapshot = null;
            var observer = new DirectorObserver(day.Layout, () => snapshot) { RunEveryJoin = Environment.GetEnvironmentVariable("HP_FRAME_PROFILE_EVERY_JOIN") == "1" };
            var feed = new Timed(observer);
            var host = new DirectorHost(DirectorKit.Story(), day.Layout, new PresentationMap(day.Layout), feed, new Recorder(TextWriter.Null, TextWriter.Null, TextWriter.Null, TextWriter.Null));
            var model = new HarborObservedModel(day.Layout);
            var clock = new Stopwatch(); double modelMs = 0, hostMs = 0, worst = 0; var ticks = 0; var minute = 0; var allocated = GC.GetAllocatedBytesForCurrentThread();
            var lines = new List<string> { "minute    sim  ticks  model ms  observer ms  scheduler ms  worst ms  kB/tick  history rows" };
            for (var tick = 0; tick / fps < 1272; tick++) {
                var wall = 0.05 + tick / fps;
                snapshot = day.Snapshot(wall);
                clock.Restart(); var frame = model.Update(day.Feed, new ClockReading((6 + wall / 60) * 3600, ClockMode.Live, ""), DirectorFixtureDay.At(wall)); modelMs += clock.Elapsed.TotalMilliseconds;
                clock.Restart(); host.Tick(new DirectorSample("run", 6 + wall / 60, wall), day.Inputs(wall, frame)); var ms = clock.Elapsed.TotalMilliseconds; hostMs += ms; worst = Math.Max(worst, ms); ticks++;
                if ((int)((wall + 1 / fps) / 60) == minute && (tick + 1) / fps < 1272) continue;
                var now = GC.GetAllocatedBytesForCurrentThread();
                lines.Add(string.Format(CultureInfo.InvariantCulture, "{0,6}  {1,5:0.0}  {2,5}  {3,8:0.000}  {4,11:0.000}  {5,12:0.000}  {6,8:0.0}  {7,7:0}  {8,12}",
                    minute, 6 + wall / 60, ticks, modelMs / ticks, feed.Ms / ticks, (hostMs - feed.Ms) / ticks, worst, (now - allocated) / 1024.0 / ticks, day.ReadRows));
                minute++; modelMs = hostMs = worst = feed.Ms = 0; ticks = 0; allocated = now;
            }
            foreach (var line in lines) TestContext.Progress.WriteLine(line);
            Assert.That(observer.Problems, Is.Empty, string.Join("; ", observer.Problems));
        }
    }
}
