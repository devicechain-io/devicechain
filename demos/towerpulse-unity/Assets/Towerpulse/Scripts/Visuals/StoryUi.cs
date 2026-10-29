// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

using System;
using System.Collections.Generic;
using System.Globalization;
using DeviceChain.Towerpulse.Model;
using DeviceChain.Towerpulse.Plane;
using DeviceChain.Towerpulse.Story;
using UnityEngine;
using UnityEngine.UI;

namespace DeviceChain.Towerpulse.Visuals
{
    /// <summary>
    /// The cards and the captions: a status strip, a zone card, an alarm card, a command card that keeps "delivered" and "done" apart, and a chiller card.
    /// Every number on a card is a value the plane reported or the platform returned; the UI formats and never computes. Each fact carries the name of its
    /// witness: <i>device (simulated)</i>, <i>platform</i> or <i>preview</i>, so the three are never confused. Laid out in pixels of a 1600 x 900 frame.
    /// </summary>
    [DefaultExecutionOrder(200)]
    public sealed class StoryUi : MonoBehaviour
    {
        const float W = 1600f, H = 900f;
        const float DockW = 424f, DockX = W - DockW - 20f;
        static readonly Color Panel = new Color(0.05f, 0.07f, 0.10f, 0.90f);
        static readonly Color Ink = new Color(0.94f, 0.96f, 0.98f, 1f);
        static readonly Color Muted = new Color(0.64f, 0.70f, 0.78f, 1f);
        static readonly Color Queued = new Color(0.92f, 0.78f, 0.30f, 1f);
        static readonly Color Delivered = new Color(0.32f, 0.64f, 1.00f, 1f);
        static readonly Color DoneGreen = new Color(0.26f, 0.88f, 0.48f, 1f);
        static readonly Color Failed = new Color(0.96f, 0.26f, 0.22f, 1f);
        static readonly Color Amber = new Color(1.00f, 0.74f, 0.20f, 1f);

        sealed class Card
        {
            public RectTransform Rt;
            public CanvasGroup Group;
            public Vector2 Size;
            public bool Want;
            public Image Bar;
        }

        StoryApp story;
        Canvas canvas;
        RectTransform canvasRect;
        Font font, mono;
        Sprite disc, ring;
        Card hud, zone, alarm, command, chiller, caption, legend;
        Image leaderZone, leaderChiller, dotZone, dotChiller;

        // hud
        Text hudTitle, hudTime, hudMode, hudAccel;
        Image hudAccelBg;
        // zone
        Text zTitle, zWitness, zTemp, zSet, zOcc, zDamper, zPlatform;
        // alarm
        Text aTitle, aLine, aState;
        // command
        Text cTitle, cTarget, cId;
        readonly Image[] nodes = new Image[3];
        readonly Image[] links = new Image[2];
        readonly Text[] nodeNum = new Text[3];
        readonly Text[] nodeName = new Text[3];
        readonly Text[] nodeGloss = new Text[3];
        readonly Text[] nodeWhen = new Text[3];
        Text cFoot;
        // chiller
        Text hTitle, hState, hReading, hAlarm, hRestart, hCop;
        Image hDot;
        // caption + legend
        Text capBeat, capText, legText;

        public RectTransform CanvasRect => canvasRect;

        /// <summary>Set while a take plays a shot whose clock jumps (see <see cref="StoryText.Jump"/>): shown in the status strip's orange chip, as the model's own speed label is.</summary>
        public string JumpLabel { get; set; }

        /// <summary>Names the cards that are standing, for tests and the stills log.</summary>
        public string Visible()
        {
            var l = new List<string>();
            AddIfShown(l, "hud", hud);
            AddIfShown(l, "zone", zone);
            AddIfShown(l, "alarm", alarm);
            AddIfShown(l, "command", command);
            AddIfShown(l, "chiller", chiller);
            AddIfShown(l, "caption", caption);
            return string.Join(",", l);
        }

        static void AddIfShown(List<string> l, string name, Card c)
        {
            if (c != null && c.Group.alpha > 0.5f) l.Add(name);
        }

