// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

using System.Collections.Generic;
using System.Globalization;

namespace DeviceChain.Harborpulse.Model
{
    /// <summary>
    /// The words written in the world itself (on a well's level rings, on a cabinet), in two classes. A viewer's tag is written for
    /// someone who has never seen a rule key and is drawn in every picture. An engineer's tag names the source of what it marks (a
    /// rule key, WORLD, the runner) and is drawn only with the engineer overlay: it never appears in a rendered cut, and the render's
    /// text audit refuses one that does.
    /// </summary>
    public static class WorldText
    {
        static string M(double level) => level.ToString("0.0", CultureInfo.InvariantCulture) + " m";

        /// <summary>The three fixed levels of a wet well, for a viewer.</summary>
        public static string AlarmLevel => "Alarm level " + M(PumpPresentation.HighMarkerM);
        public static string FloatSwitch => "Float switch " + M(PumpPresentation.FloatMarkerM);
        public static string PumpStops => "Pump stops " + M(PumpPresentation.LowCutoffM);

        /// <summary>Engineer-only: where a marker's value comes from.</summary>
        public const string AttributeRule = "well_alarm_m (platform attribute, MINOR rule)";
        public const string FeederDown = "feeder down (WORLD)";
        public const string Replacement = "replacement (runner)";

        public static IEnumerable<string> ViewerTags => new[] { AlarmLevel, FloatSwitch, PumpStops };
        public static IEnumerable<string> EngineerTags => new[] { AttributeRule, FeederDown, Replacement };

        /// <summary>
        /// Whether a string reads as an engineer's label: a source in brackets, an identifier with an underscore, or a rule key
        /// (lower-case words joined by hyphens). A viewer's tag has none of these.
        /// </summary>
        public static bool LooksInternal(string text)
        {
            if (string.IsNullOrEmpty(text)) return false;
            if (text.Contains("(") || text.Contains("_") || text.Contains("WORLD")) return true;
            return System.Text.RegularExpressions.Regex.IsMatch(text, @"\b[a-z]+(-[a-z]+)+\b");
        }
    }
}
