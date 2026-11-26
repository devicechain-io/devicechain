// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

using System;
using System.Collections.Generic;

namespace DeviceChain.Harborpulse.Model.Director
{
    /// <summary>The names of every authored camera pose, so builders and tests can walk them without reaching into the map.</summary>
    public static class StoryPoses
    {
        /// <summary>(pose family, device token): each family has the eight suffixes below.</summary>
        public static readonly (string Family, string Token)[] Families =
        {
            ("pump-north", "hp-pump-north"), ("pump-east", "hp-pump-east"), ("pump-basin", "hp-pump-basin"),
            ("chargers-east", "hp-charger-east-1"), ("chargers-basin", "hp-charger-basin-2"), ("lamp-east", "hp-light-east-3"),
            ("gauge-north", "hp-gauge-north-3"), ("gauge-east", "hp-gauge-east-1"), ("terminal", "hp-meter-ferry-terminal"),
            ("north-quay", "hp-light-north-1"), ("charger-north", "hp-charger-north-1"),
        };

        /// <summary>Stand-off views from the water side at ground level: these show the horizon.</summary>
        public static readonly string[] GroundSuffixes = { "kiosk-ext", "pair", "close", "wide", "promenade", "insert" };

        /// <summary>The cut-away views into the wet well, which keep their steep downward look.</summary>
        public static readonly string[] WellSuffixes = { "well", "well-locked" };

        /// <summary>Added to a pose's name: its 9:16 version (<see cref="PresentationMap.Tall"/>). Only the wells' views have one.</summary>
        public const string TallSuffix = ":tall";

        /// <summary>The ground-level poses that are not a family of device views.</summary>
        public static readonly string[] SpecialGround = { "pose:east-quay:mouth-wide", "pose:plain:wide", "pose:truck:chase", "pose:depot:wide" };

        /// <summary>The rig's night-sky shot (not a story pose) and the scenario hour at which the moon stands in the middle of it.</summary>
        public const string NightSky = "pose:basin:night-sky";
        public const double NightSkyHour = 4;

        /// <summary>The start of a cut's crane down to HOME (not a story pose of its own: no shot holds on it).</summary>
        public const string HomeHigh = "pose:home-high";

        /// <summary>A cut's own view: the end of the rise over the dark East Quay.</summary>
        public const string EastRise = "pose:east-quay:rise";

        public static readonly string[] Home = { "pose:home", "pose:home:portrait", "pose:night-home", "pose:storm-home" };

        public static IEnumerable<string> GroundLevel()
        {
            foreach (var f in Families) foreach (var s in GroundSuffixes) yield return "pose:" + f.Family + ":" + s;
            foreach (var s in SpecialGround) yield return s;
        }

        public static IEnumerable<string> All()
        {
            foreach (var h in Home) yield return h;
            foreach (var g in GroundLevel()) yield return g;
            foreach (var f in Families) foreach (var s in WellSuffixes) yield return "pose:" + f.Family + ":" + s;
        }

        /// <summary>
        /// True when an upright obstacle (a crown or a post of <paramref name="radius"/> metres standing from <paramref name="baseY"/> to
        /// <paramref name="topY"/>) stands in the line of sight from a pose's camera to its look target: the segment passes within
        /// <paramref name="radius"/> + <paramref name="margin"/> of it, horizontally, below its top. The first and last metre are ignored.
        /// </summary>
        public static bool Blocks(CameraPose pose, double x, double z, double baseY, double topY, double radius, double margin = 1.5)
        {
            double ax = pose.Camera.X, az = pose.Camera.Z, bx = pose.Look.X, bz = pose.Look.Z;
            var dx = bx - ax; var dz = bz - az;
            var len2 = dx * dx + dz * dz;
            if (len2 < 1e-6) return false;
            var u = Math.Max(0, Math.Min(1, ((x - ax) * dx + (z - az) * dz) / len2));
            var len = Math.Sqrt(len2);
            if (u * len < 1 || (1 - u) * len < 1) return false;
            var px = ax + u * dx; var pz = az + u * dz;
            if ((x - px) * (x - px) + (z - pz) * (z - pz) > (radius + margin) * (radius + margin)) return false;
            var sight = pose.Camera.Y + u * (pose.Look.Y - pose.Camera.Y);
            return sight < topY + .5 && sight > baseY - 1;
        }
    }
}