        public void Bind(StoryApp owner)
        {
            story = owner;
            font = Resources.GetBuiltinResource<Font>("LegacyRuntime.ttf");
            mono = Font.CreateDynamicFontFromOSFont(new[] { "Consolas", "Cascadia Mono", "Lucida Console", "DejaVu Sans Mono", "Menlo", "Courier New" }, 16);
            if (mono == null) mono = font;
            disc = MakeDisc(64, 0f);
            ring = MakeDisc(64, 0.80f);
            BuildCanvas();
            BuildHud();
            BuildZone();
            BuildAlarm();
            BuildCommand();
            BuildChiller();
            BuildCaption();
        }

        // ------------------------------------------------------------------ building blocks

        void BuildCanvas()
        {
            var go = new GameObject("StoryCanvas", typeof(RectTransform));
            go.transform.SetParent(transform, false);
            canvas = go.AddComponent<Canvas>();
            canvas.renderMode = RenderMode.ScreenSpaceCamera;
            canvas.worldCamera = story.Director != null ? story.Director.Camera : Camera.main;
            canvas.planeDistance = 1f;
            var scaler = go.AddComponent<CanvasScaler>();
            scaler.uiScaleMode = CanvasScaler.ScaleMode.ScaleWithScreenSize;
            scaler.referenceResolution = new Vector2(W, H);
            scaler.screenMatchMode = CanvasScaler.ScreenMatchMode.MatchWidthOrHeight;
            scaler.matchWidthOrHeight = 0.5f;
            canvasRect = (RectTransform)go.transform;
        }

        static Sprite MakeDisc(int n, float hole)
        {
            var tex = new Texture2D(n, n, TextureFormat.RGBA32, false) { hideFlags = HideFlags.HideAndDontSave, wrapMode = TextureWrapMode.Clamp, filterMode = FilterMode.Bilinear };
            float c = (n - 1) / 2f, r = n / 2f - 1f;
            var px = new Color32[n * n];
            for (int y = 0; y < n; y++)
                for (int x = 0; x < n; x++)
                {
                    float d = Mathf.Sqrt((x - c) * (x - c) + (y - c) * (y - c));
                    float a = Mathf.Clamp01(r - d + 0.5f);
                    if (hole > 0f) a *= Mathf.Clamp01((d - r * hole) + 0.5f);
                    px[y * n + x] = new Color32(255, 255, 255, (byte)(a * 255f));
                }
            tex.SetPixels32(px);
            tex.Apply();
            return Sprite.Create(tex, new Rect(0, 0, n, n), new Vector2(0.5f, 0.5f), n);
        }

        RectTransform Rect(Transform parent, string name, Vector2 topLeft, Vector2 size)
        {
            var go = new GameObject(name, typeof(RectTransform));
            go.transform.SetParent(parent, false);
            var rt = (RectTransform)go.transform;
            rt.anchorMin = rt.anchorMax = rt.pivot = new Vector2(0, 1);
            rt.anchoredPosition = new Vector2(topLeft.x, -topLeft.y);
            rt.sizeDelta = size;
            return rt;
        }

        Image Img(Transform parent, string name, Vector2 pos, Vector2 size, Color col, Sprite sprite = null)
        {
            var rt = Rect(parent, name, pos, size);
            var im = rt.gameObject.AddComponent<Image>();
            im.color = col;
            im.sprite = sprite;
            im.raycastTarget = false;
            return im;
        }

        Text Txt(Transform parent, string name, string text, Vector2 pos, Vector2 size, int fontSize, Color col, bool bold = false, TextAnchor anchor = TextAnchor.UpperLeft, bool monospace = false)
        {
            var rt = Rect(parent, name, pos, size);
            var t = rt.gameObject.AddComponent<Text>();
            t.font = monospace ? mono : font;
            t.fontSize = fontSize;
            t.fontStyle = bold ? FontStyle.Bold : FontStyle.Normal;
            t.color = col;
            t.alignment = anchor;
            t.horizontalOverflow = HorizontalWrapMode.Overflow;
            t.verticalOverflow = VerticalWrapMode.Overflow;
            t.raycastTarget = false;
            t.text = text;
            return t;
        }

        Card NewCard(string name, Vector2 pos, Vector2 size, Color accent)
        {
            var rt = Rect(canvasRect, name, pos, size);
            var c = new Card { Rt = rt, Size = size, Group = rt.gameObject.AddComponent<CanvasGroup>() };
            Img(rt, "Back", Vector2.zero, size, Panel);
            c.Bar = Img(rt, "Bar", Vector2.zero, new Vector2(6, size.y), accent);
            c.Group.alpha = 0;
            c.Group.blocksRaycasts = false;
            c.Group.interactable = false;
            return c;
        }

