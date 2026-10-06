// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

using System;
using System.Collections.Generic;
using System.Globalization;
using System.IO;
using DeviceChain.Sitepulse.DevicePlane;
using DeviceChain.Sitepulse.Platform;
using DeviceChain.Sitepulse.Recording;
using DeviceChain.Sitepulse.Simulation;
using DeviceChain.Sitepulse.Tasks;
using DeviceChain.Sitepulse.Visuals;
using UnityEngine;

namespace DeviceChain.Sitepulse.App
{
    /// <summary>The recorder's command line: on by default; <c>-sitepulse-record &lt;dir&gt;</c> chooses where, <c>-sitepulse-no-record</c> turns it off.</summary>
    public sealed class RecordOptions
    {
        public const string DirFlag = "-sitepulse-record";
        public const string OffFlag = "-sitepulse-no-record";
        public const string PlatformVersionFlag = "-sitepulse-platform-version";

        public bool Enabled { get; private set; } = true;
        public string Directory { get; private set; }
        public string PlatformVersion { get; private set; }

        public static RecordOptions Parse(IReadOnlyList<string> args, out string error)
        {
            var problems = new List<string>();
            var o = new RecordOptions();
            var dir = CommandLineArgs.Get(args, DirFlag, out var e1);
            if (e1 != null) problems.Add(e1);
            var version = CommandLineArgs.Get(args, PlatformVersionFlag, out var e2);
            if (e2 != null) problems.Add(e2);
            var off = false;
            if (args != null)
                foreach (var a in args)
                    if (a == OffFlag) off = true;
            if (off && dir != null) problems.Add($"{OffFlag} and {DirFlag} contradict each other");
            if (problems.Count > 0)
            {
                error = string.Join("\n", problems);
                return null;
            }

            o.Enabled = !off;
            o.Directory = dir;
            o.PlatformVersion = version;
            error = null;
            return o;
        }
    }

    /// <summary>What the live recorder watches: the scene's own parts and the live run's. Plain references; nothing here is created by the recorder.</summary>
    public sealed class LiveRecorderWorld
    {
        public QuarryFleetPreview Fleet;

        /// <summary>The machines to record, in order. Defaults to the fleet's own (a test hands in rigs it made).</summary>
        public Func<IEnumerable<MachineRig>> Machines;

        /// <summary>The scene's clock speed and whether a machine is on its routine track. Default: the fleet's.</summary>
        public Func<double> TimeScale;
        public Func<string, bool> IsOnTrack;
        public DeviceFleet Plane;
        public TaskDirector Director;
        public Timeline Timeline;
        public PresenterControls Presenter;
        public PlatformObserver Observer;
        public IReadOnlyList<RunDevice> Devices;
        public string Tenant, Instance, Manifest;
        public string BuildInfoPath;
    }

    /// <summary>
    /// Records the live run (<see cref="RunRecorder"/>) from the app's side: it samples every machine's pose 20 times a second from what
    /// the scene drew, and listens to the observer (what the viewer was shown), the device plane (what the devices did), the timeline and
    /// the presenter. It is a passenger: the observer and the plane call it through taps that are dropped if it throws, and a disk that
    /// refuses a write stops the recording, not the run. Main thread, except the sample sink (pool threads, which the recorder allows).
    /// </summary>
    public sealed class LiveRecorder : IAckedSampleSink, IDisposable
    {
        const double FrameEvery = 1.0 / RunRecorder.NominalHz;
        const double StatsEverySeconds = 60.0;

        readonly RunRecorder recorder;
        readonly LiveRecorderWorld world;
        readonly List<MachineRig> rigs = new List<MachineRig>();
        readonly MachineSample[] frame;
        readonly Vector3[] lastPosition;
        readonly float[] travel;
        readonly bool[] seen;
        readonly string[] names;
        readonly bool[] missingLogged;
        readonly Dictionary<string, DeviceSessionHost> hosts = new Dictionary<string, DeviceSessionHost>(StringComparer.Ordinal);
        readonly Action<string> log;
        double nextFrame, nextStats;
        double lastScale = double.NaN;
        bool closed;

        LiveRecorder(RunRecorder recorder, LiveRecorderWorld world, Action<string> log)
        {
            this.recorder = recorder;
            this.world = world;
            this.log = log;
            foreach (var r in world.Machines()) rigs.Add(r);
            frame = new MachineSample[rigs.Count];
            lastPosition = new Vector3[rigs.Count];
            travel = new float[rigs.Count];
            seen = new bool[rigs.Count];
            missingLogged = new bool[rigs.Count];
            names = new string[rigs.Count];
            for (var i = 0; i < rigs.Count; i++) names[i] = rigs[i] != null ? rigs[i].name : "machine " + i;
            if (world.Plane != null)
                foreach (var h in world.Plane.Hosts) hosts[h.ExternalId] = h;
        }

        public string Directory => recorder.Directory;
        public RunRecorder Recorder => recorder;

