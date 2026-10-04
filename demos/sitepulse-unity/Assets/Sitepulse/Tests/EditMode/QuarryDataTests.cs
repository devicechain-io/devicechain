// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

using System.IO;
using System.IO.Compression;
using DeviceChain.Sitepulse.Visuals;
using NUnit.Framework;
using UnityEditor;
using UnityEngine;

namespace DeviceChain.Sitepulse.Tests
{
    public sealed class QuarryDataTests
    {
        // Pack heights the way quarry_heightmap.py does: plane-predicted, mod 65536, gzip.
        static byte[] Pack(ushort[,] s)
        {
            int n = s.GetLength(0);
            var raw = new byte[n * n * 2];
            for (int j = 0; j < n; j++)
            {
                for (int i = 0; i < n; i++)
                {
                    int pred = (i > 0 ? s[j, i - 1] : 0) + (j > 0 ? s[j - 1, i] : 0) - (i > 0 && j > 0 ? s[j - 1, i - 1] : 0);
                    int r = (s[j, i] - pred) & 0xFFFF;
                    raw[2 * (j * n + i)] = (byte)r;
                    raw[2 * (j * n + i) + 1] = (byte)(r >> 8);
                }
            }
            using var ms = new MemoryStream();
            using (var gz = new GZipStream(ms, CompressionMode.Compress)) gz.Write(raw, 0, raw.Length);
            return ms.ToArray();
        }

        [Test]
        public void HeightmapDecodeRoundTrips()
        {
            const int n = 33;
            var s = new ushort[n, n];
            var rng = new System.Random(7);
            for (int j = 0; j < n; j++)
                for (int i = 0; i < n; i++)
                    s[j, i] = (ushort)rng.Next(0, 65536);       // worst case: no smoothness at all
            s[0, 0] = 65535;
            s[n - 1, n - 1] = 0;
            var h = QuarryTerrain.DecodeHeights(Pack(s), n);
            for (int j = 0; j < n; j++)
                for (int i = 0; i < n; i++)
                    Assert.AreEqual(s[j, i] / 65535f, h[j, i], 1e-7f, $"sample row {j} col {i}");
        }

        [Test]
        public void HeightmapDecodeRejectsATruncatedFile()
        {
            var packed = Pack(new ushort[9, 9]);
            Assert.Throws<InvalidDataException>(() => QuarryTerrain.DecodeHeights(packed, 17));
        }

        [System.Serializable]
        sealed class Choreo
        {
            public float dt;
            public string[] channels;
            public Track[] tracks;
            public Machine[] machines;
        }

        [System.Serializable]
        sealed class Track
        {
            public string kind;
            public float period;
            public float[] data;
        }

        [System.Serializable]
        sealed class Machine
        {
            public string id, kind;
            public int track;
        }

        [Test]
        public void FleetChoreographyHasTheWholeFleet()
        {
            var json = AssetDatabase.LoadAssetAtPath<TextAsset>("Assets/Sitepulse/Data/quarry_fleet.json");
            Assert.IsNotNull(json);
            var c = JsonUtility.FromJson<Choreo>(json.text);
            Assert.AreEqual(8, c.channels.Length);
            Assert.AreEqual(18, c.machines.Length);
            foreach (var kind in new[] { "Dozer", "Loader", "Hauler" })
                Assert.AreEqual(6, System.Array.FindAll(c.machines, m => m.kind == kind).Length, kind);
            foreach (var m in c.machines)
            {
                var t = c.tracks[m.track];
                Assert.AreEqual(m.kind, t.kind, m.id);
                Assert.AreEqual(0, t.data.Length % c.channels.Length, m.id);
                Assert.AreEqual(Mathf.Round(t.period / c.dt), t.data.Length / c.channels.Length, 1f, m.id);
            }
        }
    }
}
