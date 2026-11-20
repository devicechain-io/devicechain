// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

using System.Collections.Generic;
using System.Globalization;
using System.IO;
using System.Text.Json;
using System.Linq;

namespace DeviceChain.Harborpulse.Model.Director
{
    /// <summary>The operator checker uses the same page-local boundaries in check_director_log.py.</summary>
    public static class DirectorLogCheck
    {
        public static IReadOnlyList<string> Check(TextReader input)
        {
            var failures = new List<string>(); var events = new List<DirectorEvent>(); double eof = 0;
            string line;
            while ((line = input.ReadLine()) != null)
            {
                using (var doc = JsonDocument.Parse(line))
                {
                    var row = doc.RootElement;
                    if (row.TryGetProperty("wallS", out var wall)) eof = System.Math.Max(eof, wall.GetDouble());
                    if (row.GetProperty("kind").GetString() != "director") continue;
                    var e = DirectorWire.ReadEvent(row.GetProperty("data"));
                    events.Add(e); eof = System.Math.Max(eof, e.ShowS);
                }
            }
            var rendered = events.Any(e => e.Kind == "visible-caption");
            DirectorEvent active = null; int captions = 0;
            void Close(double at) {
                if (active == null) return;
                var duration = at - active.ShowS;
                if (duration < 3 - 1e-9) failures.Add("page " + active.Page + " caption at " + active.ShowS.ToString("R", CultureInfo.InvariantCulture) + " visible " + duration.ToString("R", CultureInfo.InvariantCulture) + " s: " + active.Value);
                active = null;
            }
            foreach (var e in events) {
                var caption = rendered ? e.Kind == "visible-caption" : e.Kind == "caption" || e.Kind == "title";
                var end = rendered ? e.Kind == "visible-caption-end" : e.Kind == "caption-end" || e.Kind == "page-end" || e.Kind == "state" && e.Value == "Presented";
                if (caption) { Close(e.ShowS); active = e; captions++; }
                else if (e.Kind == "recording-end" || end && active?.Page == e.Page) Close(e.ShowS);
            }
            Close(eof); // EOF is a boundary too, even when the writer did not close cleanly.
            if (captions == 0) failures.Add("empty acceptance log: no captions");
            return failures;
        }
    }
}
