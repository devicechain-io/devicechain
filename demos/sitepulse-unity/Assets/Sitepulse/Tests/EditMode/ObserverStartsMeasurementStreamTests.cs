// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

using System.IO;
using System.Text.RegularExpressions;
using NUnit.Framework;
using UnityEngine;

namespace DeviceChain.Sitepulse.Tests
{
    /// <summary>
    /// PlatformObserver.Start builds the measurement stream runner and must also RUN it. A refactor of the alarm stream once
    /// dropped the one line that ran it: the runner was built, never started, and every machine sat at "0/19 observed" with the
    /// banner saying "measurements not started" while the devices published fine. Nothing else failed, because the alarm stream
    /// and the polls kept working. This pins that the runner a Start builds is a runner it starts.
    /// </summary>
    public sealed class ObserverStartsMeasurementStreamTests
    {
        static string ObserverSource()
        {
            var path = Path.GetFullPath(Path.Combine(Application.dataPath, "..", "..", "shared", "io.devicechain.demo-platform", "Runtime", "Platform", "PlatformObserver.cs"));
            Assert.IsTrue(File.Exists(path), "PlatformObserver.cs not found at " + path);
            return File.ReadAllText(path);
        }

        [Test]
        public void Start_runs_the_measurement_stream_it_builds()
        {
            var src = ObserverSource();
            Assert.IsTrue(Regex.IsMatch(src, @"new StreamRunner<JsonElement>\(\s*c => measurementSocket\.SubscribeAsync"), "the measurement runner is no longer built here: update this test with the move");
            Assert.IsTrue(Regex.IsMatch(src, @"Guard\(""measurement stream"",\s*\(\)\s*=>\s*measurements\.RunAsync\(ct\)"), "the measurement stream runner is built but never run: the cards would never be observed");
        }
    }
}