        static void Set(Text t, string s)
        {
            if (t != null && t.text != s) t.text = s;
        }

        // ------------------------------------------------------------------ the cards

        void BuildHud()
        {
            hud = NewCard("Hud", new Vector2(20, 18), new Vector2(660, 74), Delivered);
            hudTitle = Txt(hud.Rt, "Title", "TOWERPULSE", new Vector2(20, 8), new Vector2(200, 28), 22, Ink, true);
            hudTime = Txt(hud.Rt, "Time", "", new Vector2(190, 9), new Vector2(200, 28), 21, Ink, false, TextAnchor.UpperLeft, true);
            hudAccelBg = Img(hud.Rt, "AccelBack", new Vector2(398, 9), new Vector2(180, 26), new Color(0.95f, 0.38f, 0.10f, 1f));
            hudAccel = Txt(hud.Rt, "Accel", "", new Vector2(398, 9), new Vector2(180, 26), 16, Color.white, true, TextAnchor.MiddleCenter);
            hudMode = Txt(hud.Rt, "Mode", "", new Vector2(20, 42), new Vector2(630, 24), 16, Muted);
        }

        void BuildZone()
        {
            zone = NewCard("ZoneCard", new Vector2(0, 0), new Vector2(316, 196), Amber);
            zTitle = Txt(zone.Rt, "Title", "", new Vector2(20, 10), new Vector2(300, 24), 20, Ink, true);
            zWitness = Txt(zone.Rt, "Witness", "", new Vector2(20, 34), new Vector2(300, 18), 13, Muted);
            zTemp = Txt(zone.Rt, "Temp", "", new Vector2(20, 54), new Vector2(300, 52), 46, Ink, true);
            Txt(zone.Rt, "SetLabel", "SETPOINT", new Vector2(20, 116), new Vector2(120, 16), 12, Muted);
            zSet = Txt(zone.Rt, "Set", "", new Vector2(20, 130), new Vector2(150, 24), 20, Ink, false, TextAnchor.UpperLeft, true);
            Txt(zone.Rt, "OccLabel", "PEOPLE", new Vector2(176, 116), new Vector2(120, 16), 12, Muted);
            zOcc = Txt(zone.Rt, "Occ", "", new Vector2(176, 130), new Vector2(60, 24), 20, Ink, false, TextAnchor.UpperLeft, true);
            Txt(zone.Rt, "DamperLabel", "DAMPER", new Vector2(250, 116), new Vector2(80, 16), 12, Muted);
            zDamper = Txt(zone.Rt, "Damper", "", new Vector2(250, 130), new Vector2(86, 24), 20, Ink, false, TextAnchor.UpperLeft, true);
            zPlatform = Txt(zone.Rt, "Platform", "", new Vector2(20, 166), new Vector2(310, 20), 14, Muted);
            leaderZone = Img(canvasRect, "ZoneLeader", Vector2.zero, new Vector2(10, 2), new Color(1, 1, 1, 0.85f));
            leaderZone.rectTransform.pivot = new Vector2(0, 0.5f);
            dotZone = Img(canvasRect, "ZoneDot", Vector2.zero, new Vector2(14, 14), Color.white, disc);
            dotZone.rectTransform.pivot = new Vector2(0.5f, 0.5f);
            zone.Rt.SetAsLastSibling();
        }

        void BuildAlarm()
        {
            alarm = NewCard("AlarmCard", new Vector2(DockX, 108), new Vector2(DockW, 112), Failed);
            aTitle = Txt(alarm.Rt, "Title", "", new Vector2(22, 10), new Vector2(390, 26), 21, Ink, true);
            aLine = Txt(alarm.Rt, "Line", "", new Vector2(22, 44), new Vector2(390, 22), 17, Ink);
            aState = Txt(alarm.Rt, "State", "", new Vector2(22, 72), new Vector2(390, 32), 14, Muted);
        }

