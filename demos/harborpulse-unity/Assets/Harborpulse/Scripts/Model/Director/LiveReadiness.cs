// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

using System;
using System.Collections.Generic;
using System.Linq;

namespace DeviceChain.Harborpulse.Model.Director
{
    public sealed class RecordingStatus
    {
        public string Problem { get; private set; }
        public T Establish<T>(Func<T> create) where T : class
        {
            try { var value = create(); if (value == null) throw new InvalidOperationException("no recording sink"); Problem = null; return value; }
            catch (Exception e) { Problem = "recording IO: " + e.Message; return null; }
        }
    }

    public static class PresentationMembership
    {
        public static bool TryRead(HarborLayout layout, DirectorReadSnapshot snapshot, Func<string, string> boundToken,
            DateTimeOffset now, out IReadOnlyDictionary<string, string> members, out string problem)
        {
            members = null; problem = null;
            var result = new Dictionary<string, string>(StringComparer.Ordinal);
            try {
                foreach (var quay in layout.Devices.Select(d => d.Quay).Distinct()) {
                    var read = snapshot?.Reads.FirstOrDefault(r => r.Name == "membership" && r.Scope == quay && r.Healthy(now));
                    if (read == null) { problem = "platform membership waiting/failed: " + quay; return false; }
                    foreach (var row in DirectorProbeEvaluation.Records("membership", read.Data.Value)) {
                        var token = row.GetProperty("token").GetString();
                        var device = layout.Devices.FirstOrDefault(d => boundToken(d.ExternalId) == token);
                        if (device == null) continue; // an area can also contain devices outside this scene
                        if (result.ContainsKey(device.Token)) throw new FormatException("multiple platform quays for " + device.Token);
                        result.Add(device.Token, quay);
                    }
                }
                members = result; return true;
            }
            catch (Exception e) when (e is FormatException || e is InvalidOperationException || e is KeyNotFoundException) {
                problem = "platform membership: " + e.Message; return false;
            }
        }
    }
}
