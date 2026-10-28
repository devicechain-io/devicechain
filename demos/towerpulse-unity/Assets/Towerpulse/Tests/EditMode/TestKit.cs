// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

using System;
using System.Collections.Generic;
using DeviceChain.Towerpulse.Model;

namespace DeviceChain.Towerpulse.Tests
{
    /// <summary>
    /// A stand-in for the platform's rules, just enough to close the loop in a test: R1 (5 minute average above 26 C while the room is occupied raises an
    /// alarm and sends boost-cooling) and R4 (a chiller silent for 2 minutes raises an alarm). It samples every 30 s, as a device would report. It lives in
    /// the tests, not the model: the model must stay unable to decide anything.
    /// </summary>
    public sealed class RuleHarness
    {
        public const double SampleS = 30.0;
        public const double HotC = 26.0;
        public const double AbsenceS = 120.0;

        public readonly TowerDay Day;
        public bool R1Enabled = true;
        public bool R4Enabled = true;
        public double BoostMinutes = 30;

        public readonly Dictionary<string, double> HotAlarmAt = new Dictionary<string, double>();
        public readonly Dictionary<string, CommandTicket> BoostSent = new Dictionary<string, CommandTicket>();
        public double ChillerAlarmAt = double.NaN;
        public double MaxF4EastTempC = double.MinValue;

        readonly Dictionary<string, Queue<double>> _window = new Dictionary<string, Queue<double>>();
        double _silentSince = double.NaN;

        public RuleHarness(TowerDay day) { Day = day; }

        public BuildingModel Model { get { return Day.Model; } }

        /// <summary>Advance in 30 s report intervals to a sim time, evaluating the rules on each.</summary>
        public void RunTo(double simS, Action<double> onSample = null)
        {
            while (Day.TimeS + SampleS <= simS + 1e-9)
            {
                double t = Day.TimeS + SampleS;
                Day.AdvanceTo(t);
                Evaluate(t);
                if (onSample != null) onSample(t);
            }
        }

        void Evaluate(double t)
        {
            foreach (var z in Model.Zones)
            {
                string id = z.Spec.Id;
                Queue<double> w;
                if (!_window.TryGetValue(id, out w)) { w = new Queue<double>(); _window[id] = w; }
                w.Enqueue(z.TempC);
                while (w.Count > 10) w.Dequeue();
                if (id == BuildingLayout.HotZone && z.TempC > MaxF4EastTempC) MaxF4EastTempC = z.TempC;
                if (!R1Enabled || w.Count < 10 || z.Occupancy == 0 || HotAlarmAt.ContainsKey(id)) continue;
                double sum = 0;
                foreach (double v in w) sum += v;
                if (sum / w.Count > HotC)
                {
                    HotAlarmAt[id] = t;
                    BoostSent[id] = Model.BoostCooling(id, BoostMinutes);
                }
            }
            // R1 re-arms once the room has been under the threshold again.
            var rearm = new List<string>();
            foreach (var kv in HotAlarmAt)
            {
                int zi = Model.Layout.ZoneIndex(kv.Key);
                if (Model.Zones[zi].TempC < HotC - 1.0 && Model.Zones[zi].Boost == null) rearm.Add(kv.Key);
            }
            foreach (var id in rearm) HotAlarmAt.Remove(id);

            var ch = Model.Chillers[Model.Layout.ChillerIndex(BuildingLayout.AnnexChiller)];
            if (ch.Online) _silentSince = double.NaN;
            else
            {
                if (double.IsNaN(_silentSince)) _silentSince = t;
                if (R4Enabled && double.IsNaN(ChillerAlarmAt) && t - _silentSince >= AbsenceS) ChillerAlarmAt = t;
            }
        }

        public ZoneState Zone(string id) { return Model.Zones[Model.Layout.ZoneIndex(id)]; }
    }
}