        void BuildCommand()
        {
            command = NewCard("CommandCard", new Vector2(DockX, 236), new Vector2(DockW, 268), Delivered);
            cTitle = Txt(command.Rt, "Title", "", new Vector2(22, 10), new Vector2(390, 26), 21, Ink, true);
            cTarget = Txt(command.Rt, "Target", "", new Vector2(22, 38), new Vector2(390, 20), 16, Muted);
            Txt(command.Rt, "IdLabel", "COMMAND ID", new Vector2(22, 64), new Vector2(120, 14), 11, Muted);
            cId = Txt(command.Rt, "Id", "", new Vector2(22, 78), new Vector2(390, 20), 15, Ink, false, TextAnchor.UpperLeft, true);
            float[] cx = { 76, 212, 348 };
            for (int i = 0; i < 2; i++)
                links[i] = Img(command.Rt, "Link" + i, new Vector2(cx[i] + 20, 125), new Vector2(cx[i + 1] - cx[i] - 40, 4), new Color(1, 1, 1, 0.22f));
            for (int i = 0; i < 3; i++)
            {
                nodes[i] = Img(command.Rt, "Node" + i, new Vector2(cx[i] - 20, 108), new Vector2(40, 40), Muted, ring);
                nodeNum[i] = Txt(command.Rt, "Num" + i, (i + 1).ToString(CultureInfo.InvariantCulture), new Vector2(cx[i] - 20, 108), new Vector2(40, 40), 20, Ink, true, TextAnchor.MiddleCenter);
                nodeName[i] = Txt(command.Rt, "Name" + i, CommandCard.StageLabel((CardStage)i), new Vector2(cx[i] - 64, 154), new Vector2(128, 18), 16, Ink, true, TextAnchor.UpperCenter);
                nodeGloss[i] = Txt(command.Rt, "Gloss" + i, CommandCard.StageGloss((CardStage)i), new Vector2(cx[i] - 64, 174), new Vector2(128, 18), 14, Muted, false, TextAnchor.UpperCenter);
                nodeWhen[i] = Txt(command.Rt, "When" + i, "", new Vector2(cx[i] - 64, 194), new Vector2(128, 34), 13, Muted, false, TextAnchor.UpperCenter, true);
            }
            cFoot = Txt(command.Rt, "Foot", "SENT means delivered to the device.\nOnly SUCCESSFUL means the job is done.", new Vector2(22, 232), new Vector2(390, 34), 14, Muted);
        }

        void BuildChiller()
        {
            chiller = NewCard("ChillerCard", new Vector2(0, 0), new Vector2(380, 206), Amber);
            hTitle = Txt(chiller.Rt, "Title", "", new Vector2(22, 10), new Vector2(340, 26), 21, Ink, true);
            hDot = Img(chiller.Rt, "Dot", new Vector2(22, 44), new Vector2(22, 22), Amber, disc);
            hState = Txt(chiller.Rt, "State", "", new Vector2(52, 41), new Vector2(300, 28), 22, Amber, true);
            hReading = Txt(chiller.Rt, "Reading", "", new Vector2(22, 76), new Vector2(350, 20), 15, Ink, false, TextAnchor.UpperLeft, true);
            hAlarm = Txt(chiller.Rt, "Alarm", "", new Vector2(22, 100), new Vector2(350, 20), 15, Ink);
            hRestart = Txt(chiller.Rt, "Restart", "", new Vector2(22, 124), new Vector2(350, 20), 15, Ink);
            hCop = Txt(chiller.Rt, "Cop", "", new Vector2(22, 156), new Vector2(350, 40), 14, Muted);
            leaderChiller = Img(canvasRect, "ChillerLeader", Vector2.zero, new Vector2(10, 2), new Color(1, 1, 1, 0.85f));
            leaderChiller.rectTransform.pivot = new Vector2(0, 0.5f);
            dotChiller = Img(canvasRect, "ChillerDot", Vector2.zero, new Vector2(14, 14), Color.white, disc);
            dotChiller.rectTransform.pivot = new Vector2(0.5f, 0.5f);
            chiller.Rt.SetAsLastSibling();
        }

        void BuildCaption()
        {
            caption = NewCard("Caption", new Vector2(20, H - 96), new Vector2(760, 76), Delivered);
            capBeat = Txt(caption.Rt, "Beat", "", new Vector2(22, 8), new Vector2(700, 18), 13, Muted);
            capText = Txt(caption.Rt, "Text", "", new Vector2(22, 28), new Vector2(720, 36), 26, Ink, true);
            legend = NewCard("Legend", new Vector2(W - 724, H - 74), new Vector2(704, 54), Muted);
            legText = Txt(legend.Rt, "Text", "device (simulated): what the controllers sent and received  ·  platform: what the platform reported back\npreview: a scripted stand-in when no platform is connected", new Vector2(18, 6), new Vector2(680, 42), 14, Muted);
        }