        /// <summary>The player's build record: <c>build-info.json</c> beside the player, or (in the Editor) in the project's Build folder.</summary>
        public static string FindBuildInfo()
        {
            var parent = Path.GetDirectoryName(Application.dataPath);
            var beside = Path.Combine(parent ?? ".", "build-info.json");
            if (File.Exists(beside)) return beside;
            return Path.Combine(parent ?? ".", "Build", "build-info.json");
        }

        /// <summary>
        /// Starts recording, or returns null when it cannot (the reason is logged: a run that cannot be recorded still runs). The base
        /// directory is <paramref name="options"/>' or the player's persistent data path under <c>Recordings</c>.
        /// </summary>
        public static LiveRecorder TryStart(RecordOptions options, LiveRecorderWorld world, Action<string> log, Func<double> clock = null, Func<string, Stream> openStream = null)
        {
            if (options == null || !options.Enabled) return null;
            if (world.Machines == null && world.Fleet == null) return null;
            world.Machines ??= () => world.Fleet.Machines;
            world.TimeScale ??= () => world.Fleet.timeScale;
            world.IsOnTrack ??= id => world.Fleet.IsOnTrack(id);
            try
            {
                var machines = new List<MachineRig>(world.Machines());
                if (machines.Count == 0)
                {
                    log("recording not started: the scene's fleet has no machines");
                    return null;
                }

                var table = new List<SimMachine>();
                foreach (var r in machines) table.Add(new SimMachine(r.name, r.Kind == MachineKind.Dozer ? SimKind.Dozer : r.Kind == MachineKind.Loader ? SimKind.Loader : SimKind.Hauler));

                var header = new RunHeader
                {
                    Build = BuildInfo.Read(world.BuildInfoPath ?? FindBuildInfo()),
                    PlatformVersion = options.PlatformVersion,
                    Instance = world.Instance,
                    Tenant = world.Tenant,
                    Manifest = world.Manifest,
                    Seed = "per device: FNV-1a of the device id (the local model's own seeds)",
                    SiteOriginLatitude = SiteDefinition.OriginLatitude,
                    SiteOriginLongitude = SiteDefinition.OriginLongitude,
                };
                if (world.Devices != null) header.Devices.AddRange(world.Devices);
                header.PresenterPresets.Add(string.Format(CultureInfo.InvariantCulture,
                    "prepare-low-fuel: the tank to {0}% and a burn that crosses {1}% within about {2} s",
                    PresenterActions.JustAbovePct, PresenterActions.LowFuelLinePct, PresenterActions.CrossWithinSeconds));

                var baseDir = options.Directory ?? Path.Combine(Application.persistentDataPath, "Recordings");
                var recorder = RunRecorder.Create(baseDir, header, table, clock, null, openStream);
                var live = new LiveRecorder(recorder, world, log);
                recorder.Faulted += why => log("recording STOPPED: " + why);
                live.Attach();
                log($"recording · {recorder.Directory} · run {recorder.RunId} · {table.Count} machines at {RunRecorder.NominalHz:0} Hz");
                return live;
            }
            catch (Exception e) when (e is IOException || e is UnauthorizedAccessException || e is ArgumentException)
            {
                log("recording not started: " + e.GetType().Name + ": " + e.Message);
                return null;
            }
        }

        void Attach()
        {
            // A tap that throws is removed by the one that calls it. The recording it was feeding is then incomplete, and says so: the
            // recorder is faulted (run.json: not ended cleanly, and the log says why) before the exception goes on to remove the tap.
            if (world.Observer != null) world.Observer.OnItem = item =>
            {
                try
                {
                    var line = RecordingMaps.Observed(item);
                    if (line != null) recorder.Observed(line);
                }
                catch (Exception e)
                {
                    recorder.Fail(e);
                    throw;
                }
            };
            if (world.Plane != null) world.Plane.OnEvent = e =>
            {
                try { OnDeviceEvent(e); }
                catch (Exception ex)
                {
                    recorder.Fail(ex);
                    throw;
                }
            };
            if (world.Timeline != null) world.Timeline.Added += OnTimelineRow;
            if (world.Presenter != null) world.Presenter.Said += OnPresenterSaid;
        }

        void OnDeviceEvent(DeviceEvent e)
        {
            var line = RecordingMaps.Device(e);
            if (line == null) return;
            recorder.Device(line);
            if (e.Kind != DeviceEventKind.Task || e.Task == null) return;
            var task = e.Task;
            var id = e.ExternalId;
            // the answer is recorded when the task layer gives it, from whichever thread completes it
            task.Completion.ContinueWith(t =>
            {
                try
                {
                    if (t.IsFaulted || t.IsCanceled) recorder.Device(RecordingMaps.Completed(id, task.Token, TaskResult.Fail("the task ended without an answer")));
                    else recorder.Device(RecordingMaps.Completed(id, task.Token, t.Result));
                }
                catch (Exception e) { recorder.Fail(e); }
            }, System.Threading.Tasks.TaskContinuationOptions.ExecuteSynchronously);
        }

