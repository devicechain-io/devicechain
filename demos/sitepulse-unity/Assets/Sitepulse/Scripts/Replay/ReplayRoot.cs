// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

using System;
using System.Collections;
using System.Collections.Generic;
using System.Globalization;
using System.IO;
using DeviceChain.Sitepulse.Domain;
using DeviceChain.Sitepulse.Recording;
using DeviceChain.Sitepulse.Visuals;
using UnityEngine;
using UnityEngine.InputSystem;

namespace DeviceChain.Sitepulse.Replay
{
    /// <summary>
    /// The composition root of a replay: a recorded live run played back through the scene's own fleet and data layer, and, with
    /// <c>-sitepulse-render</c>, rendered offline shot by shot. It constructs no network object and cannot: this assembly references neither
    /// the platform nor the device plane (an EditMode test reads the assembly graph and fails if either appears). Machines stand where
    /// the recording drew them (interpolated, never re-simulated), the cards are filled from the recorded observations by
    /// <see cref="ReplayReadingSource"/> and judged against the recording's own clock, and the effects (dust, exhaust, falling rock)
    /// are stepped from those poses by the fleet's ordinary <see cref="IQuarryEffect.Step"/> path.
    ///
    /// Runs ahead of the fleet and the overlay (<c>-800</c>) so the poses are set before either reads them in the same frame.
    /// </summary>
    [DefaultExecutionOrder(-800)]
    public sealed class ReplayRoot : MonoBehaviour
    {
        /// <summary>What the root is given: the scene's own parts and where to say things. Nothing else.</summary>
        public sealed class Inputs
        {
            public QuarryFleetPreview Fleet;
            public IotOverlay Overlay;
            public ReplayOptions Options;

            /// <summary>Null for an offline render: nothing of the app's own is drawn.</summary>
            public IReplayScreen Screen;

            public string DefaultRecordings;
            public BuildInfo RenderBuild;
        }

        const float KeySeekSeconds = 5f;
        const int FleetWaitFrames = 300;
        const float PanelEverySeconds = 0.25f;

        enum Phase { Waiting, Playing, ShotBegin, Preroll, Capture, Finished }

        Inputs inputs;
        ReplayComposition composition;
        ReplaySession session;
        ReplayReadingSource source;
        QuarryFleetPreview fleet;
        IotOverlay overlay;
        IReplayScreen screen;
        RecordingData data;
        string recordingPath;

        bool ready, fleetAttached, paused, seeked, endAnnounced, showTimeline = true, manualSelection;
        int waitedFrames, selected;
        float nextPanel;
        string shownTimeline, shownHelp;
        readonly Dictionary<string, MachineRig> rigs = new Dictionary<string, MachineRig>(StringComparer.Ordinal);
        readonly Dictionary<string, float> lastTravel = new Dictionary<string, float>(StringComparer.Ordinal);
        readonly List<string> recorded = new List<string>();

        // render
        ShotFile shotFile;
        List<PlannedShot> plan;
        Phase phase = Phase.Waiting;
        int shotIndex, captured;
        bool firstFrame, wantCapture;
        string shotDir;
        float step;
        Camera cam;
        RenderTexture target;
        Texture2D readback;
        readonly Dictionary<string, int> written = new Dictionary<string, int>(StringComparer.Ordinal);

        public ReplaySession Session => session;
        public ReplayComposition Composition => composition;
        public bool Ready => ready;

        /// <summary>Adds the root to <paramref name="host"/> and starts it. Returns null when the replay cannot start (the reason is on screen, or in the log of a render).</summary>
        public static ReplayRoot Begin(GameObject host, Inputs inputs)
        {
            var root = host.AddComponent<ReplayRoot>();
            return root.Init(inputs) ? root : null;
        }