        // ------------------------------------------------------------------ per frame

        void LateUpdate()
        {
            if (story == null || story.Problem != null || story.App == null || story.App.Plane == null) return;
            Refresh(Time.unscaledDeltaTime, false);
        }

        /// <summary>Draws the cards for the current state; <paramref name="snap"/> skips the fade so a still shows the settled frame.</summary>
        public void Refresh(float dt, bool snap)
        {
            var app = story.App;
            var plane = app.Plane;
            var model = plane.Model;
            var shot = story.Director != null ? story.Director.Current : null;
            bool free = shot == null;

            Wants(free, shot, out var wantZone, out var wantAlarm, out var wantCommand, out var wantChiller);
            var headline = story.Alarms.Headline();
            var latest = story.Commands.Cards.Count > 0 ? story.Commands.Cards[story.Commands.Cards.Count - 1] : null;

            // the card pairs: a shot about the hot room shows the room's alarm and command, a shot about the chiller shows the chiller's
            AlarmFact alarmShown = headline;
            CommandCard commandShown = latest;
            if (shot != null && shot.Cards.Contains("chiller"))
            {
                var ch = OfflinePreview.ByLayout(plane, BuildingLayout.AnnexChiller);
                alarmShown = story.Alarms.Find(ch.ExternalId, "chiller-silent");
                commandShown = story.Commands.Latest(ch.ExternalId);
            }
            else if (shot != null && shot.Cards.Contains("zone"))
            {
                var hot = OfflinePreview.ByLayout(plane, BuildingLayout.HotZone);
                alarmShown = story.Alarms.Find(hot.ExternalId, "zone-hot");
                commandShown = story.Commands.Latest(hot.ExternalId);
            }

            DrawHud(app);
            if (wantZone) DrawZone(plane, model);
            if (wantAlarm && alarmShown != null) DrawAlarm(alarmShown, plane); else wantAlarm = false;
            if (wantCommand && commandShown != null) DrawCommand(commandShown, plane); else wantCommand = false;
            if (wantChiller) DrawChiller(plane, model);
            DrawCaption(shot);

            hud.Want = true;
            zone.Want = wantZone;
            alarm.Want = wantAlarm;
            command.Want = wantCommand;
            chiller.Want = wantChiller;
            caption.Want = shot != null && !string.IsNullOrEmpty(shot.Caption);
            legend.Want = true;

            float k = Mathf.Clamp01(dt * 6f);
            foreach (var c in new[] { hud, zone, alarm, command, chiller, caption, legend })
                c.Group.alpha = snap ? (c.Want ? 1f : 0f) : Mathf.MoveTowards(c.Group.alpha, c.Want ? 1f : 0f, k);
            leaderZone.enabled = dotZone.enabled = zone.Group.alpha > 0.01f;
            leaderChiller.enabled = dotChiller.enabled = chiller.Group.alpha > 0.01f;
            var lz = leaderZone.color; lz.a = 0.85f * zone.Group.alpha; leaderZone.color = lz; dotZone.color = lz;
            var lc = leaderChiller.color; lc.a = 0.85f * chiller.Group.alpha; leaderChiller.color = lc; dotChiller.color = lc;
        }

        static void Wants(bool free, Shot shot, out bool z, out bool a, out bool c, out bool h)
        {
            if (free) { z = a = c = h = true; return; }
            z = shot.Cards.Contains("zone");
            a = shot.Cards.Contains("alarm");
            c = shot.Cards.Contains("command");
            h = shot.Cards.Contains("chiller");
        }

        static string Hms(double simS)
        {
            if (double.IsNaN(simS)) return "";
            var f = TimeClock.Format(simS);
            int sp = f.IndexOf(' ');
            return sp >= 0 ? f.Substring(sp + 1) : f;
        }

        static string One(double v) { return v.ToString("0.0", CultureInfo.InvariantCulture); }

        static string Pretty(TowerPlane plane, string externalId)
        {
            var d = plane.Find(externalId);
            if (d == null) return externalId;
            if (d.Kind == PlaneKind.Vav)
            {
                int i = plane.Model.Layout.ZoneIndex(d.LayoutId);
                return i >= 0 ? plane.Model.Layout.Zones[i].Name : d.LayoutId;
            }
            if (d.Kind == PlaneKind.Chiller) return d.LayoutId == BuildingLayout.AnnexChiller ? "Annex chiller" : "Tower chiller";
            return d.LayoutId;
        }

