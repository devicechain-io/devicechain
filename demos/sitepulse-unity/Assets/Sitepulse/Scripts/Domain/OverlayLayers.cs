// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

using System;
using System.Collections.Generic;

namespace DeviceChain.Sitepulse.Domain
{
    public enum DrawerMode { Off, Side, Full }

    /// <summary>
    /// Which parts of the data layer are drawn: the cards (and the alarm ring), the proof drawer, the selected-machine panel, the route
    /// highlight and the zone labels. The geofence's outline and its name are drawn with the cards or the zone labels. This is everything a
    /// shot file may ask for: there is deliberately no member for the mode badge, the readiness panel, the key help or a replay tag (a render
    /// never carries them), so no shot can switch them on.
    /// </summary>
    public readonly struct OverlayLayers
    {
        public OverlayLayers(bool cards, DrawerMode drawer, bool panel, bool route, bool zoneLabels)
        {
            Cards = cards;
            Drawer = drawer;
            Panel = panel;
            Route = route;
            ZoneLabels = zoneLabels;
        }

        public bool Cards { get; }
        public DrawerMode Drawer { get; }
        public bool Panel { get; }
        public bool Route { get; }
        public bool ZoneLabels { get; }

        /// <summary>What a render shows when its shot says nothing: the cards, as before.</summary>
        public static OverlayLayers RenderDefault => new OverlayLayers(true, DrawerMode.Off, false, false, false);

        /// <summary>What a person at the keyboard starts with: the cards and the route highlight; the drawer, the panel and the zone labels are keys.</summary>
        public static OverlayLayers InteractiveDefault => new OverlayLayers(true, DrawerMode.Off, false, true, false);

        public bool FenceVisible => Cards || ZoneLabels;

        public OverlayLayers With(bool? cards = null, DrawerMode? drawer = null, bool? panel = null, bool? route = null, bool? zoneLabels = null) =>
            new OverlayLayers(cards ?? Cards, drawer ?? Drawer, panel ?? Panel, route ?? Route, zoneLabels ?? ZoneLabels);
    }

    /// <summary>A line of small print drawn into a render's frames while <c>From</c> to <c>Until</c> (seconds into the shot) holds. Render-only: nothing in Live or an interactive replay sets one.</summary>
    public readonly struct ChipView
    {
        public ChipView(string text, double from, double until)
        {
            Text = text;
            From = from;
            Until = until;
        }

        public string Text { get; }
        public double From { get; }
        public double Until { get; }
    }

    /// <summary>
    /// The names the site's zones carry on the platform (its areas, by token), as far as this run knows them. A zone the book has no name for
    /// has no label: nothing here guesses a name. Live fills it from the platform's own areas, a replay from what the run recorded, and
    /// Choreographed (offline, illustrative) from <see cref="ZoneNames.ManifestCopy"/>.
    /// </summary>
    public sealed class ZoneNameBook
    {
        readonly Dictionary<string, string> names = new Dictionary<string, string>(StringComparer.Ordinal);

        /// <summary>Moves whenever a name is set or the book is cleared.</summary>
        public int Version { get; private set; }

        public int Count => names.Count;

        public void Set(string token, string name)
        {
            if (string.IsNullOrWhiteSpace(token) || string.IsNullOrWhiteSpace(name)) return;
            if (names.TryGetValue(token, out var held) && held == name) return;
            names[token] = name;
            Version++;
        }

        public void Clear()
        {
            if (names.Count == 0) return;
            names.Clear();
            Version++;
        }

        /// <summary>The platform's name for a zone, or null when this run has none (the zone then has no label).</summary>
        public string Of(string token) => token != null && names.TryGetValue(token, out var n) ? n : null;
    }

    public static class ZoneNames
    {
        /// <summary>
        /// The names the Sitepulse manifest gives its three areas. It stands in ONLY for Choreographed mode, which has no platform to ask; a Live run asks
        /// the platform and a replay reads what the run recorded.
        /// </summary>
        public static ZoneNameBook ManifestCopy()
        {
            var book = new ZoneNameBook();
            book.Set("sp-zone-cut", "Excavation Face");
            book.Set("sp-zone-fill", "Fill Ground");
            book.Set("sp-zone-yard", "Equipment Yard");
            return book;
        }
    }

    /// <summary>The places inside a zone its name may ride, best first: the middle, then the middle of each half and each quarter.</summary>
    public static class ZoneLabelSpots
    {
        static readonly (double fx, double fz)[] Fractions =
        {
            (0.5, 0.5), (0.5, 0.25), (0.5, 0.75), (0.25, 0.5), (0.75, 0.5), (0.25, 0.25), (0.75, 0.25), (0.25, 0.75), (0.75, 0.75),
        };

        /// <summary>The spots of a zone <c>[x0, x1] x [z0, z1]</c>, in metres, in the order a label tries them.</summary>
        public static IReadOnlyList<(double X, double Z)> Of(double x0, double x1, double z0, double z1)
        {
            var spots = new List<(double, double)>(Fractions.Length);
            foreach (var (fx, fz) in Fractions) spots.Add((x0 + (x1 - x0) * fx, z0 + (z1 - z0) * fz));
            return spots;
        }
    }
}