        bool Init(Inputs given)
        {
            inputs = given ?? throw new ArgumentNullException(nameof(given));
            fleet = given.Fleet;
            overlay = given.Overlay;
            screen = given.Screen;
            composition = ReplayComposition.For(given.Options);
            if (composition.Hud != (screen != null)) throw new InvalidOperationException("a render is given no screen, and an interactive replay is given one");
            if (fleet == null || overlay == null) return Fail("The replay cannot start", "the Sitepulse App component is not wired to the fleet and overlay (rebuild the scene or run Sitepulse > Quarry > Add Sitepulse App To Scene)", null);

            recordingPath = given.Options.RecordingDirectory(given.DefaultRecordings);
            try
            {
                data = RecordingData.Load(recordingPath);
            }
            catch (RecordingFormatException e)
            {
                return Fail("The recording cannot be played", e.Message, null);
            }
            catch (IOException e)
            {
                return Fail("The recording cannot be read", e.Message, null);
            }

            foreach (var w in data.Warnings) Debug.LogWarning("[sitepulse] replay: " + Redactor.Redact(w));
            session = new ReplaySession(data);
            foreach (var m in data.Sim.Machines) recorded.Add(m.Id);
            source = Wire(overlay, session, composition, line => Debug.LogWarning("[sitepulse] replay: " + Redactor.Redact(line)));

            if (given.Options.Render)
            {
                try
                {
                    shotFile = ShotFile.Parse(File.ReadAllText(given.Options.ShotsFile));
                    plan = ShotPlanner.Plan(shotFile, data, w => Debug.LogWarning("[sitepulse] render: " + Redactor.Redact(w)));
                }
                catch (ShotException e)
                {
                    return Fail("The shots cannot be rendered", e.Message, null);
                }
                catch (IOException e)
                {
                    return Fail("The shots file cannot be read", e.Message, null);
                }

                try { Directory.CreateDirectory(given.Options.OutDir); }
                catch (Exception e) when (e is IOException || e is UnauthorizedAccessException) { return Fail("The output directory cannot be made", e.Message, plan); }
                step = 1f / shotFile.Fps;
                Time.captureDeltaTime = step;
                QualitySettings.vSyncCount = 0;
                Application.targetFrameRate = -1;
                Debug.Log($"[sitepulse] render: {plan.Count} shot(s) from run {data.Header.RunId} at {shotFile.Fps} fps into {given.Options.OutDir}");
            }
            else
            {
                session.Seek(given.Options.StartSeconds);
                var date = data.Header.StartedAtUtc.UtcDateTime.ToString("yyyy-MM-dd", CultureInfo.InvariantCulture);
                screen.SetBadge(Badge(data.Header.RunId, date), false);
            }

            if (!given.Options.Render) overlay.Picked += OnPicked;
            ready = true;
            phase = given.Options.Render ? Phase.ShotBegin : Phase.Playing;
            return true;
        }

        /// <summary>
        /// Gives the overlay the replay's own reading source, the recording's clock (a card ages as it did live, not against today) and
        /// whether the cards say they are replayed: always for an interactive replay, never for a render.
        /// </summary>
        public static ReplayReadingSource Wire(IotOverlay overlay, ReplaySession session, ReplayComposition composition, Action<string> warn)
        {
            var source = new ReplayReadingSource(session, warn);
            overlay.Source = source;
            overlay.Clock = () => session.WallClock;
            overlay.ReplayTag = composition.ReplayTag;
            overlay.CommandsSince = session.Header.StartedAtUtc;
            // the drawer's rows and the route highlight come from the recording, rebuilt by the code the live app runs
            overlay.Proof = session.Proof;
            overlay.RouteOf = session.RouteOf;
            return source;
        }

        /// <summary>The interactive replay's badge, always on screen: what this is, which run, and when it was recorded.</summary>
        public static string Badge(string runId, string date) => $"REPLAY · recorded live run {runId} · {date}";

        bool Fail(string title, string message, List<PlannedShot> planned)
        {
            Debug.LogError("[sitepulse] " + Redactor.Redact(title + ": " + message));
            if (overlay != null) overlay.show = false;
            if (inputs.Options.Render)
            {
                if (data != null && shotFile != null && planned != null)
                    TryWriteReport(planned, message);
                Application.Quit(1);
            }
            else if (screen != null)
            {
                screen.SetBadge("REPLAY · not running", true);
                screen.ShowError(title, message);
            }

            ready = false;
            return false;
        }

