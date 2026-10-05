// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

using System;
using System.Collections.Generic;
using System.Globalization;

namespace DeviceChain.Sitepulse.Domain
{
    /// <summary>
    /// The measurement keys the Sitepulse device profiles define, spelled as the platform spells
    /// them. A card shows these and nothing else: a value under any other key is refused.
    /// </summary>
    public static class MeasurementKeys
    {
        // the equipment profile (dozers, loaders, haul trucks)
        public const string FuelPct = "fuel_pct";
        public const string EngineTempC = "engine_temp_c";
        public const string EngineHours = "engine_hours";
        public const string PayloadT = "payload_t";
        public const string TyrePressureKpa = "tyre_pressure_kpa";

        // the plant device (the primary crusher)
        public const string ThroughputTph = "throughput_tph";

        public static readonly IReadOnlyList<string> Equipment = new[] { FuelPct, EngineTempC, EngineHours, PayloadT, TyrePressureKpa };
        public static readonly IReadOnlyList<string> Plant = new[] { ThroughputTph };

        /// <summary>The unit a key's value is shown in.</summary>
        public static string Unit(string key) => key switch
        {
            FuelPct => "%",
            EngineTempC => "°C",
            EngineHours => "h",
            PayloadT => "t",
            TyrePressureKpa => "kPa",
            ThroughputTph => "t/h",
            _ => throw new ArgumentException("not a Sitepulse measurement key: " + key, nameof(key)),
        };
    }

    /// <summary>The alarms the Sitepulse rules raise, by alarm key.</summary>
    public static class AlarmKeys
    {
        public const string LowFuel = "low-fuel";
        public const string Overheat = "overheat";
        public const string TyrePressureLow = "tyre-pressure-low";

        public static readonly IReadOnlyList<string> All = new[] { LowFuel, Overheat, TyrePressureLow };
    }

    /// <summary>Where a command sent to a machine is in its delivery.</summary>
    public enum CommandState { Queued, Sent, Successful, Failed }

    /// <summary>What a card shows for one device: its latest measurements by platform key, its
    /// speed (derived from its location, not measured), its active alarms and the state of the
    /// last command sent to it. A device with no reading under a key shows nothing for it.</summary>
    public sealed class DeviceReading
    {
        public enum Profile { Equipment, Plant }

        readonly Dictionary<string, double> values = new Dictionary<string, double>();
        readonly List<string> alarms = new List<string>();

        public DeviceReading(string deviceId, Profile profile)
        {
            DeviceId = deviceId ?? throw new ArgumentNullException(nameof(deviceId));
            Kind = profile;
        }

        public string DeviceId { get; }
        public Profile Kind { get; }

        /// <summary>Speed over the ground in km/h, from the device's location; equipment only.</summary>
        public double? SpeedKmh { get; set; }

        /// <summary>Whether the plant is running; plant only.</summary>
        public bool? Running { get; set; }

        /// <summary>The last command sent to the device and where it is, if any.</summary>
        public string Command { get; private set; }
        public CommandState? CommandState { get; private set; }

        public IReadOnlyList<string> Alarms => alarms;
        public bool HasAlarm => alarms.Count > 0;

        IReadOnlyList<string> Keys => Kind == Profile.Equipment ? MeasurementKeys.Equipment : MeasurementKeys.Plant;

        /// <summary>Set a measurement. A key the device's profile does not define is refused.</summary>
        public DeviceReading Set(string key, double value)
        {
            if (!Contains(Keys, key))
                throw new ArgumentException($"{key} is not a measurement of the {Kind} profile", nameof(key));
            values[key] = value;
            return this;
        }

        public bool TryGet(string key, out double value) => values.TryGetValue(key, out value);

        public void ClearAlarms() => alarms.Clear();

        /// <summary>Raise an alarm by key. An alarm no Sitepulse rule raises is refused.</summary>
        public DeviceReading Raise(string alarmKey)
        {
            if (Kind != Profile.Equipment || !Contains(AlarmKeys.All, alarmKey))
                throw new ArgumentException($"{alarmKey} is not an alarm of the {Kind} profile", nameof(alarmKey));
            if (!alarms.Contains(alarmKey)) alarms.Add(alarmKey);
            return this;
        }

        public DeviceReading SetCommand(string command, CommandState state)
        {
            Command = command;
            CommandState = state;
            return this;
        }

        public void ClearCommand()
        {
            Command = null;
            CommandState = null;
        }

        /// <summary>A key's value with its unit ("47 %", "720 t/h"), or null when there is none.</summary>
        public string Format(string key)
        {
            if (!values.TryGetValue(key, out var v)) return null;
            string unit = MeasurementKeys.Unit(key);
            string num = key == MeasurementKeys.PayloadT && v > 0.0 && v < 10.0
                ? v.ToString("0.0", CultureInfo.InvariantCulture)
                : Math.Round(v).ToString("0", CultureInfo.InvariantCulture);
            return unit == "%" ? num + " %" : num + " " + unit;
        }

        public static string Label(CommandState s) => s switch
        {
            Domain.CommandState.Queued => "QUEUED",
            Domain.CommandState.Sent => "SENT",
            Domain.CommandState.Successful => "SUCCESSFUL",
            _ => "FAILED",
        };

        static bool Contains(IReadOnlyList<string> list, string key)
        {
            for (int i = 0; i < list.Count; i++)
                if (list[i] == key) return true;
            return false;
        }
    }
}
