// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

using System;
using System.Collections.Generic;
using DeviceChain.Towerpulse.Plane;

namespace DeviceChain.Towerpulse.Story
{
    /// <summary>How fast a fan turns, from what its device reported. A rate for a value handed in; nothing is derived about the plant.</summary>
    public static class FanSpin
    {
        /// <summary>Degrees a second at 100 % (about one turn in 0.67 s: fast enough to read as a fan, slow enough not to alias at 60 frames).</summary>
        public const double MaxDegPerS = 540.0;
        public const double ChillerDegPerS = 480.0;

        public static double AhuDegPerS(double reportedFanPct)
        {
            if (double.IsNaN(reportedFanPct) || reportedFanPct <= 0) return 0;
            return (reportedFanPct >= 100 ? 100 : reportedFanPct) / 100.0 * MaxDegPerS;
        }

        /// <summary>The fans turn only while the chiller SAYS it is running; silence is not running (the fans ramp down through <see cref="Approach"/>).</summary>
        public static double ChillerFansDegPerS(bool running) { return running ? ChillerDegPerS : 0; }

        /// <summary>Moves a current rate toward a target by at most <paramref name="rampDegPerS2"/> a second, so a fan spins up and down instead of snapping.</summary>
        public static double Approach(double current, double target, double rampDegPerS2, double dt)
        {
            double step = rampDegPerS2 * dt;
            if (current < target) return Math.Min(target, current + step);
            return Math.Max(target, current - step);
        }
    }

    /// <summary>What a chiller's status lamp says. "Not reporting" is a different statement from "stopped": one is silence, the other is a word.</summary>
    public enum LampState { Running, Stopped, NotReporting }

    public static class LampLook
    {
        public static readonly Rgba RunningColour = new Rgba(0.20f, 0.92f, 0.34f, 1f);
        public static readonly Rgba StoppedColour = new Rgba(0.96f, 0.18f, 0.12f, 1f);
        public static readonly Rgba SilentColour = new Rgba(1.00f, 0.72f, 0.12f, 1f);
        public const double SilentBlinkPeriodS = 1.6;

        /// <summary>From the reading the chiller last sent: none at all is "not reporting", otherwise its own <c>running</c> flag decides.</summary>
        public static LampState Of(IReadOnlyDictionary<string, double> reading)
        {
            if (reading == null) return LampState.NotReporting;
            if (reading.TryGetValue(Wire.Running, out var r)) return r >= 0.5 ? LampState.Running : LampState.Stopped;
            return LampState.NotReporting; // a report with no running flag does not say the chiller is running
        }

        public static Rgba Colour(LampState s)
        {
            switch (s)
            {
                case LampState.Running: return RunningColour;
                case LampState.Stopped: return StoppedColour;
                default: return SilentColour;
            }
        }

        /// <summary>0..1 brightness at a time: steady for a word, a slow blink for silence.</summary>
        public static double Brightness(LampState s, double timeS)
        {
            if (s != LampState.NotReporting) return 1.0;
            double phase = (timeS / SilentBlinkPeriodS) - Math.Floor(timeS / SilentBlinkPeriodS);
            return phase < 0.5 ? 1.0 : 0.12;
        }

        public static string Label(LampState s)
        {
            switch (s)
            {
                case LampState.Running: return "RUNNING";
                case LampState.Stopped: return "STOPPED";
                default: return "NOT REPORTING";
            }
        }
    }

    public static class StoryText
    {
        /// <summary>The label shown whenever the model runs faster than the wall clock; null at real time.</summary>
        public static string Accelerated(double modelSpeed)
        {
            if (double.IsNaN(modelSpeed) || modelSpeed <= 1.0000001) return null;
            return "ACCELERATED x" + modelSpeed.ToString("0.#", System.Globalization.CultureInfo.InvariantCulture);
        }
        /// <summary>
        /// The label a shot carries while its clock runs faster than the wall: "TIME JUMP x120", the speed rounded so a reader can tell a skip from real time.
        /// Null when the shot is at real time (or has no clock of its own), because real time needs no label.
        /// </summary>
        public static string Jump(double modelSecondsPerShowSecond)
        {
            if (double.IsNaN(modelSecondsPerShowSecond) || modelSecondsPerShowSecond <= 1.05) return null;
            return "TIME JUMP x" + Math.Round(modelSecondsPerShowSecond).ToString("0", System.Globalization.CultureInfo.InvariantCulture);
        }

        /// <summary>The part of the day a model hour falls in, by the word a caption would use for it.</summary>
        public static string DayPart(double hourOfDay)
        {
            double h = ((hourOfDay % 24) + 24) % 24;
            if (h < 5) return "night";
            if (h < 8) return "dawn";
            if (h < 12) return "morning";
            if (h < 17.5) return "afternoon";
            if (h < 20) return "dusk";
            if (h < 22) return "evening";
            return "night";
        }

        static readonly string[][] PartWords =
        {
            new[] { "dawn", "sunrise" }, new[] { "morning" }, new[] { "afternoon" }, new[] { "dusk", "sunset", "twilight" }, new[] { "evening" }, new[] { "night" },
        };
        static readonly string[] PartNames = { "dawn", "morning", "afternoon", "dusk", "evening", "night" };

        /// <summary>
        /// The part of the day a caption claims ("dusk", "afternoon", ...), or null when it makes no claim. A caption that names a time of day must agree with
        /// the model clock the shot shows beside it: <see cref="CaptionAgrees"/>.
        /// </summary>
        public static string ClaimedDayPart(string caption)
        {
            if (string.IsNullOrEmpty(caption)) return null;
            var lower = caption.ToLowerInvariant();
            for (int i = 0; i < PartWords.Length; i++)
                foreach (var w in PartWords[i])
                    if (System.Text.RegularExpressions.Regex.IsMatch(lower, "\b" + w + "\b")) return PartNames[i];
            return null;
        }

        /// <summary>True when the caption makes no claim about the time of day, or the claim is true at that model hour.</summary>
        public static bool CaptionAgrees(string caption, double hourOfDay)
        {
            var claim = ClaimedDayPart(caption);
            return claim == null || claim == DayPart(hourOfDay);
        }

    }
}