        void TryWriteReport(List<PlannedShot> planned, string error)
        {
            try
            {
                File.WriteAllText(Path.Combine(inputs.Options.OutDir, RenderReport.FileName),
                    RenderReport.Build(data, recordingPath, shotFile, planned, written, DateTimeOffset.UtcNow, inputs.RenderBuild ?? BuildInfo.Unknown(), error));
            }
            catch (Exception e) when (e is IOException || e is UnauthorizedAccessException)
            {
                Debug.LogWarning("[sitepulse] render: cannot write render.json: " + e.Message);
            }
        }

        // ------------------------------------------------------------------ frame

        void Update()
        {
            if (!ready) return;
            if (!fleetAttached && !AttachFleet()) return;

            if (inputs.Options.Render) StepRender();
            else StepInteractive();

            ApplyPoses();
            if (inputs.Options.Render)
            {
                PlaceCamera();
                ApplyShotLayers();
            }
        }

        bool AttachFleet()
        {
            if (fleet.Count == 0)
            {
                if (++waitedFrames > FleetWaitFrames) Fail("The replay cannot start", "the scene's fleet spawned no machines", plan);
                return false;
            }

            foreach (var rig in fleet.Machines) rigs[rig.name] = rig;
            foreach (var id in recorded)
                if (!rigs.ContainsKey(id))
                    return Fail("The replay cannot start", $"the recording holds machine {id}, which the scene's fleet does not", plan);
            // every machine of the scene is taken off its track: those the recording holds are posed from it, and any it does not hold stand where they were
            foreach (var id in rigs.Keys) fleet.Detach(id);
            fleetAttached = true;
            seeked = true;
            return true;
        }

        void StepInteractive()
        {
            var kb = Keyboard.current;
            var delta = 0.0;
            var jumped = false;
            if (kb != null)
            {
                if (kb.spaceKey.wasPressedThisFrame) paused = !paused;
                if (kb.leftArrowKey.wasPressedThisFrame) { delta = -KeySeekSeconds; jumped = true; }
                if (kb.rightArrowKey.wasPressedThisFrame) { delta = KeySeekSeconds; jumped = true; }
                if (kb.homeKey.wasPressedThisFrame) { session.Seek(0); Jumped(); }
                if (kb.leftBracketKey.wasPressedThisFrame) { selected = (selected + recorded.Count - 1) % recorded.Count; manualSelection = true; overlay.Selected = recorded[selected]; }
                if (kb.rightBracketKey.wasPressedThisFrame) { selected = (selected + 1) % recorded.Count; manualSelection = true; overlay.Selected = recorded[selected]; }
                if (kb.tKey.wasPressedThisFrame) showTimeline = !showTimeline;
                if (kb.dKey.wasPressedThisFrame) overlay.Layers = overlay.Layers.With(drawer: overlay.Layers.Drawer == DrawerMode.Off ? DrawerMode.Side : DrawerMode.Off);
                if (kb.iKey.wasPressedThisFrame) overlay.Layers = overlay.Layers.With(panel: !overlay.Layers.Panel);
                if (kb.zKey.wasPressedThisFrame) overlay.Layers = overlay.Layers.With(zoneLabels: !overlay.Layers.ZoneLabels);
            }

            if (jumped)
            {
                session.Seek(session.Time + delta);
                Jumped();
                endAnnounced = false;
            }
            else if (!paused)
            {
                session.Advance(Math.Min(Time.unscaledDeltaTime, 0.25f));
                if (session.AtEnd)
                {
                    paused = true;
                    endAnnounced = true;
                }
            }

            // a paused replay is frozen whole, particles included
            Time.timeScale = paused ? 0f : 1f;
            UpdateScreen();
        }

        void UpdateScreen()
        {
            if (screen == null || Time.unscaledTime < nextPanel) return;
            nextPanel = Time.unscaledTime + PanelEverySeconds;
            if (!manualSelection && session.LastCommanded != null) selected = Math.Max(0, recorded.IndexOf(session.LastCommanded));
            var text = showTimeline && recorded.Count > 0 ? session.TimelineText(recorded[Math.Min(selected, recorded.Count - 1)]) : null;
            if (text != shownTimeline)
            {
                shownTimeline = text;
                screen.ShowTimeline(text);
            }

            var help = $"{Clock(session.Time)} / {Clock(session.Duration)}{(endAnnounced ? " · end" : paused ? " · paused" : "")} · SPACE pause · LEFT/RIGHT 5 s · HOME restart · [ ] machine · T timeline · D proof drawer · I machine panel · Z zone names";
            if (help != shownHelp)
            {
                shownHelp = help;
                screen.ShowHelp(help);
            }
        }

