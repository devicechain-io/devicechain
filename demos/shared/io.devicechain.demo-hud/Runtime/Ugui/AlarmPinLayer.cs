// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

using System;
using System.Collections.Generic;
using System.Globalization;
using UnityEngine;
using UnityEngine.UI;

namespace DeviceChain.Demos.Hud
{
    /// <summary>Where a device's pin stands in the world: just over its highest point, and the spot on the ground under it.</summary>
    public struct PinPlace
    {
        public Vector3 Top, Ground;
        public PinPlace(Vector3 top, Vector3 ground) { Top = top; Ground = ground; }
    }

    /// <summary>A device's name, written under its pin's tip (or at the device's top when it has no alarm): only for a shot's subjects.</summary>
    public readonly struct DeviceTag
    {
        public DeviceTag(string key, string text) { Key = key; Text = text; }
        public string Key { get; }
        public string Text { get; }
    }

    /// <summary>
    /// The alarm pins, drawn on the HUD's canvas at each device's place on screen: a pin is sized in screen space, always faces the
    /// viewer and is never dimmed by the scene's weather or grade. Under each pin a soft glow lies on the ground, which is all that is
    /// kept for a far shot. What is drawn and how is <see cref="AlarmPinBoard"/>'s; this places it.
    /// </summary>
    public sealed class AlarmPinLayer
    {
        sealed class PinView { public RectTransform Root, GlowRect; public Image Glow, Ring, Pin, Tick, Badge; public Text Count; public PinSeverity? Drawn; }
        sealed class TagView { public RectTransform Root; public Text Text; }
        readonly StoryHud hud; readonly RectTransform glows, pins, tags;
        readonly List<PinView> pool = new List<PinView>(); readonly List<TagView> tagPool = new List<TagView>();
        readonly List<KeyValuePair<float, int>> order = new List<KeyValuePair<float, int>>();
        public AlarmPinBoard Board { get; } = new AlarmPinBoard();
        /// <summary>How many device names may be written at once.</summary>
        public const int MaxTags = 4;
        /// <summary>How many pins were on screen in the last draw.</summary>
        public int OnScreen { get; private set; }
        /// <summary>
        /// The HUD's panels, each as { left, bottom, right, top } in shares of the frame. A pin, a tick, a glow or a name whose place
        /// falls on one (with a margin its own size) is not drawn: a world marker never sits on a panel. Set before <see cref="Draw"/>.
        /// </summary>
        public IReadOnlyList<float[]> Panels { get; set; }
        /// <summary>How many markers the panels hid in the last draw.</summary>
        public int Hidden { get; private set; }

        public AlarmPinLayer(StoryHud hud)
        {
            this.hud = hud;
            glows = HudUi.Rect("Pin glows", hud.Root.transform); HudUi.Stretch(glows);
            pins = HudUi.Rect("Alarm pins", hud.Root.transform); HudUi.Stretch(pins);
            tags = HudUi.Rect("Device tags", hud.Root.transform); HudUi.Stretch(tags);
        }

        PinView View(int i)
        {
            while (pool.Count <= i)
            {
                var v = new PinView { Root = HudUi.Rect("Pin", pins) };
                v.Glow = HudUi.Image("Glow", glows, hud.Sprites.Glow(), HudSprites.C(HudTheme.Amber, AlarmPins.GlowOpacity)); v.GlowRect = v.Glow.rectTransform;
                v.Ring = HudUi.Image("Ring", v.Root, hud.Sprites.Ring(), HudSprites.C(HudTheme.Amber));
                v.Pin = HudUi.Image("Teardrop", v.Root, null, Color.white);
                v.Tick = HudUi.Image("Cleared tick", v.Root, hud.Sprites.Tick(), Color.white);
                v.Badge = HudUi.Image("Count", v.Root, hud.Sprites.Dot(), HudSprites.C(HudTheme.Ink));
                v.Count = HudUi.Text("Count text", v.Badge.transform, hud.Font, 13, HudSprites.C(HudTheme.Edge), TextAnchor.MiddleCenter, true); HudUi.Stretch(v.Count.rectTransform);
                pool.Add(v);
            }
            return pool[i];
        }

