// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

using System.Collections.Generic;
using DeviceChain.Demos.Platform;
using DeviceChain.Harborpulse.Model;

namespace DeviceChain.Harborpulse.Live
{
    /// <summary>
    /// What the scene needs of the platform, as the shared binder's data: each kind of device must resolve to its harborpulse device type, and
    /// the kinds the scene reads (the gauges and the lights) must have profiles that define the metrics it reads. The scene sends no command,
    /// so it requires none.
    /// </summary>
    public static class HarborScene
    {
        /// <summary>The name this demo registers with the shared platform package: its log prefix.</summary>
        public const string DemoName = "harborpulse";

        public static IReadOnlyList<SceneDevice> Devices(HarborLayout layout)
        {
            var list = new List<SceneDevice>(layout.Devices.Count);
            foreach (var d in layout.Devices) list.Add(new SceneDevice(d.ExternalId, d.Kind));
            return list;
        }

        /// <summary>The binder's contract, built from the layout so a device type renamed in the manifest is renamed here with it.</summary>
        public static SceneContract Contract(HarborLayout layout)
        {
            var kinds = new Dictionary<string, KindContract>();
            foreach (var d in layout.Devices)
            {
                if (kinds.ContainsKey(d.Kind)) continue;
                var k = new KindContract { TypeToken = d.DeviceType };
                if (DeviceKind.IsGauge(d.Kind)) k.NumericMetrics = new[] { Metrics.WaterLevel };
                if (d.Kind == DeviceKind.Light) k.NumericMetrics = new[] { Metrics.LampOn, Metrics.Dim, Metrics.LampFault, Metrics.LampPower };
                // the chargers that hold their own session and the pull lamps each resolve to their own type, whose profile defines
                // the one metric the scene reads of them
                if (d.Kind == DeviceKind.Pump || d.Kind == DeviceKind.Charger || d.Kind == DeviceKind.Meter || d.Kind == DeviceKind.Truck ||
                    DeviceKind.IsLinkedCharger(d.Kind) || d.Kind == DeviceKind.PullLight)
                    k.NumericMetrics = new List<string>(HarborObservedModel.MetricsFor(d.Kind)).ToArray();
                kinds[d.Kind] = k;
            }

            return new SceneContract { Kinds = kinds, Zones = new HashSet<string>() };
        }
    }
}
