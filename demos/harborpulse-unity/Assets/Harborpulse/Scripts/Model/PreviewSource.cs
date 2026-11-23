// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

using System;
using DeviceChain.Demos.Platform;

namespace DeviceChain.Harborpulse.Model
{
    /// <summary>
    /// The OFFLINE PREVIEW's stand-in for the platform: plausible gauge levels and lamp states for a rehearsal or a look-check still, with no
    /// instance. Its curves follow the simulator's story (a 12-hour tide with high water at 07:00 and 19:00, the storm surge from 17:00, East
    /// Quay sitting 0.3 m higher, the lamps on from 18:00 to 06:30, East Quay's feeder out from 19:45 to 21:15) so a preview looks like the
    /// day, but it is NOT platform data and the scene never shows it unlabelled: every frame of a preview says PREVIEW.
    /// </summary>
    public sealed partial class PreviewSource : IHarborSource
    {
        readonly HarborLayout layout;
        double simSeconds;
        DateTimeOffset now;

        public PreviewSource(HarborLayout layout)
        {
            this.layout = layout ?? throw new ArgumentNullException(nameof(layout));
        }

        /// <summary>Moves the preview to a sim time; readings are stamped <paramref name="wallNow"/>, so they are always fresh.</summary>
        public void SetTime(double simS, DateTimeOffset wallNow)
        {
            if (simS < simSeconds) commandRecords.Clear();
            simSeconds = simS;
            now = wallNow;
            BuildPreviewEvidence();
        }

        public bool TryGet(string externalId, string metric, out Reading reading)
        {
            reading = default;
            var d = layout.Find(externalId);
            if (d == null) return false;
            var h = DayClock.Hours(simSeconds);
            var outage = d.Quay == "hp-east-quay" && h >= 19.75 && h < 21.25;
            switch (metric)
            {
                case Metrics.WaterLevel:
                    if (!DeviceKind.IsGauge(d.Kind)) return false;
                    // the legacy gauge's decoder is the wrong one: it never yields a level
                    if (d.Kind == DeviceKind.LegacyGauge) return false;
                    var level = Tide(h) + Surge(h) + (d.Quay == "hp-east-quay" ? 0.3 : 0) + 0.02 * Math.Sin(d.Index * 2.1);
                    reading = new Reading(level, now);
                    return true;
                case Metrics.LampOn:
                    if (d.Kind != DeviceKind.Light) return false;
                    // through the outage the lamp is silent: its last report is from when the feeder failed
                    var at = outage ? now - TimeSpan.FromSeconds((h - 19.75) / 24 * layout.DayLengthSeconds) : now;
                    reading = new Reading(LampsOn(h) || outage ? 1 : 0, at);
                    return true;
                case Metrics.Dim:
                    // a pull lamp holds full level until it fetches the day's set-dim command
                    if (d.Kind == DeviceKind.PullLight) { reading = new Reading(h >= PullDimHour ? PullDimPct : 100, now); return true; }
                    if (d.Kind != DeviceKind.Light) return false;
                    reading = new Reading(100, now);
                    return true;
                default:
                    return TryPreviewMetric(d, metric, h, outage, out reading);
            }
        }

        /// <summary>
        /// The stand-in day of the devices that hold their own session, after the simulator's: the lamps fetch a set-dim to 35 % at 10:00, the
        /// first charger on a token drops its session from 12:00 to 13:00, and the second charger on a certificate is refused from 14:00.
        /// </summary>
        public const double PullDimHour = 10, PullDimPct = 35, MqttDropFrom = 12, MqttDropTo = 13, CertRefusedFrom = 14, LinkedChargerKw = 7.2;

        /// <summary>The hour a linked charger's stand-in fell silent, when it is silent at <paramref name="h"/>.</summary>
        public static double? LinkedSilentSince(LayoutDevice d, double h) =>
            d.Kind == DeviceKind.MqttCharger && d.Index == 1 && h >= MqttDropFrom && h < MqttDropTo ? MqttDropFrom :
            d.Kind == DeviceKind.CertCharger && d.Index == 2 && h >= CertRefusedFrom ? CertRefusedFrom : (double?)null;

        static double Tide(double h) => 0.5 + 0.5 * Math.Cos(2 * Math.PI * (h - 19) / 12);

        static double Surge(double h) => Smooth(17, 18.5, h) * (1 - Smooth(20.5, 22.5, h));

        static bool LampsOn(double h) => h < 6.5 || h >= 18;

        static double Smooth(double a, double b, double x)
        {
            if (x <= a) return 0;
            if (x >= b) return 1;
            var t = (x - a) / (b - a);
            return t * t * (3 - 2 * t);
        }
    }
}