        void DrawHud(Towerpulse.DevicePlane.TowerpulseLiveApp app)
        {
            var plane = app.Plane;
            Set(hudTime, TimeClock.Format(plane.Model.TimeS));
            var accel = JumpLabel ?? StoryText.Accelerated(plane.Clock.Speed);
            hudAccel.enabled = hudAccelBg.enabled = accel != null;
            Set(hudAccel, accel ?? "");
            if (app.ReplaySource != null)
            {
                Set(hudMode, "REPLAY  ·  " + app.ReplaySource.Header.RunId + "  ·  no platform is connected; facts are as recorded, with their sources");
                hudMode.color = Amber; hud.Bar.color = Amber;
            }
            else if (app.Offline)
            {
                Set(hudMode, "OFFLINE PREVIEW  ·  no platform is connected; facts marked preview are scripted");
                hudMode.color = Amber; hud.Bar.color = Amber;
            }
            else
            {
                bool live = app.ObserverState.Measurements.IsLive;
                Set(hudMode, app.Status == "running" ? (live ? "LIVE  ·  devices simulated here, platform reports back" : "LIVE  ·  devices up, platform stream not live yet") : app.Status);
                hudMode.color = live ? DoneGreen : Amber; hud.Bar.color = live ? DoneGreen : Amber;
            }
        }

        void DrawZone(TowerPlane plane, BuildingModel model)
        {
            var dev = OfflinePreview.ByLayout(plane, BuildingLayout.HotZone);
            var r = Readings.For(model, dev);
            double temp = double.NaN, set = double.NaN, occ = double.NaN, damper = double.NaN;
            if (r != null)
            {
                if (r.TryGetValue(Wire.Temp, out var t)) temp = t;
                if (r.TryGetValue(Wire.Setpoint, out var s)) set = s;
                if (r.TryGetValue(Wire.Occupancy, out var o)) occ = o;
                if (r.TryGetValue(Wire.Damper, out var d)) damper = d;
            }
            var tint = TintRamp.Colour(temp, set);
            var col = new Color(tint.R, tint.G, tint.B, 1f);
            Set(zTitle, Pretty(plane, dev.ExternalId).ToUpperInvariant());
            Set(zWitness, FactLabels.Tag(FactSource.Device) + " · " + dev.ExternalId);
            Set(zTemp, double.IsNaN(temp) ? "no reading" : One(temp) + " °C");
            zTemp.color = col; zone.Bar.color = col;
            Set(zSet, double.IsNaN(set) ? "—" : One(set) + " °C");
            Set(zOcc, double.IsNaN(occ) ? "—" : ((int)occ).ToString(CultureInfo.InvariantCulture));
            Set(zDamper, double.IsNaN(damper) ? "—" : ((int)damper).ToString(CultureInfo.InvariantCulture) + " %");
            Set(zPlatform, PlatformSees(dev.ExternalId, Wire.Temp, "°C"));

            Vector3? anchor = null;
            if (story.View != null && story.View.TryAnchor(BuildingLayout.HotZone, out var wp)) anchor = wp;
            PlaceAnchored(zone, leaderZone, dotZone, anchor, ZoneOffset);
        }

        /// <summary>Where the zone card sits from the point it labels, in frame pixels (x right, y down). The shot file's framing leaves this side of the room clear.</summary>
        public static Vector2 ZoneOffset = new Vector2(290, -120);
        public static Vector2 ChillerOffset = new Vector2(120, -230);

        // What the platform itself returned for a metric, with how long ago; a plain statement when it has not (yet) returned anything.
        string PlatformSees(string externalId, string metric, string unit)
        {
            var app = story.App;
            if (app.ReplaySource != null) return "platform: as recorded";
            if (app.Offline) return "platform: not connected (preview)";
            if (app.Observed == null || !app.TryDeviceToken(externalId, out var token)) return "platform: not watching this device";
            if (!app.Observed.TryGet(token, out var d) || !d.Measurements.TryGetValue(metric, out var v)) return "platform: nothing returned yet";
            double age = (DateTimeOffset.UtcNow - v.ObservedAt).TotalSeconds;
            return "platform: " + One(v.Value) + " " + unit + "  ·  " + Mathf.Max(0, (int)age).ToString(CultureInfo.InvariantCulture) + " s ago";
        }

