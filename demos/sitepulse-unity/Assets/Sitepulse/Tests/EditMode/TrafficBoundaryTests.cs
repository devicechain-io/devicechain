// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

using System.IO;
using System.Linq;
using NUnit.Framework;
using UnityEngine;

namespace DeviceChain.Sitepulse.Tests
{
    // The interlocking's core is kept free of the quarry so that a second simulation can take it: nothing under Scripts/Tasks/Traffic knows a quarry
    // type, and nothing reads the topology while the demo runs (S4 describes the site and checks it; the reader has no caller outside the tests).
    public sealed class TrafficBoundaryTests
    {
        static string Scripts => Path.Combine(Application.dataPath, "Sitepulse", "Scripts");

        [Test]
        public void TrafficFolderIsQuarryFree()
        {
            var folder = Path.Combine(Scripts, "Tasks", "Traffic");
            var files = Directory.GetFiles(folder, "*.cs", SearchOption.AllDirectories);
            Assert.GreaterOrEqual(files.Length, 4, "the folder's sources are found");
            foreach (var f in files)
            {
                var text = File.ReadAllText(f);
                StringAssert.DoesNotContain("using DeviceChain.Sitepulse", text, $"{Path.GetFileName(f)} uses a quarry namespace");
                StringAssert.DoesNotContain("DeviceChain.Sitepulse.", text.Replace("DeviceChain.Sitepulse.Tests", ""), $"{Path.GetFileName(f)} names a quarry type");
                StringAssert.DoesNotContain("using UnityEngine", text, $"{Path.GetFileName(f)} uses Unity");
            }

            var asmdef = File.ReadAllText(Path.Combine(folder, "DeviceChain.Sim.Traffic.asmdef"));
            StringAssert.Contains("\"references\": []", asmdef, "the assembly references nothing quarry-side");
        }

        [Test]
        public void NothingReadsTheTopologyWhileTheDemoRuns()
        {
            var callers = Directory.GetFiles(Scripts, "*.cs", SearchOption.AllDirectories)
                .Where(f => File.ReadAllText(f).Contains("SiteTopologyReader") && Path.GetFileName(f) != "SiteTopologyReader.cs").ToList();
            CollectionAssert.IsEmpty(callers, "the topology reader is called from: " + string.Join(", ", callers.Select(Path.GetFileName)));
        }
    }
}
