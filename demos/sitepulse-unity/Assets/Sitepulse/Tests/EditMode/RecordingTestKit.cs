// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

using System;
using System.Collections.Generic;
using System.IO;
using DeviceChain.Sitepulse.Recording;
using NUnit.Framework;

namespace DeviceChain.Sitepulse.Tests
{
    /// <summary>A short synthetic run on disk, written through the real recorder with a clock the test moves.</summary>
    sealed class SyntheticRun : IDisposable
    {
        public static readonly DateTimeOffset Start = new DateTimeOffset(2026, 10, 6, 14, 0, 0, TimeSpan.Zero);
        public const string Truck = "SP-HL-0006", Loader = "SP-LD-0003", TruckToken = "sp-hauler-06", LoaderToken = "sp-loader-03";

        public readonly string Base = Path.Combine(Path.GetTempPath(), "sitepulse-a7-" + Guid.NewGuid().ToString("N").Substring(0, 12));
        public double T;
        public RunRecorder Recorder;
        public readonly List<string> Observed = new List<string>(), Device = new List<string>(), Presenter = new List<string>();
        public readonly List<MachineSample[]> Frames = new List<MachineSample[]>();
        public readonly List<double> FrameTimes = new List<double>();

        public string Dir => Recorder.Directory;

        public SyntheticRun(Action<RunHeader> edit = null, string runId = null, Func<double> clock = null)
        {
            var header = new RunHeader
            {
                RunId = runId ?? "run-20261006T140000Z",
                StartedAtUtc = Start,
                Instance = "sitepulse",
                Tenant = "sitepulse-tenant",
                Manifest = "quarry_fleet_live · 2 devices",
                Seed = "per device",
                SiteOriginLatitude = 39.0,
                SiteOriginLongitude = -117.0,
            };
            header.Devices.Add(new RunDevice { Id = Truck, Token = TruckToken, Kind = "Hauler" });
            header.Devices.Add(new RunDevice { Id = Loader, Token = LoaderToken, Kind = "Loader" });
            header.Build = new BuildInfo { GitSha = "79bf619b38ea", TrackedTreeClean = true, SdkCommit = "69446d47b09f", UnityVersion = "6000.5.3f1", ScriptingBackend = "IL2CPP" };
            edit?.Invoke(header);
            Recorder = RunRecorder.Create(Base, header,
                new[] { new SimMachine(Truck, SimKind.Hauler), new SimMachine(Loader, SimKind.Loader) }, clock ?? (() => T), () => Start + TimeSpan.FromSeconds(T));
        }

        public static MachineSample Sample(float x, float z, float heading, float fuel = 50f, byte mode = 0, byte phase = 0) => new MachineSample
        {
            X = x, Y = 12.5f, Z = z, Heading = heading, Pitch = 1.5f, Roll = -0.5f, P1 = 3f, P2 = -4f, Steer = 7f, Travel = x + z,
            FuelPct = fuel, EngineTempC = 91f, EngineHours = 4321.5f, PayloadT = 88f, TyrePressureKpa = 701f, Mode = mode, Phase = phase, Loaded = true, OnTrack = mode == 0,
        };

        /// <summary>Writes frames from <see cref="T"/> for <paramref name="seconds"/> at 20 Hz: the truck drives east along z=10 and burns fuel; the loader stands.</summary>
        public void Frame(double t, MachineSample truck, MachineSample loader)
        {
            T = t;
            var frame = new[] { truck, loader };
            Recorder.WriteSimFrame(t, frame);
            Frames.Add(frame);
            FrameTimes.Add(t);
        }

        public void Drive(double from, double seconds, float fuelFrom = 50f, float fuelTo = 40f)
        {
            var n = (int)Math.Round(seconds * 20);
            for (var i = 0; i <= n; i++)
            {
                var t = from + i / 20.0;
                var f = (float)(i / (double)n);
                Frame(t, Sample(100f + 5f * (float)(t - from), 10f, 90f, fuelFrom + (fuelTo - fuelFrom) * f), Sample(0f, 0f, 0f, 70f));
            }
        }

        public ObservedLine Obs(double t, ObservedLine line)
        {
            T = t;
            Recorder.Observed(line);
            Observed.Add(line.ToJson());
            return line;
        }

        public DeviceLine Dev(double t, DeviceLine line)
        {
            T = t;
            Recorder.Device(line);
            Device.Add(line.ToJson());
            return line;
        }

        public PresenterLine Pres(double t, PresenterLine line)
        {
            T = t;
            Recorder.Presenter(line);
            Presenter.Add(line.ToJson());
            return line;
        }

        public static ObservedLine Measurement(string device, string name, double value, DateTimeOffset occurred, DateTimeOffset observed, bool snapshot = false)
        {
            var l = ObservedLine.Of(ObservedKinds.Measurement);
            l.Device = device;
            l.Name = name;
            l.Value = value;
            l.OccurredAt = occurred;
            l.ObservedAt = observed;
            l.FromSnapshot = snapshot;
            return l;
        }

        public static ObservedLine Alarm(string device, string token, string key, string state, DateTimeOffset at, string severity = "MAJOR")
        {
            var l = ObservedLine.Of(ObservedKinds.Alarm);
            l.Device = device;
            l.Token = token;
            l.Name = key;
            l.MetricKey = "fuel_pct";
            l.State = state;
            l.Severity = severity;
            l.OccurredAt = at;
            l.ObservedAt = at.AddMilliseconds(120);
            return l;
        }

        public static ObservedLine Command(string device, string token, string name, string status, DateTimeOffset queued, DateTimeOffset seen)
        {
            var l = ObservedLine.Of(ObservedKinds.Command);
            l.Device = device;
            l.Token = token;
            l.Name = name;
            l.State = status;
            l.QueuedAt = queued;
            l.ObservedAt = seen;
            return l;
        }

        public static ObservedLine Status(string source, string state, DateTimeOffset at)
        {
            var l = ObservedLine.Of(ObservedKinds.Status);
            l.Name = source;
            l.State = state;
            l.OccurredAt = at;
            return l;
        }

        public static DeviceLine Row(string machine, string kind, string text)
        {
            var l = DeviceLine.Of(DeviceKinds.Timeline, machine);
            l.RowKind = kind;
            l.Text = text;
            return l;
        }

        public RecordingData Reload()
        {
            Recorder.Close();
            return RecordingData.Load(Dir);
        }

        public void Dispose()
        {
            try { Recorder.Close(); } catch (Exception) { /* a test that broke the run on purpose */ }
            try { if (Directory.Exists(Base)) Directory.Delete(Base, true); } catch (IOException) { }
        }
    }
}