        void DrawAlarm(AlarmFact a, TowerPlane plane)
        {
            var sev = a.Severity == "CRITICAL" ? Failed : (a.Severity == "MAJOR" ? new Color(1f, 0.52f, 0.14f, 1f) : Amber);
            alarm.Bar.color = a.Active ? sev : Muted;
            Set(aTitle, "ALARM  ·  " + a.Key);
            Set(aLine, a.Severity + "  ·  " + Pretty(plane, a.Device));
            Set(aState, (a.Active ? "ACTIVE since " + Hms(a.RaisedAtS) : "CLEARED at " + Hms(a.ClearedAtS)) + "  ·  " + FactLabels.Tag(a.Source));
            aLine.color = a.Active ? sev : Muted;
        }

        void DrawCommand(CommandCard c, TowerPlane plane)
        {
            Set(cTitle, "COMMAND  ·  " + c.Name);
            Set(cTarget, "to " + Pretty(plane, c.Device));
            Set(cId, c.Id);
            double pulse = 0.55 + 0.45 * Math.Sin(Time.unscaledTime * 5.0);
            for (int i = 0; i < 3; i++)
            {
                var stage = (CardStage)i;
                bool reached = c.Has(stage);
                Color fill;
                if (stage == CardStage.Queued) fill = Queued;
                else if (stage == CardStage.Delivered) fill = Delivered;
                else fill = c.Outcome == CardOutcome.Failed ? Failed : DoneGreen;

                var name = CommandCard.StageLabel(stage);
                var gloss = CommandCard.StageGloss(stage);
                if (stage == CardStage.Done && c.Outcome == CardOutcome.Failed) { name = c.FailReason; gloss = "not done"; }

                nodes[i].sprite = reached ? disc : ring;
                nodes[i].color = reached ? fill : new Color(Muted.r, Muted.g, Muted.b, 0.55f);
                nodeNum[i].color = reached ? new Color(0.04f, 0.06f, 0.09f, 1f) : Muted;
                nodeName[i].color = reached ? fill : Muted;
                Set(nodeName[i], name);
                Set(nodeGloss[i], gloss);
                Set(nodeWhen[i], reached ? Hms(c.AtS(stage)) + "\n" + Short(c.SourceOf(stage)) : "");
            }
            // delivered, but the job is not done: the third node waits, visibly
            if (c.Delivered && !c.Terminal)
            {
                nodes[2].color = new Color(DoneGreen.r, DoneGreen.g, DoneGreen.b, (float)(0.25 + 0.5 * pulse));
                nodeGloss[2].color = DoneGreen;
            }
            else nodeGloss[2].color = Muted;
            links[0].color = c.Has(CardStage.Delivered) ? Delivered : new Color(1, 1, 1, 0.22f);
            links[1].color = c.Done ? DoneGreen : (c.Outcome == CardOutcome.Failed ? Failed : new Color(1, 1, 1, 0.22f));
            command.Bar.color = c.Done ? DoneGreen : (c.Outcome == CardOutcome.Failed ? Failed : (c.Delivered ? Delivered : Queued));
        }

        static string Short(FactSource s)
        {
            switch (s)
            {
                case FactSource.Platform: return "platform";
                case FactSource.Preview: return "preview";
                default: return "device";
            }
        }

