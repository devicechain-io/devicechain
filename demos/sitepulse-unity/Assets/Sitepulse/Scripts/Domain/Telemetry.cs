// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

using System;
using System.Collections.Generic;
using System.Globalization;

namespace DeviceChain.Sitepulse.Domain
{
    /// <summary>
    /// The measurement keys the Sitepulse device profiles define, spelled as the platform spells
    /// them. A card shows these and nothing else: a value under any other key is refused. The key
    /// is the data model; what a person reads on a card is its <see cref="Label"/>.
    /// </summary>
    public static class MeasurementKeys
    {
        // the equipment profile (dozers, loaders, haul trucks)
        public const string FuelPct = "fuel_pct";
        public const string EngineTempC = "engine_temp_c";
        public const string EngineHours = "engine_hours";
        public const string PayloadT = "payload_t";
        public const string TyrePressureKpa = "tyre_pressure_kpa";

        // the plant profile (the primary crusher): a throughput, and whether it is running, which
        // the platform models as a BOOLEAN metric
        public const string ThroughputTph = "throughput_tph";
        public const string PlantRunning = "plant_running";

        public static readonly IReadOnlyList<string> Equipment = new[] { FuelPct, EngineTempC, EngineHours, PayloadT, TyrePressureKpa };
        public static readonly IReadOnlyList<string> Plant = new[] { ThroughputTph };
        public static readonly IReadOnlyList<string> PlantFlags = new[] { PlantRunning };

        /// <summary>The unit a key's value is shown in.</summary>
        public static string Unit(string key) => key switch
        {
            FuelPct => "%",
            EngineTempC => "°C",
            EngineHours => "h",
            PayloadT => "t",
            TyrePressureKpa => "kPa",
            ThroughputTph => "t/h",
            _ => throw new ArgumentException("not a numeric Sitepulse measurement key: " + key, nameof(key)),
        };

        /// <summary>What a card calls a key: a short name a person reads, not the key itself.</summary>
        public static string Label(string key) => key switch
        {
            FuelPct => "Fuel",
            EngineTempC => "Engine temp",
            EngineHours => "Engine hours",
            PayloadT => "Payload",
            TyrePressureKpa => "Tyre pressure",
            ThroughputTph => "Throughput",
            PlantRunning => "Running",
            _ => throw new ArgumentException("not a Sitepulse measurement key: " + key, nameof(key)),
        };
    }

    /// <summary>The alarms the Sitepulse rules raise, by alarm key.</summary>
    public static class AlarmKeys
    {
        public const string LowFuel = "low-fuel";
        public const string EngineOverheat = "engine-overheat";
        public const string TyrePressureLow = "tyre-pressure-low";

        public static readonly IReadOnlyList<string> All = new[] { LowFuel, EngineOverheat, TyrePressureLow };

        /// <summary>The measurement a rule watches to raise the alarm.</summary>
        public static string Metric(string alarmKey) => alarmKey switch
        {
            LowFuel => MeasurementKeys.FuelPct,
            EngineOverheat => MeasurementKeys.EngineTempC,
            TyrePressureLow => MeasurementKeys.TyrePressureKpa,
            _ => throw new ArgumentException("not a Sitepulse alarm: " + alarmKey, nameof(alarmKey)),
        };

        /// <summary>What a card calls an alarm.</summary>
        public static string Label(string alarmKey) => alarmKey switch
        {
            LowFuel => "Low fuel",
            EngineOverheat => "Engine overheat",
            TyrePressureLow => "Tyre pressure low",
            _ => throw new ArgumentException("not a Sitepulse alarm: " + alarmKey, nameof(alarmKey)),
        };
    }

    /// <summary>The commands the equipment profile defines, by command key (what a rule's
    /// sendCommand action and the console name), not by the command definition's token.</summary>
    public static class CommandKeys
    {
        public const string GotoArea = "goto-area";
        public const string GotoRefuel = "goto-refuel";

        public static readonly IReadOnlyList<string> Equipment = new[] { GotoArea, GotoRefuel };

        /// <summary>What a card calls a command.</summary>
        public static string Label(string commandKey) => commandKey switch
        {
            GotoArea => "Go to area",
            GotoRefuel => "Go refuel",
            _ => throw new ArgumentException("not a Sitepulse command: " + commandKey, nameof(commandKey)),
        };
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
        readonly Dictionary<string, bool> flags = new Dictionary<string, bool>();
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

        /// <summary>The last command sent to the device and where it is, if any.</summary>
        public string Command { get; private set; }
        public CommandState? CommandState { get; private set; }

        public IReadOnlyList<string> Alarms => alarms;
        public bool HasAlarm => alarms.Count > 0;

        IReadOnlyList<string> Keys => Kind == Profile.Equipment ? MeasurementKeys.Equipment : MeasurementKeys.Plant;
        IReadOnlyList<string> FlagKeys => Kind == Profile.Equipment ? Array.Empty<string>() : MeasurementKeys.PlantFlags;
        IReadOnlyList<string> Commands => Kind == Profile.Equipment ? CommandKeys.Equipment : Array.Empty<string>();

        /// <summary>Set a measurement. A key the device's profile does not define as a number is refused.</summary>
        public DeviceReading Set(string key, double value)
        {
            if (!Contains(Keys, key))
                throw new ArgumentException($"{key} is not a numeric measurement of the {Kind} profile", nameof(key));
            values[key] = value;
            return this;
        }

        /// <summary>Set a BOOLEAN measurement. A key the device's profile does not define as one is refused.</summary>
        public DeviceReading Set(string key, bool value)
        {
            if (!Contains(FlagKeys, key))
                throw new ArgumentException($"{key} is not a boolean measurement of the {Kind} profile", nameof(key));
            flags[key] = value;
            return this;
        }

        public bool TryGet(string key, out double value) => values.TryGetValue(key, out value);

        public bool TryGetFlag(string key, out bool value) => flags.TryGetValue(key, out value);

        public void ClearAlarms() => alarms.Clear();

        /// <summary>Raise an alarm by key. An alarm no Sitepulse rule raises is refused.</summary>
        public DeviceReading Raise(string alarmKey)
        {
            if (Kind != Profile.Equipment || !Contains(AlarmKeys.All, alarmKey))
                throw new ArgumentException($"{alarmKey} is not an alarm of the {Kind} profile", nameof(alarmKey));
            if (!alarms.Contains(alarmKey)) alarms.Add(alarmKey);
            return this;
        }

        /// <summary>Record the last command sent to the device. A command the device's profile does
        /// not define is refused, as the platform's enqueue gate would refuse it.</summary>
        public DeviceReading SetCommand(string command, CommandState state)
        {
            if (!Contains(Commands, command))
                throw new ArgumentException($"{command} is not a command of the {Kind} profile", nameof(command));
            Command = command;
            CommandState = state;
            return this;
        }

        public void ClearCommand()
        {
            Command = null;
            CommandState = null;
        }

        /// <summary>A key's value with its unit ("47 %", "720 t/h"), or null when there is none.
        /// A BOOLEAN key reads "Yes" or "No".</summary>
        public string Format(string key)
        {
            if (flags.TryGetValue(key, out var f)) return f ? "Yes" : "No";
            if (!values.TryGetValue(key, out var v)) return null;
            string unit = MeasurementKeys.Unit(key);
            string num = key == MeasurementKeys.PayloadT && v > 0.0 && v < 10.0
                ? v.ToString("0.0", CultureInfo.InvariantCulture)
                : Math.Round(v).ToString("0", CultureInfo.InvariantCulture);
            return num + " " + unit;
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
