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

    /// <summary>
    /// Where a card's value came from. A reading has exactly one provenance for its whole life, and
    /// every value written to it must declare the same one: an observed reading cannot be handed an
    /// illustrative number, and an illustrative one cannot be handed an observed number. This is how a
    /// Live card can never show a value the platform did not report.
    /// </summary>
    public enum Provenance { Illustrative, Observed, Replayed }

    /// <summary>When a value happened on the device (its own clock) and when this app saw it.</summary>
    public readonly struct Observation
    {
        public Observation(DateTimeOffset occurredAt, DateTimeOffset observedAt)
        {
            OccurredAt = occurredAt;
            ObservedAt = observedAt;
        }

        public DateTimeOffset OccurredAt { get; }
        public DateTimeOffset ObservedAt { get; }
    }

    /// <summary>
    /// The platform's nine command states, and <see cref="Unknown"/> for anything else (whose raw text
    /// is kept and printed). Showing a TIMEOUT or a CANCELLED as FAILED would say something the platform
    /// did not.
    /// </summary>
    public enum CommandState { Queued, Held, Sent, Parked, Successful, Failed, Timeout, Expired, Cancelled, Unknown }

    /// <summary>A command's state as the platform spelled it.</summary>
    public readonly struct CommandStatus
    {
        CommandStatus(CommandState state, string raw)
        {
            State = state;
            Raw = raw;
        }

        public CommandState State { get; }

        /// <summary>The text the platform sent; the only thing an Unknown state has to say.</summary>
        public string Raw { get; }

        public static CommandStatus Of(CommandState state) =>
            state == CommandState.Unknown ? throw new ArgumentException("an unknown state carries its raw text: use Parse", nameof(state)) : new CommandStatus(state, Name(state));

        /// <summary>Reads the platform's spelling (upper case). Anything else is Unknown, raw text kept.</summary>
        public static CommandStatus Parse(string raw)
        {
            switch (raw)
            {
                case "QUEUED": return Of(CommandState.Queued);
                case "HELD": return Of(CommandState.Held);
                case "SENT": return Of(CommandState.Sent);
                case "PARKED": return Of(CommandState.Parked);
                case "SUCCESSFUL": return Of(CommandState.Successful);
                case "FAILED": return Of(CommandState.Failed);
                case "TIMEOUT": return Of(CommandState.Timeout);
                case "EXPIRED": return Of(CommandState.Expired);
                case "CANCELLED": return Of(CommandState.Cancelled);
                default: return new CommandStatus(CommandState.Unknown, raw ?? "");
            }
        }

        /// <summary>The platform has finished with the command: it will not change state again.</summary>
        public bool IsTerminal => State == CommandState.Successful || State == CommandState.Failed
            || State == CommandState.Timeout || State == CommandState.Expired || State == CommandState.Cancelled;

        /// <summary>What a card prints: the platform's name for the state, or the raw text of an unknown one.</summary>
        public string Label => State == CommandState.Unknown ? (Raw.Length == 0 ? "UNKNOWN" : Raw) : Raw;

        static string Name(CommandState s) => s switch
        {
            CommandState.Queued => "QUEUED",
            CommandState.Held => "HELD",
            CommandState.Sent => "SENT",
            CommandState.Parked => "PARKED",
            CommandState.Successful => "SUCCESSFUL",
            CommandState.Failed => "FAILED",
            CommandState.Timeout => "TIMEOUT",
            CommandState.Expired => "EXPIRED",
            CommandState.Cancelled => "CANCELLED",
            _ => throw new ArgumentOutOfRangeException(nameof(s), s, null),
        };
    }

    /// <summary>An alarm on a card: which rule, how severe, and its state after the last transition.</summary>
    public readonly struct ReadingAlarm
    {
        public const string Active = "ACTIVE";

        public ReadingAlarm(string key, string severity, string state)
        {
            Key = key;
            Severity = severity;
            State = state;
        }

        public string Key { get; }

        /// <summary>The platform's severity, as it spelled it; null for an illustrative alarm, which has none.</summary>
        public string Severity { get; }

        public string State { get; }
        public bool IsActive => State == Active;
    }

    /// <summary>What a card shows for one device: its measurements by platform key, its speed (the
    /// device's last Location event's <c>speed</c>, converted to km/h, never derived from anything
    /// else), its alarms and the state of the last command sent to it. A device with no reading under
    /// a key shows nothing for it. Every value is written under a declared <see cref="Provenance"/>
    /// that must be the reading's own.</summary>
    public sealed class DeviceReading
    {
        public enum Profile { Equipment, Plant }

        readonly Dictionary<string, double> values = new Dictionary<string, double>();
        readonly Dictionary<string, bool> flags = new Dictionary<string, bool>();
        readonly Dictionary<string, Observation> stamps = new Dictionary<string, Observation>();
        readonly List<ReadingAlarm> alarms = new List<ReadingAlarm>();

        public DeviceReading(string deviceId, Profile profile, Provenance provenance = Provenance.Illustrative)
        {
            DeviceId = deviceId ?? throw new ArgumentNullException(nameof(deviceId));
            Kind = profile;
            Provenance = provenance;
        }

        public string DeviceId { get; }
        public Profile Kind { get; }
        public Provenance Provenance { get; }

        /// <summary>Speed over the ground in km/h, from the device's last Location event's speed (the
        /// platform stores m/s); equipment only. Not measured by anything else.</summary>
        public double? SpeedKmh { get; private set; }

        /// <summary>When the speed's Location event happened and was seen; null for an illustrative speed.</summary>
        public Observation? SpeedStamp { get; private set; }

        /// <summary>The last command sent to the device and where it is, if any.</summary>
        public string Command { get; private set; }
        public CommandStatus? CommandStatus { get; private set; }

        public IReadOnlyList<ReadingAlarm> Alarms => alarms;

        /// <summary>An alarm is active on the device.</summary>
        public bool HasAlarm
        {
            get
            {
                for (int i = 0; i < alarms.Count; i++)
                    if (alarms[i].IsActive) return true;
                return false;
            }
        }

        /// <summary>The first active alarm; only meaningful when <see cref="HasAlarm"/>.</summary>
        public ReadingAlarm FirstAlarm
        {
            get
            {
                for (int i = 0; i < alarms.Count; i++)
                    if (alarms[i].IsActive) return alarms[i];
                throw new InvalidOperationException("the reading has no active alarm");
            }
        }

        IReadOnlyList<string> Keys => Kind == Profile.Equipment ? MeasurementKeys.Equipment : MeasurementKeys.Plant;
        IReadOnlyList<string> FlagKeys => Kind == Profile.Equipment ? Array.Empty<string>() : MeasurementKeys.PlantFlags;
        IReadOnlyList<string> Commands => Kind == Profile.Equipment ? CommandKeys.Equipment : Array.Empty<string>();

        /// <summary>
        /// A value must be written under the reading's own provenance. An observed value carries when it
        /// happened and when it was seen; an illustrative one has neither (it did not happen).
        /// </summary>
        void Admit(Provenance by, Observation? stamp, string what)
        {
            if (by != Provenance)
                throw new InvalidOperationException($"{what}: a {by} value cannot be written to a {Provenance} reading of {DeviceId}");
            if (by == Provenance.Illustrative && stamp.HasValue)
                throw new ArgumentException($"{what}: an illustrative value has no observation time", nameof(stamp));
            if (by != Provenance.Illustrative && !stamp.HasValue)
                throw new ArgumentException($"{what}: a {by} value must say when it occurred and was seen", nameof(stamp));
        }

        /// <summary>Set a measurement. A key the device's profile does not define as a number is refused.</summary>
        public DeviceReading Set(string key, double value, Provenance by, Observation? stamp = null)
        {
            if (!Contains(Keys, key))
                throw new ArgumentException($"{key} is not a numeric measurement of the {Kind} profile", nameof(key));
            Admit(by, stamp, key);
            values[key] = value;
            Stamp(key, stamp);
            return this;
        }

        /// <summary>Set a BOOLEAN measurement. A key the device's profile does not define as one is refused.</summary>
        public DeviceReading Set(string key, bool value, Provenance by, Observation? stamp = null)
        {
            if (!Contains(FlagKeys, key))
                throw new ArgumentException($"{key} is not a boolean measurement of the {Kind} profile", nameof(key));
            Admit(by, stamp, key);
            flags[key] = value;
            Stamp(key, stamp);
            return this;
        }

        void Stamp(string key, Observation? stamp)
        {
            if (stamp.HasValue) stamps[key] = stamp.Value;
            else stamps.Remove(key);
        }

        /// <summary>Set the speed in km/h. Equipment only; the plant has no location.</summary>
        public DeviceReading SetSpeedKmh(double kmh, Provenance by, Observation? stamp = null)
        {
            if (Kind != Profile.Equipment) throw new ArgumentException($"the {Kind} profile has no speed");
            Admit(by, stamp, "speed");
            SpeedKmh = kmh;
            SpeedStamp = stamp;
            return this;
        }

        public bool TryGet(string key, out double value) => values.TryGetValue(key, out value);

        public bool TryGetFlag(string key, out bool value) => flags.TryGetValue(key, out value);

        /// <summary>When a measurement happened and was seen; false for an absent or illustrative one.</summary>
        public bool TryGetStamp(string key, out Observation stamp) => stamps.TryGetValue(key, out stamp);

        /// <summary>The newest occurrence time among the measurements; null when there are none stamped.</summary>
        public DateTimeOffset? NewestOccurredAt
        {
            get
            {
                DateTimeOffset? newest = null;
                foreach (var s in stamps.Values)
                    if (!newest.HasValue || s.OccurredAt > newest.Value) newest = s.OccurredAt;
                return newest;
            }
        }

        public void ClearAlarms() => alarms.Clear();

        /// <summary>Record an alarm by key. An alarm no Sitepulse rule raises is refused. Raising the same
        /// alarm again replaces its severity and state.</summary>
        public DeviceReading Raise(string alarmKey, Provenance by, string severity = null, string state = ReadingAlarm.Active, Observation? stamp = null)
        {
            if (Kind != Profile.Equipment || !Contains(AlarmKeys.All, alarmKey))
                throw new ArgumentException($"{alarmKey} is not an alarm of the {Kind} profile", nameof(alarmKey));
            Admit(by, stamp, alarmKey);
            var alarm = new ReadingAlarm(alarmKey, severity, state);
            for (int i = 0; i < alarms.Count; i++)
                if (alarms[i].Key == alarmKey)
                {
                    alarms[i] = alarm;
                    return this;
                }

            alarms.Add(alarm);
            return this;
        }

        /// <summary>Record the last command sent to the device. A command the device's profile does
        /// not define is refused, as the platform's enqueue gate would refuse it.</summary>
        public DeviceReading SetCommand(string command, CommandStatus status, Provenance by, Observation? stamp = null)
        {
            if (!Contains(Commands, command))
                throw new ArgumentException($"{command} is not a command of the {Kind} profile", nameof(command));
            Admit(by, stamp, command);
            Command = command;
            CommandStatus = status;
            return this;
        }

        public void ClearCommand()
        {
            Command = null;
            CommandStatus = null;
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

        static bool Contains(IReadOnlyList<string> list, string key)
        {
            for (int i = 0; i < list.Count; i++)
                if (list[i] == key) return true;
            return false;
        }
    }
}
