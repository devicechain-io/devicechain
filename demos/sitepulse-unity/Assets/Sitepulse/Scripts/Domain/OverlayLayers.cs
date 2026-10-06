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

    /// <summary>The names the three zones carry on the platform (the areas the Sitepulse manifest provisions), by area token.</summary>
    public static class ZoneNames
    {
        /// <summary>The platform's area name for a zone token, or <paramref name="fallback"/> (the feature file's own label) for a zone it does not know.</summary>
        public static string Of(string token, string fallback)
        {
            switch (token)
            {
                case "sp-zone-cut": return "Excavation Face";
                case "sp-zone-fill": return "Fill Ground";
                case "sp-zone-yard": return "Equipment Yard";
                default: return fallback;
            }
        }
    }
}