        void DrawChiller(TowerPlane plane, BuildingModel model)
        {
            var dev = OfflinePreview.ByLayout(plane, BuildingLayout.AnnexChiller);
            var r = Readings.For(model, dev);
            var lamp = LampLook.Of(r);
            var lc = LampLook.Colour(lamp);
            var col = new Color(lc.R, lc.G, lc.B, 1f);
            Set(hTitle, "ANNEX CHILLER");
            Set(hState, LampLook.Label(lamp));
            hState.color = col; hDot.color = new Color(col.r, col.g, col.b, (float)LampLook.Brightness(lamp, Time.unscaledTime) * 0.9f + 0.1f); chiller.Bar.color = col;
            if (r != null)
            {
                r.TryGetValue(Wire.ChwSupply, out var chw);
                r.TryGetValue(Wire.Power, out var kw);
                Set(hReading, "chw " + One(chw) + " °C   power " + One(kw) + " kW");
            }
            else Set(hReading, "sending nothing  (" + FactLabels.Tag(FactSource.Device) + ")");

            var al = story.Alarms.Find(dev.ExternalId, "chiller-silent");
            if (al == null) { Set(hAlarm, "alarm: none"); hAlarm.color = Muted; }
            else
            {
                Set(hAlarm, "alarm: " + al.Key + "  " + al.Severity + "  " + (al.Active ? "ACTIVE" : "CLEARED") + "  [" + Short(al.Source) + "]");
                hAlarm.color = al.Active ? Failed : Muted;
            }

            var rc = story.Commands.Latest(dev.ExternalId);
            if (rc == null) { Set(hRestart, "restart: not sent"); hRestart.color = Muted; }
            else
            {
                string word = rc.Done ? "SUCCESSFUL (job done)" : (rc.Outcome == CardOutcome.Failed ? rc.FailReason : (rc.Delivered ? "SENT (delivered, not done)" : "QUEUED"));
                Set(hRestart, rc.Name + ": " + word);
                hRestart.color = rc.Done ? DoneGreen : (rc.Outcome == CardOutcome.Failed ? Failed : (rc.Delivered ? Delivered : Queued));
            }
            Set(hCop, CopLine(dev.ExternalId));

            Vector3? anchor = null;
            if (story.View != null && story.View.TryUnitPosition(BuildingLayout.AnnexChiller, out var wp)) anchor = wp;
            PlaceAnchored(chiller, leaderChiller, dotChiller, anchor, ChillerOffset);
        }

        // The platform computes COP from the chiller's two kilowatt metrics; this card only shows the value the platform returned.
        string CopLine(string externalId)
        {
            var app = story.App;
            if (app.Offline) return "COP, calculated by the platform: not connected";
            if (app.Observed == null || !app.TryDeviceToken(externalId, out var token)) return "COP, calculated by the platform: not watching";
            if (!app.Observed.TryGet(token, out var d) || !d.Measurements.TryGetValue("cop", out var v)) return "COP, calculated by the platform: nothing returned yet";
            double age = (DateTimeOffset.UtcNow - v.ObservedAt).TotalSeconds;
            return "COP, calculated by the platform: " + v.Value.ToString("0.0#", CultureInfo.InvariantCulture) + "  ·  " + Mathf.Max(0, (int)age).ToString(CultureInfo.InvariantCulture) + " s ago";
        }

        void DrawCaption(Shot shot)
        {
            if (shot == null) return;
            Set(capBeat, (shot.Beat ?? "").ToUpperInvariant());
            Set(capText, shot.Caption ?? "");
        }

        // Puts a card beside the world point it speaks about, keeps it inside the frame and clear of the right-hand dock, and draws the leader.
        void PlaceAnchored(Card card, Image leader, Image dot, Vector3? world, Vector2 offset)
        {
            var cam = canvas.worldCamera;
            if (!world.HasValue || cam == null) return;
            var sp = cam.WorldToScreenPoint(world.Value);
            if (sp.z <= 0) { card.Want = false; return; }
            // Frame fractions straight from the camera, not through the canvas transform (its scale is not set until the canvas has been drawn once).
            float sx = canvasRect.rect.width > 1f ? canvasRect.rect.width : W, sy = canvasRect.rect.height > 1f ? canvasRect.rect.height : H;
            var p = new Vector2(sp.x / cam.pixelWidth * sx, (1f - sp.y / cam.pixelHeight) * sy); // top-left coordinates of the anchor point, y down
            float x = p.x + offset.x, y = p.y + offset.y - card.Size.y / 2f;
            float limit = DockX - 10f;
            if (x + card.Size.x > limit) x = p.x - Mathf.Abs(offset.x) - card.Size.x;
            x = Mathf.Clamp(x, 14f, limit - card.Size.x);
            y = Mathf.Clamp(y, 100f, sy - card.Size.y - 110f);
            card.Rt.anchoredPosition = new Vector2(x, -y);

            var edge = new Vector2(x < p.x ? x + card.Size.x : x, y + card.Size.y / 2f);
            dot.rectTransform.anchoredPosition = new Vector2(p.x, -p.y);
            var d = edge - p;
            leader.rectTransform.anchoredPosition = new Vector2(p.x, -p.y);
            leader.rectTransform.sizeDelta = new Vector2(d.magnitude, 2f);
            leader.rectTransform.localRotation = Quaternion.Euler(0, 0, Mathf.Atan2(-d.y, d.x) * Mathf.Rad2Deg);
        }
    }
}
