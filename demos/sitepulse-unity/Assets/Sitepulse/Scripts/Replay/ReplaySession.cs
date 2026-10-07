// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

using System;
using System.Collections.Generic;
using System.Text;
using DeviceChain.Sitepulse.Domain;
using DeviceChain.Sitepulse.Recording;
using DeviceChain.Sitepulse.Tasks;

namespace DeviceChain.Sitepulse.Replay
{
    /// <summary>
    /// A recording and a cursor into it. Time moves only by <see cref="Seek"/> and <see cref="Advance"/>; nothing here reads a clock,
    /// so the same steps give the same answers on any machine at any speed, which is what lets an offline render advance by exactly
    /// one frame's worth of time per frame. Observed state is rebuilt by applying the log up to the cursor (and from the start when
    /// the cursor goes back). Pure logic: no Unity, no network.
    /// </summary>
    public sealed class ReplaySession
    {
        readonly RecordingData data;
        readonly ReplayedState state = new ReplayedState();
        readonly Dictionary<string, List<(double t, TimelineRow row)>> timeline = new Dictionary<string, List<(double, TimelineRow)>>(StringComparer.Ordinal);
        readonly List<(double t, string machine)> received = new List<(double, string)>();
        readonly ProofLog proof = new ProofLog();
        readonly ZoneNameBook zones = new ZoneNameBook();
        readonly Dictionary<string, RoutePolyline> routes = new Dictionary<string, RoutePolyline>(StringComparer.Ordinal);
        readonly Func<string, string> idOfToken;
        int next, nextDevice;

        public ReplaySession(RecordingData data)
        {
            this.data = data ?? throw new ArgumentNullException(nameof(data));
            idOfToken = token => data.Header.IdOf(token);
            foreach (var d in data.Device)
            {
                if (d.K != DeviceKinds.Timeline) continue;
                if (!timeline.TryGetValue(d.Device, out var list)) timeline[d.Device] = list = new List<(double, TimelineRow)>();
                list.Add((d.T, new TimelineRow(d.Utc, d.RowKind, d.Text)));
                if (d.RowKind == TimelineKinds.Received) received.Add((d.T, d.Device));
            }
        }

        public RecordingData Data => data;
        public RunHeader Header => data.Header;
        public ReplayedState State => state;

        /// <summary>The proof drawer's rows as of the cursor: the same ones the live app held, rebuilt from the recording by the same code.</summary>
        public ProofLog Proof => proof;

        /// <summary>The zone names the recording holds as of the cursor (the platform's, read when the run started); empty when it holds none, and then no zone is labelled.</summary>
        public ZoneNameBook Zones => zones;

        /// <summary>The route the machine was driving at the cursor, drawn from the recording; null when it was driving none.</summary>
        public RoutePolyline RouteOf(string machine) => routes.TryGetValue(machine, out var r) ? r : null;
        public double Duration => data.Duration;

        /// <summary>Seconds into the recording.</summary>
        public double Time { get; private set; }

        public bool AtEnd => Time >= Duration;

        /// <summary>The wall-clock moment the viewer was living in at the cursor: what a card's age is judged against.</summary>
        public DateTimeOffset WallClock => data.Header.StartedAtUtc + TimeSpan.FromSeconds(Time);

        /// <summary>Moves to <paramref name="t"/> (clamped to the recording). Going back rebuilds the observed state from the start.</summary>
        public void Seek(double t)
        {
            if (double.IsNaN(t)) throw new ArgumentException("not a time", nameof(t));
            t = Math.Max(0.0, Math.Min(t, Duration));
            if (t < Time)
            {
                state.Reset();
                proof.Reset();
                zones.Clear();
                routes.Clear();
                next = 0;
                nextDevice = 0;
            }

            var log = data.Observed;
            while (next < log.Count && log[next].T <= t)
            {
                var line = log[next++];
                state.Apply(line);
                ProofFeed.Apply(proof, line, idOfToken);
            }

            var device = data.Device;
            while (nextDevice < device.Count && device[nextDevice].T <= t)
            {
                var d = device[nextDevice++];
                if (d.K == DeviceKinds.Timeline) proof.DeviceRow(d.Device, d.Utc, d.RowKind, d.Text);
                else if (d.K == DeviceKinds.Route) routes[d.Device] = RoutePolyline.FromFlat(d.Points);
                else if (d.K == DeviceKinds.Zones)
                    foreach (var kv in d.ZoneNames) zones.Set(kv.Key, kv.Value);
            }

            Time = t;
        }

        public void Advance(double dt) => Seek(Time + dt);

        /// <summary>The machine's pose and values at the cursor, interpolated between recorded frames; false for a machine the recording does not hold.</summary>
        public bool TrySample(string machineId, out MachineSample sample)
        {
            if (!data.Sim.TryIndexOf(machineId, out var i))
            {
                sample = default;
                return false;
            }

            sample = data.Sim.Sample(Time, i);
            return true;
        }

        /// <summary>The machine's timeline rows up to the cursor, oldest first, at most <paramref name="max"/>.</summary>
        public IReadOnlyList<TimelineRow> Rows(string machine, int max = Timeline.MaxRowsPerMachine)
        {
            var result = new List<TimelineRow>();
            if (!timeline.TryGetValue(machine, out var list)) return result;
            var end = CountUpTo(list, Time);
            for (var i = Math.Max(0, end - max); i < end; i++) result.Add(list[i].row);
            return result;
        }

        static int CountUpTo(List<(double t, TimelineRow row)> list, double t)
        {
            int lo = 0, hi = list.Count;
            while (lo < hi)
            {
                var mid = (lo + hi) >> 1;
                if (list[mid].t <= t) lo = mid + 1;
                else hi = mid;
            }

            return lo;
        }

        /// <summary>The machine that most recently received a command, as of the cursor; null before any.</summary>
        public string LastCommanded
        {
            get
            {
                string machine = null;
                foreach (var (t, m) in received)
                {
                    if (t > Time) break;
                    machine = m;
                }

                return machine;
            }
        }

        /// <summary>The timeline panel's text for a machine, in the form the live panel prints it (rows oldest first), or null with nothing to say.</summary>
        public string TimelineText(string machine, int rows = 10)
        {
            var list = Rows(machine, rows);
            if (list.Count == 0) return null;
            var sb = new StringBuilder();
            sb.Append("Timeline (the device's own account, recorded)\n").Append(machine).Append('\n');
            foreach (var r in list) sb.Append('\n').Append(r);
            return sb.ToString();
        }
    }
}