        /// <summary>
        /// Draws the pins. <paramref name="active"/> is the devices with an active alarm now; <paramref name="place"/> answers where a
        /// device stands (null for one the scene does not draw); <paramref name="nowS"/> is the presentation clock.
        /// </summary>
        public void Draw(IEnumerable<PinInput> active, double nowS, Camera camera, Func<string, PinPlace?> place, IReadOnlyList<DeviceTag> deviceTags = null)
        {
            var draws = Board.Update(active, nowS);
            var eye = camera.transform.position; var pitch = Mathf.Abs(Mathf.Sin(camera.transform.eulerAngles.x * Mathf.Deg2Rad));
            order.Clear();
            var places = new PinPlace?[draws.Count];
            for (var i = 0; i < draws.Count; i++)
            {
                places[i] = place(draws[i].Key);
                if (places[i].HasValue) order.Add(new KeyValuePair<float, int>(-(places[i].Value.Top - eye).sqrMagnitude, i));
            }
            order.Sort((a, b) => a.Key.CompareTo(b.Key)); // far pins first, so a near one is drawn over them
            var used = 0; var tips = new Dictionary<string, Vector2>(StringComparer.Ordinal); Hidden = 0;
            foreach (var item in order)
            {
                var d = draws[item.Value]; var at = places[item.Value].Value;
                var top = camera.WorldToViewportPoint(at.Top);
                if (top.z <= 0.3f || top.x < -0.05f || top.x > 1.05f || top.y < -0.05f || top.y > 1.1f) continue;
                // the pin stands up from its tip: it is on a panel if any of its height is
                var reach = AlarmPins.MaxPx * AlarmPins.CriticalScale / HudTheme.ReferenceHeight;
                if (HudTheme.OnPanel(top.x, top.y + reach / 2, Panels, 0.012f, reach / 2 + 0.008f)) { Hidden++; continue; }
                var v = View(used++); var distance = (at.Top - eye).magnitude;
                var style = AlarmPins.Style(d.Severity); var tint = style.Outlined ? style.Edge : style.Fill;
                var px = AlarmPins.Pixels(distance, camera.fieldOfView) * d.Scale;
                v.Root.gameObject.SetActive(true);
                v.Root.anchorMin = v.Root.anchorMax = new Vector2(top.x, top.y); v.Root.pivot = new Vector2(0.5f, 0); v.Root.anchoredPosition = Vector2.zero; v.Root.sizeDelta = Vector2.zero;
                if (v.Drawn != d.Severity) { v.Pin.sprite = hud.Sprites.Pin(d.Severity); v.Drawn = d.Severity; }
                v.Pin.gameObject.SetActive(d.PinAlpha > 0.001f && px > 0.5f);
                v.Pin.color = new Color(1, 1, 1, d.PinAlpha);
                HudUi.Pin(v.Pin.rectTransform, new Vector2(0.5f, 0), new Vector2(0.5f, 0.03f), Vector2.zero, new Vector2(px * 0.75f, px));
                v.Ring.gameObject.SetActive(d.Ring >= 0);
                if (d.Ring >= 0)
                {
                    var size = px * (0.9f + 1.6f * d.Ring); v.Ring.color = HudSprites.C(tint, 0.8f * (1 - d.Ring));
                    HudUi.Pin(v.Ring.rectTransform, new Vector2(0.5f, 0), new Vector2(0.5f, 0.5f), new Vector2(0, px * 0.64f), new Vector2(size, size));
                }
                v.Tick.gameObject.SetActive(d.TickAlpha > 0.001f); v.Tick.color = new Color(1, 1, 1, d.TickAlpha);
                var tickPx = AlarmPins.Pixels(distance, camera.fieldOfView);
                HudUi.Pin(v.Tick.rectTransform, new Vector2(0.5f, 0), new Vector2(0.5f, 0), Vector2.zero, new Vector2(tickPx, tickPx));
                var badge = d.Count > 1 && d.PinAlpha > 0.5f && px > 1;
                v.Badge.gameObject.SetActive(badge);
                if (badge)
                {
                    var b = Mathf.Max(15, px * 0.5f);
                    HudUi.Pin(v.Badge.rectTransform, new Vector2(0.5f, 0), new Vector2(0.5f, 0.5f), new Vector2(px * 0.36f, px * 0.9f), new Vector2(b, b));
                    v.Count.text = d.Count.ToString(CultureInfo.InvariantCulture); v.Count.fontSize = Mathf.RoundToInt(b * 0.72f);
                }
                // the glow lies on the ground under the pin, as flat as the camera sees the ground
                var ground = camera.WorldToViewportPoint(at.Ground); var glow = ground.z > 0.3f && d.PinAlpha > 0.001f && !HudTheme.OnPanel(ground.x, ground.y, Panels, 0.03f, 0.02f);
                v.Glow.gameObject.SetActive(glow);
                if (glow)
                {
                    var w = AlarmPins.GlowPixels((at.Ground - eye).magnitude, camera.fieldOfView) * 2.2f;
                    v.GlowRect.anchorMin = v.GlowRect.anchorMax = new Vector2(ground.x, ground.y); v.GlowRect.pivot = new Vector2(0.5f, 0.5f);
                    v.GlowRect.anchoredPosition = Vector2.zero; v.GlowRect.sizeDelta = new Vector2(w, w * Mathf.Max(0.22f, pitch));
                    v.Glow.color = HudSprites.C(tint, AlarmPins.GlowOpacity * 1.6f * d.PinAlpha);
                }
                tips[d.Key] = new Vector2(top.x, top.y);
            }
            OnScreen = used;
            for (var i = used; i < pool.Count; i++) { pool[i].Root.gameObject.SetActive(false); pool[i].Glow.gameObject.SetActive(false); }

            var tagIndex = 0;
            if (deviceTags != null)
                foreach (var tag in deviceTags)
                {
                    if (tagIndex >= MaxTags || string.IsNullOrEmpty(tag.Text)) break;
                    Vector2 at;
                    if (!tips.TryGetValue(tag.Key, out at))
                    {
                        var where = place(tag.Key); if (!where.HasValue) continue;
                        var p = camera.WorldToViewportPoint(where.Value.Top);
                        if (p.z <= 0.3f || p.x < 0.03f || p.x > 0.97f || p.y < 0.05f || p.y > 0.95f) continue;
                        at = new Vector2(p.x, p.y);
                    }
                    if (HudTheme.OnPanel(at.x, at.y - 0.02f, Panels, 0.07f, 0.03f)) { Hidden++; continue; }
                    while (tagPool.Count <= tagIndex)
                    {
                        var t = new TagView { Root = HudUi.Rect("Tag", tags) };
                        var scrim = HudUi.Image("Scrim", t.Root, hud.Sprites.Rounded(), HudSprites.C(HudTheme.Scrim), true); HudUi.Stretch(scrim.rectTransform);
                        t.Text = HudUi.Text("Text", t.Root, hud.Font, HudTheme.MarkPx + 2, HudSprites.C(HudTheme.Ink), TextAnchor.MiddleCenter); HudUi.Stretch(t.Text.rectTransform);
                        tagPool.Add(t);
                    }
                    var view = tagPool[tagIndex++]; view.Root.gameObject.SetActive(true);
                    var width = HudUi.Width(view.Text, tag.Text) + 20;
                    view.Root.anchorMin = view.Root.anchorMax = at; view.Root.pivot = new Vector2(0.5f, 1); view.Root.anchoredPosition = new Vector2(0, -5); view.Root.sizeDelta = new Vector2(width, 26);
                }
            for (var i = tagIndex; i < tagPool.Count; i++) tagPool[i].Root.gameObject.SetActive(false);
        }

        /// <summary>A new shot: see <see cref="AlarmPinBoard.Settle"/>.</summary>
        public void Settle() => Board.Settle();
    }
}