        static string Clock(double seconds)
        {
            var s = (int)Math.Max(0, seconds);
            return $"{s / 60:00}:{s % 60:00}";
        }

        void Jumped()
        {
            seeked = true;
            foreach (var e in QuarryEffects.Active.ToArray()) e.ClearParticles();
        }

        void ApplyPoses()
        {
            foreach (var id in recorded)
            {
                if (!session.TrySample(id, out var s)) continue;
                // a machine that was never seen has no pose to stand in: it stays where it is
                if (float.IsNaN(s.X) || float.IsNaN(s.Y) || float.IsNaN(s.Z)) continue;
                var distance = 0f;
                if (!seeked && lastTravel.TryGetValue(id, out var last))
                {
                    distance = s.Travel - last;
                    if (Mathf.Abs(distance) > 50f) distance = 0f;
                }

                lastTravel[id] = s.Travel;
                fleet.PoseRecorded(id, new Vector3(s.X, s.Y, s.Z), s.Heading, s.Pitch, s.Roll, s.P1, s.P2, s.Steer, s.Loaded, distance);
            }

            seeked = false;
        }

        // ------------------------------------------------------------------ offline render

        void StepRender()
        {
            switch (phase)
            {
                case Phase.ShotBegin:
                    BeginShot();
                    return;
                case Phase.Preroll:
                {
                    var shot = plan[shotIndex];
                    if (session.Time >= shot.Start - 1e-6)
                    {
                        phase = Phase.Capture;
                        captured = 0;
                        firstFrame = true;
                        goto case Phase.Capture;
                    }

                    // the lead-in is stepped in whole frames, and its last step lands exactly on the first frame's time
                    session.Advance(Math.Min(step, shot.Start - session.Time));
                    return;
                }

                case Phase.Capture:
                    if (!firstFrame) session.Advance(step);
                    firstFrame = false;
                    wantCapture = true;
                    return;
            }
        }

        void BeginShot()
        {
            var p = plan[shotIndex];
            cam = Camera.main;
            if (cam == null)
            {
                Fail("The render cannot start", "the scene has no camera tagged MainCamera", plan);
                return;
            }

            shotDir = Path.Combine(inputs.Options.OutDir, p.Shot.Name);
            try
            {
                Directory.CreateDirectory(shotDir);
            }
            catch (Exception e) when (e is IOException || e is UnauthorizedAccessException)
            {
                Fail("The output directory cannot be made", e.Message, plan);
                return;
            }

            if (target == null || target.width != p.Shot.Width || target.height != p.Shot.Height)
            {
                if (target != null) { cam.targetTexture = null; target.Release(); Destroy(target); }
                if (readback != null) Destroy(readback);
                target = new RenderTexture(p.Shot.Width, p.Shot.Height, 24, RenderTextureFormat.ARGB32);
                target.Create();
                readback = new Texture2D(p.Shot.Width, p.Shot.Height, TextureFormat.RGB24, false);
            }

            cam.targetTexture = target;
            // the follow camera's target (or the shot's own focus) is the selected machine: its card shows, whatever its state
            overlay.Selected = p.Shot.Selected;
            overlay.Pinned = null;
            overlay.Chips = null;
            session.Seek(p.PrerollFrom);
            Jumped();
            written[p.Shot.Name] = 0;
            phase = Phase.Preroll;
            Debug.Log($"[sitepulse] render: shot {p.Shot.Name} · {p.Event.Description} at run {p.Event.T:0.0} s · {p.Frames} frames {p.Shot.Width}x{p.Shot.Height}");
            StartCoroutine(CaptureLoop());
        }

        readonly List<string> pinnedNow = new List<string>();

