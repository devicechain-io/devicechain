// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

using System;
using System.Collections.Generic;
using System.IO;
using System.Text;
using System.Text.Json;
using DeviceChain.Sitepulse.Domain;
using DeviceChain.Sitepulse.Recording;

namespace DeviceChain.Sitepulse.Replay
{
    /// <summary>
    /// <c>render.json</c>, written beside the frames: the account of what the footage is. The video's description and captions are written
    /// from it. It says in plain words that the frames are a replay of a recorded live run, which run, and when it was recorded; it carries no
    /// credential (every value passes the Redactor).
    /// </summary>
    public static class RenderReport
    {
        public const string FileName = "render.json";
        public const string Statement = "These frames were rendered offline from a recorded live run. They are a replay, not a live capture: say so in the video's description and captions.";
        public const string Overlay = "No HUD, no mode badge and no replay tag is drawn on any frame (maintainer decision D4); the disclosure is the description's and the captions'.";
        public const string AcceleratedCaption = "Accelerated simulation clock";

        public static string Build(RecordingData data, string recordingPath, ShotFile file, IReadOnlyList<PlannedShot> plan, IReadOnlyDictionary<string, int> framesWritten,
            DateTimeOffset renderedAtUtc, BuildInfo renderBuild, string error)
        {
            var h = data.Header;
            using var ms = new MemoryStream();
            using (var w = new Utf8JsonWriter(ms, new JsonWriterOptions { Indented = true }))
            {
                w.WriteStartObject();
                w.WriteString("status", error == null ? "complete" : "failed");
                JsonIo.WriteStr(w, "error", error);
                w.WriteBoolean("isReplay", true);
                w.WriteString("statement", Statement);
                w.WriteString("frameOverlay", Overlay);
                w.WriteString("runId", h.RunId);
                w.WriteString("sourceRecording", recordingPath);
                w.WriteString("recordingStartedAtUtc", JsonIo.Time(h.StartedAtUtc));
                w.WriteString("recordingDate", h.StartedAtUtc.UtcDateTime.ToString("yyyy-MM-dd", System.Globalization.CultureInfo.InvariantCulture));
                w.WriteNumber("recordingDurationSeconds", JsonIo.Seconds(data.Duration));
                JsonIo.WriteStr(w, "platformVersion", h.PlatformVersion);
                JsonIo.WriteStr(w, "instance", h.Instance);
                JsonIo.WriteStr(w, "tenant", h.Tenant);
                w.WriteStartObject("recordingBuild");
                JsonIo.WriteStr(w, "gitSha", h.Build.GitSha);
                JsonIo.WriteStr(w, "sdkCommit", h.Build.SdkCommit);
                w.WriteEndObject();
                w.WriteStartObject("renderBuild");
                JsonIo.WriteStr(w, "gitSha", renderBuild.GitSha);
                JsonIo.WriteStr(w, "unityVersion", renderBuild.UnityVersion);
                w.WriteEndObject();
                w.WriteString("renderedAtUtc", JsonIo.Time(renderedAtUtc));
                w.WriteNumber("fps", file.Fps);
                w.WriteStartArray("shots");
                foreach (var p in plan)
                {
                    var s = p.Shot;
                    var end = p.Start + s.Duration;
                    w.WriteStartObject();
                    w.WriteString("name", s.Name);
                    w.WriteString("directory", s.Name);
                    w.WriteStartObject("startEvent");
                    w.WriteString("selector", s.StartEvent.ToString());
                    w.WriteString("found", p.Event.Description);
                    w.WriteNumber("atRunSeconds", JsonIo.Seconds(p.Event.T));
                    w.WriteString("atUtc", JsonIo.Time(p.Event.Utc));
                    w.WriteEndObject();
                    w.WriteNumber("offsetSeconds", s.Offset);
                    w.WriteNumber("startRunSeconds", JsonIo.Seconds(p.Start));
                    w.WriteString("startUtc", JsonIo.Time(h.StartedAtUtc + TimeSpan.FromSeconds(p.Start)));
                    w.WriteNumber("durationSeconds", s.Duration);
                    w.WriteNumber("frames", p.Frames);
                    w.WriteNumber("framesWritten", framesWritten != null && framesWritten.TryGetValue(s.Name, out var n) ? n : 0);
                    w.WriteNumber("width", s.Width);
                    w.WriteNumber("height", s.Height);
                    w.WriteString("aspect", s.Aspect);
                    w.WriteString("camera", s.Camera.Rig.ToString().ToLowerInvariant());
                    if (s.Camera.Target != null) w.WriteString("cameraTarget", s.Camera.Target);
                    w.WriteNumber("prerollSeconds", s.Preroll);
                    var accelerated = h.AnyAccelerated(p.Start, end);
                    w.WriteBoolean("acceleratedClock", accelerated);
                    if (accelerated) w.WriteString("captionRequired", AcceleratedCaption);
                    w.WriteEndObject();
                }

                w.WriteEndArray();
                w.WriteEndObject();
            }

            return Redactor.Redact(Encoding.UTF8.GetString(ms.ToArray())) + "\n";
        }
    }
}