        // The timeline and the presenter raise these as plain events: an exception here would travel into the task layer or the presenter,
        // which the recording must never be able to break. It faults the recording instead.
        void OnTimelineRow(string machine, TimelineRow row)
        {
            try
            {
                var l = DeviceLine.Of(DeviceKinds.Timeline, machine);
                l.RowKind = row.Kind;
                l.Text = row.Text;
                recorder.Device(l);
            }
            catch (Exception e) { recorder.Fail(e); }
        }

        void OnPresenterSaid(string text)
        {
            try { recorder.Presenter(PresenterLine.Of(PresenterKinds.Action, text)); }
            catch (Exception e) { recorder.Fail(e); }
        }

        /// <summary>A sample the broker took (any thread).</summary>
        public void Record(string externalId, string deviceToken, Sample sample)
        {
            try { recorder.Device(RecordingMaps.Sample(externalId, sample, recorder.UtcNow)); }
            catch (Exception e) { recorder.Fail(e); }
        }

        /// <summary>Once a frame, after the scene has moved the machines (LateUpdate): writes a simulation frame when one is due and notes a change of the scene's clock.</summary>
        public void Tick()
        {
            if (closed || recorder.IsFaulted) return;
            try { TickInner(); }
            catch (Exception e) { recorder.Fail(e); }
        }

        void TickInner()
        {
            var scale = world.TimeScale();
            if (double.IsNaN(lastScale) || Math.Abs(scale - lastScale) > 1e-6)
            {
                lastScale = scale;
                recorder.ClockChanged(scale);
            }

            var now = recorder.Now;
            if (now >= nextFrame)
            {
                Capture(now);
                // a hitch is not made up for: the next frame is due one step from now, and its own time says when it was
                nextFrame += FrameEvery;
                if (nextFrame <= now) nextFrame = now + FrameEvery;
            }

            if (now >= nextStats)
            {
                nextStats = now + StatsEverySeconds;
                log("recording · " + recorder.Stats);
            }
        }

        void Capture(double now)
        {
            for (var i = 0; i < rigs.Count; i++)
            {
                var rig = rigs[i];
                if (rig == null)
                {
                    // one machine gone (destroyed under us) does not stop the others: it is written where it was last seen, flagged missing
                    // (NaN where it was never seen), and said once
                    if (!missingLogged[i])
                    {
                        missingLogged[i] = true;
                        log($"recording · machine {names[i]} is gone from the scene: its frames are marked missing and the others go on");
                    }

                    if (!seen[i]) frame[i].X = frame[i].Y = frame[i].Z = float.NaN;
                    frame[i].Missing = true;
                    continue;
                }

                var tr = rig.transform;
                var p = tr.position;
                var e = tr.eulerAngles;
                if (seen[i])
                {
                    // distance along the way the machine faces, over the ground (a slope does not lengthen it)
                    var f = tr.forward;
                    var flat = Mathf.Sqrt(f.x * f.x + f.z * f.z);
                    var d = p - lastPosition[i];
                    if (flat > 1e-4f) travel[i] += (d.x * f.x + d.z * f.z) / flat;
                }

                seen[i] = true;
                lastPosition[i] = p;
                var s = new MachineSample { X = p.x, Y = p.y, Z = p.z, Heading = e.y, Pitch = e.x, Roll = e.z, Steer = rig.steer, Travel = travel[i], Loaded = rig.loaded };
                switch (rig.Kind)
                {
                    case MachineKind.Dozer: s.P1 = rig.bladeArm; s.P2 = rig.ripper; s.Steer = 0f; break;
                    case MachineKind.Loader: s.P1 = rig.boom; s.P2 = rig.bucket; s.Loaded = false; break;
                    case MachineKind.Hauler: s.P1 = rig.dump; break;
                }

                if (hosts.TryGetValue(rig.name, out var host))
                {
                    var m = host.Simulation.Model;
                    s.FuelPct = (float)m.FuelPct;
                    s.EngineTempC = (float)m.EngineTempC;
                    s.EngineHours = (float)m.EngineHours;
                    s.PayloadT = (float)m.PayloadT;
                    s.TyrePressureKpa = (float)m.TyrePressureKpa;
                }
                else
                {
                    s.FuelPct = s.EngineTempC = s.EngineHours = s.PayloadT = s.TyrePressureKpa = float.NaN;
                }

                s.OnTrack = world.IsOnTrack(rig.name);
                if (world.Director != null && world.Director.TryController(rig.name, out var c))
                {
                    s.Mode = (byte)c.Mode;
                    s.Phase = (byte)c.Phase;
                }

                frame[i] = s;
            }

            recorder.WriteSimFrame(now, frame);
        }

        /// <summary>Stops listening and closes the recording. Idempotent.</summary>
        public void Dispose()
        {
            if (closed) return;
            closed = true;
            if (world.Observer != null) world.Observer.OnItem = null;
            if (world.Plane != null) world.Plane.OnEvent = null;
            if (world.Timeline != null) world.Timeline.Added -= OnTimelineRow;
            if (world.Presenter != null) world.Presenter.Said -= OnPresenterSaid;
            recorder.Close();
            log($"recording closed · {recorder.Directory} · {recorder.Stats} · {recorder.Stats.TotalBytes / 1024.0:0.0} KiB on disk");
        }
    }
}
