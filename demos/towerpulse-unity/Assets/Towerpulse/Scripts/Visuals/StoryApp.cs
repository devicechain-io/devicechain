// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

using System;
using DeviceChain.Demos.Platform;
using DeviceChain.Towerpulse.DevicePlane;
using DeviceChain.Towerpulse.Model;
using DeviceChain.Towerpulse.Plane;
using DeviceChain.Towerpulse.Story;
using UnityEngine;

namespace DeviceChain.Towerpulse.Visuals
{
    /// <summary>
    /// Composes the story: the campus view, the cards, the camera director, and the two boards of facts the cards read. Facts reach the boards from exactly
    /// two places: what the device plane itself received and answered (its ledger), and what the platform's observer reports back. Offline there is no
    /// platform, so a scripted preview stands in for it and tags what it says as a preview; it is never created when a platform is connected.
    /// </summary>
    [DefaultExecutionOrder(100)]
    public sealed class StoryApp : MonoBehaviour
    {
        [SerializeField] TextAsset layoutFile;
        [SerializeField] TextAsset shotFile;

        TowerpulseLiveApp app;
        CampusPlan plan;
        CampusView view;
        StoryUi ui;
        ShotDirector director;
        int generation = -1;
        OfflinePreview preview;
        RecordingReplay replay;

        public CommandBoard Commands { get; private set; } = new CommandBoard();
        public AlarmBoard Alarms { get; private set; } = new AlarmBoard();
        public TowerpulseLiveApp App => app;
        public CampusView View => view;
        public StoryUi Ui => ui;
        public ShotDirector Director => director;
        public CampusPlan Plan => plan;
        public string Problem { get; private set; }

        public void Configure(TextAsset layout, TextAsset shots)
        {
            layoutFile = layout;
            shotFile = shots;
        }

        void Awake()
        {
            app = FindAnyObjectByType<TowerpulseLiveApp>();
            if (app == null) { Problem = "there is no TowerpulseLiveApp in the scene"; Debug.LogError("[towerpulse] " + Problem); return; }
            app.DrawStatus = false;
            try { plan = CampusPlan.Parse(layoutFile != null ? layoutFile.text : throw new InvalidOperationException("no layout file is assigned")); }
            catch (Exception e) { Problem = "the campus layout is not usable: " + e.Message; Debug.LogError("[towerpulse] " + Problem); return; }

            view = GetComponent<CampusView>();
            if (view == null) view = gameObject.AddComponent<CampusView>();
            view.Bind(app, plan);

            director = GetComponent<ShotDirector>();
            if (director == null) director = gameObject.AddComponent<ShotDirector>();
            if (director.Camera == null) director.Configure(shotFile, Camera.main);
            if (ShotList.LoopRequested(Environment.GetCommandLineArgs())) director.Loop = true;

            ui = GetComponent<StoryUi>();
            if (ui == null) ui = gameObject.AddComponent<StoryUi>();
            ui.Bind(this);

            // the take's render and the frame measurement are player modes, asked for on the command line; the scene itself is the same
            if (TakeRenderer.Requested) gameObject.AddComponent<TakeRenderer>();
            else if (FrameBench.Requested) gameObject.AddComponent<FrameBench>();
        }

        void Update()
        {
            if (app == null || app.Plane == null || Problem != null) return;
            if (generation != app.Generation) Rebuild();
            Pull();
            if (preview != null) preview.Step();
            if (replay != null) replay.Step();
            Commands.Sync(app.Ledger, app.Plane.Model.TimeS);
        }

        /// <summary>Applies the recorded facts the model has reached (a replay only). The frame renderer calls it between the short steps of a long jump.</summary>
        public void StepReplay()
        {
            if (replay == null) return;
            replay.Step();
            Commands.Sync(app.Ledger, app.Plane.Model.TimeS);
        }

        void Rebuild()
        {
            generation = app.Generation;
            Commands = new CommandBoard();
            Alarms = new AlarmBoard();
            recorded.Clear();
            // the preview exists only without a platform, and says so on every fact it makes
            // a replay feeds the boards from a recording; a plain offline plane has the scripted preview; a live one has neither
            replay = app.ReplaySource != null ? new RecordingReplay(app.Plane, app.ReplaySource.Facts, Commands, Alarms, app.Ledger) : null;
            preview = app.Offline && replay == null ? new OfflinePreview(app.Plane, Commands, Alarms, app.Ledger) : null;
        }

        // What the platform's observer reported since the last frame, folded into the boards as platform facts.
        void Pull()
        {
            if (app.Offline) return;
            double now = app.Plane.Model.TimeS;
            app.DrainObserved(item =>
            {
                if (item is CommandItem ci && ci.Command != null && !string.IsNullOrEmpty(ci.Command.Token))
                {
                    app.TryExternalId(ci.DeviceToken, out var ext);
                    Fold(StoryFact.Command(ci.Command.Token, ext ?? ci.DeviceToken, ci.Command.Name, ci.Command.Status, FactSource.Platform, now));
                }
                else if (item is AlarmItem ai) ApplyAlarm(ai, now);
                else if (item is AlarmSnapshotItem snap) foreach (var a in snap.Alarms) ApplyAlarm(a, now);
            });
        }

        void ApplyAlarm(AlarmItem ai, double now)
        {
            if (ai.Alarm == null) return;
            app.TryExternalId(ai.DeviceToken, out var ext);
            string device = ext ?? ai.DeviceToken;
            Fold(StoryFact.Alarm(device, ai.Alarm.AlarmKey, ai.Alarm.Severity, ai.Alarm.IsActive, FactSource.Platform, now));
        }

        // The one way a platform fact reaches the boards, and the one place it is recorded: a replay reads these facts back and applies them with the same call.
        void Fold(StoryFact f)
        {
            StoryFacts.Apply(f, Commands, Alarms, app.Ledger);
            // the observer re-reports a standing state on every poll; the recording keeps a fact when it says something NEW, which is what a replay needs
            // (applying a repeat changes nothing, so leaving it out cannot change a replayed state)
            var key = f.Kind + "|" + (f.Kind == StoryFactKind.Alarm ? f.Device + "|" + f.Key : f.Token);
            var state = f.Kind == StoryFactKind.Alarm ? (f.Active ? "active" : "cleared") : f.Status;
            if (recorded.TryGetValue(key, out var last) && last == state) return;
            recorded[key] = state;
            app.RecordFact(f);
        }

        readonly System.Collections.Generic.Dictionary<string, string> recorded = new System.Collections.Generic.Dictionary<string, string>();

        /// <summary>
        /// Rehearsal and stills, offline only: rebuild the plane at <paramref name="fromHour"/> and run it to <paramref name="toHour"/> in 5 s steps with the preview
        /// in the loop, so the boards hold exactly what a real run of that length would have shown. Leaves the model frozen there.
        /// </summary>
        public void Rehearse(double fromHour, double toHour)
        {
            app.Rewind(fromHour);
            Rebuild();
            double target = toHour * 3600.0;
            double step = 5.0 / app.Plane.Clock.Speed;
            int guard = 0;
            while (app.Plane.Model.TimeS < target && guard++ < 200000)
            {
                app.AdvanceOffline(step);
                preview.Step();
                Commands.Sync(app.Ledger, app.Plane.Model.TimeS);
            }
            app.Frozen = true;
            view.RefreshNow();
        }
    }
}
