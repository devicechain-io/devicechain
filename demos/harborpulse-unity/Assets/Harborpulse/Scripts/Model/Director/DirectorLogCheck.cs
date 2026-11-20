// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

using System.Collections.Generic;
using System.Globalization;
using System.IO;
using System.Text.Json;

namespace DeviceChain.Harborpulse.Model.Director
{
    /// <summary>The operator checker uses the same page-local boundaries in check_director_log.py.</summary>
    public static class DirectorLogCheck
    {
        public static IReadOnlyList<string> Check(TextReader input)
        {
            var active = new Dictionary<string, DirectorEvent>(); var failures = new List<string>();
            string line;
            while ((line = input.ReadLine()) != null)
            {
                using (var doc = JsonDocument.Parse(line))
                {
                    var row = doc.RootElement;
                    if (row.GetProperty("kind").GetString() != "director") continue;
                    var e = DirectorWire.ReadEvent(row.GetProperty("data"));
                    var end = e.Kind == "caption" || e.Kind == "caption-end" || e.Kind == "page-end" || e.Kind == "state" && e.Value == "Presented";
                    if (end && active.TryGetValue(e.Page, out var previous))
                    {
                        var duration = e.ShowS - previous.ShowS;
                        if (duration < 3 - 1e-9) failures.Add("page " + e.Page + " caption at " + previous.ShowS.ToString("R", CultureInfo.InvariantCulture) + " visible " + duration.ToString("R", CultureInfo.InvariantCulture) + " s: " + previous.Value);
                        active.Remove(e.Page);
                    }
                    if (e.Kind == "caption") active[e.Page] = e;
                }
            }
            return failures;
        }
    }
}