        /// <summary>
        /// What the shot asks the data layer to draw, for this frame of it: the layers (the zone names fade in after their delay), the
        /// pinned cards (one more each stagger), the chips and the shot's clock, the featured metric and the card size. A portrait frame keeps
        /// its top and bottom clear. Never the badge, readiness panel, key help or a replay tag: this assembly draws none of them in a render.
        /// </summary>
        void ApplyShotLayers()
        {
            if (phase != Phase.Preroll && phase != Phase.Capture) return;
            var p = plan[shotIndex];
            var shot = p.Shot;
            var t = session.Time - p.Start;
            overlay.Layers = ShotDressing.LayersAt(shot, t);
            pinnedNow.Clear();
            var count = ShotDressing.PinnedCount(shot, t);
            for (var i = 0; i < count; i++) pinnedNow.Add(shot.PinnedCards[i]);
            overlay.Pinned = pinnedNow;
            overlay.Chips = p.Chips.Count == 0 ? null : p.Chips;
            overlay.ShotTime = t;
            overlay.Featured = shot.Featured;
            overlay.cardScale = shot.CardScale;
            overlay.SafeInsets = shot.Height > shot.Width ? new Vector2(PortraitSafeTop, PortraitSafeBottom) : Vector2.zero;
        }

        /// <summary>The share of a portrait frame's height the platforms' own captions and buttons cover at the top and the bottom.</summary>
        public const float PortraitSafeTop = 0.14f, PortraitSafeBottom = 0.20f;

        void PlaceCamera()
        {
            if (cam == null || phase == Phase.Waiting || phase == Phase.ShotBegin || phase == Phase.Finished) return;
            var p = plan[shotIndex];
            var t = Math.Max(0.0, session.Time - p.Start);
            var pose = CameraRigs.Evaluate(p.Shot.Camera, id =>
            {
                session.TrySample(id, out var s);
                return s;
            }, t, p.Shot.Duration);
            cam.fieldOfView = pose.Fov;
            cam.transform.position = pose.Position;
            cam.transform.rotation = Quaternion.LookRotation(pose.LookAt - pose.Position, Vector3.up);
        }

        // one capture per rendered frame, after the camera has drawn into the target
        IEnumerator CaptureLoop()
        {
            // a single loop for the whole run: BeginShot starts it once per shot, and it ends with the shot
            var mine = shotIndex;
            while (phase != Phase.Finished && shotIndex == mine)
            {
                yield return new WaitForEndOfFrame();
                if (!wantCapture) continue;
                wantCapture = false;
                if (phase != Phase.Capture) continue;
                var p = plan[shotIndex];
                RenderTexture.active = target;
                readback.ReadPixels(new Rect(0, 0, target.width, target.height), 0, 0, false);
                RenderTexture.active = null;
                var path = Path.Combine(shotDir, "frame_" + (captured + 1).ToString("D6", CultureInfo.InvariantCulture) + ".png");
                try
                {
                    File.WriteAllBytes(path, readback.EncodeToPNG());
                }
                catch (Exception e) when (e is IOException || e is UnauthorizedAccessException)
                {
                    Fail("A frame cannot be written", e.Message, plan);
                    yield break;
                }

                captured++;
                written[p.Shot.Name] = captured;
                if (captured < p.Frames) continue;

                Debug.Log($"[sitepulse] render: shot {p.Shot.Name} done · {captured} frames");
                if (++shotIndex >= plan.Count)
                {
                    phase = Phase.Finished;
                    cam.targetTexture = null;
                    TryWriteReport(plan, null);
                    Debug.Log($"[sitepulse] render: complete · {RenderReport.FileName} in {inputs.Options.OutDir}");
                    Application.Quit(0);
                }
                else phase = Phase.ShotBegin;
            }
        }

        // a click on a machine selects it, a click on nothing lets the panel follow the latest command again
        void OnPicked(string id)
        {
            var i = id == null ? -1 : recorded.IndexOf(id);
            manualSelection = i >= 0;
            if (i >= 0) selected = i;
        }

        void OnDestroy()
        {
            if (overlay != null) overlay.Picked -= OnPicked;
            if (inputs != null && inputs.Options.Render) Time.captureDeltaTime = 0f;
            Time.timeScale = 1f;
            if (cam != null) cam.targetTexture = null;
            if (target != null) { target.Release(); Destroy(target); }
            if (readback != null) Destroy(readback);
        }
    }
}
