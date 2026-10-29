// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

using System;
using System.Collections;
using System.Collections.Generic;
using System.Globalization;
using System.IO;
using DeviceChain.Demos.Platform;
using DeviceChain.Towerpulse.Story;
using UnityEngine;

namespace DeviceChain.Towerpulse.Visuals
{
    /// <summary>
    /// Renders a recorded run into frames, one directory per shot, from a take plan (a shot file whose shots carry the recorded run they come from and the
    /// model-clock span they cover). It is the replay path of a take: the plane is the same seeded model the run used, the story boards are fed by the
    /// recording's facts, and nothing here invents a state. The wait between two beats is CUT, not faked: a shot whose clock jumps says so on screen
    /// (<see cref="StoryText.Jump"/>), and the plan checks the caption against the clock it shows.
    ///
    ///   Towerpulse.exe -tp-replay RUNDIR -tp-render PLAN.json -tp-run A -tp-out FRAMES [-tp-fps 30] -screen-width 1920 -screen-height 1080 -screen-fullscreen 0
    ///
    /// Time is stepped by hand (the frame count, not the wall clock), so the same recording and plan give the same frames on any machine. Writes
    /// FRAMES/render.json beside the shot directories, and exits 0 only when every frame was written.
    /// </summary>
    public sealed class TakeRenderer : MonoBehaviour
    {
        public const string RenderFlag = "-tp-render", RunFlag = "-tp-run", OutFlag = "-tp-out", FpsFlag = "-tp-fps";

        /// <summary>True when the command line asks for a render.</summary>
        public static bool Requested { get { return Array.IndexOf(Environment.GetCommandLineArgs(), RenderFlag) >= 0; } }

        StoryApp story;
        int exitCode = 1;

        void Start()
        {
            story = GetComponent<StoryApp>();
            StartCoroutine(Run());
        }

        static string Arg(string flag)
        {
            var v = CommandLineArgs.Get(Environment.GetCommandLineArgs(), flag, out var err);
            if (err != null) throw new InvalidOperationException(err);
            return v;
        }

        IEnumerator Run()
        {
            yield return null;
            string failure = null;
            ShotList plan = null;
            string outDir = null, run = null;
            int fps = 30;
            try
            {
                var planPath = Arg(RenderFlag);
                outDir = Arg(OutFlag);
                run = Arg(RunFlag);
                var fpsText = Arg(FpsFlag);
                if (planPath == null || outDir == null || run == null) throw new InvalidOperationException(RenderFlag + ", " + RunFlag + " and " + OutFlag + " are all required");
                if (fpsText != null && (!int.TryParse(fpsText, NumberStyles.Integer, CultureInfo.InvariantCulture, out fps) || fps < 1 || fps > 120)) throw new InvalidOperationException(FpsFlag + " must be 1 to 120");
                if (story == null || story.Problem != null) throw new InvalidOperationException("the story could not start: " + (story == null ? "no StoryApp" : story.Problem));
                if (story.App.ReplaySource == null) throw new InvalidOperationException("a render needs a recording: pass -tp-replay RUNDIR");
                var res = ShotFile.Parse(File.ReadAllText(planPath));
                if (!res.Ok) throw new InvalidOperationException("the take plan is not valid: " + res.Report());
                plan = res.List;
            }
            catch (Exception e) { failure = e.Message; }
            if (failure != null) { Debug.LogError("[towerpulse] render: " + failure); Application.Quit(2); yield break; }

            var app = story.App;
            var director = story.Director;
            var ui = story.Ui;
            Time.captureFramerate = fps;
            app.Frozen = true;
            director.UseShots(plan);

            Directory.CreateDirectory(outDir);
            var shotsDone = new List<string>();
            int written = 0;
            foreach (var shot in plan.Shots)
            {
                if (shot.Run != run) continue;
                if (!shot.HasModel) { Debug.LogError("[towerpulse] render: shot " + shot.Id + " has no model clock; a take shot must say what time it covers"); Application.Quit(2); yield break; }
                var dir = Path.Combine(outDir, shot.Id);
                Directory.CreateDirectory(dir);
                int frames = Mathf.Max(1, Mathf.RoundToInt((float)(shot.DurationS * fps)));
                var jump = StoryText.Jump(shot.Rate);
                for (int i = 0; i < frames; i++)
                {
                    double showS = shot.StartS + (double)i / fps;
                    double target = plan.ModelAt(showS);
                    if (target + 1e-6 < app.Plane.Model.TimeS)
                    {
                        Debug.LogError("[towerpulse] render: shot " + shot.Id + " needs the model at " + TimeClockText(target) + " but it is already at " + TimeClockText(app.Plane.Model.TimeS) + ": the plan's shots must move forward in model time");
                        Application.Quit(2);
                        yield break;
                    }
                    StepModelTo(app, target);
                    director.Seek(showS);
                    ui.JumpLabel = jump;
                    yield return null;            // every Update and LateUpdate of the frame runs with this model time and camera
                    yield return new WaitForEndOfFrame();
                    var tex = ScreenCapture.CaptureScreenshotAsTexture();
                    var bytes = tex.EncodeToJPG(95);
                    Destroy(tex);
                    File.WriteAllBytes(Path.Combine(dir, "frame_" + (i + 1).ToString("000000", CultureInfo.InvariantCulture) + ".jpg"), bytes);
                    written++;
                }
                shotsDone.Add(shot.Id);
                Debug.Log("[towerpulse] render: shot " + shot.Id + ": " + frames + " frames" + (jump != null ? " (" + jump + ")" : ""));
            }
            if (shotsDone.Count == 0) { Debug.LogError("[towerpulse] render: the plan has no shot for run \"" + run + "\""); Application.Quit(2); yield break; }
            File.WriteAllText(Path.Combine(outDir, "render.json"), "{\"fps\":" + fps + ",\"run\":\"" + run + "\",\"runId\":\"" + app.ReplaySource.Header.RunId + "\",\"frames\":" + written + ",\"width\":" + Screen.width + ",\"height\":" + Screen.height + ",\"shots\":[\"" + string.Join("\",\"", shotsDone) + "\"]}\n");
            exitCode = 0;
            Debug.Log("[towerpulse] render: done, " + written + " frames in " + outDir);
            Application.Quit(exitCode);
        }

        static string TimeClockText(double s) { return Model.TimeClock.Format(s); }

        // the model moves in steps of at most 5 model seconds, as the rehearsal does, so a long jump runs the same plane a real minute would
        static void StepModelTo(DevicePlane.TowerpulseLiveApp app, double target)
        {
            double speed = app.Plane.Clock.Speed;
            int guard = 0;
            while (app.Plane.Model.TimeS < target - 1e-9 && guard++ < 100000)
            {
                double left = target - app.Plane.Model.TimeS;
                app.AdvanceOffline(Math.Min(5.0, left) / speed);
            }
        }
    }
}
