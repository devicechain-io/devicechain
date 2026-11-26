// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

using System;
using System.Collections.Generic;
using UnityEngine;

namespace DeviceChain.Demos.Hud
{
    /// <summary>
    /// Every shape the HUD draws, made in code when first asked for: the package ships no image files. Shapes are drawn from signed
    /// distances with a one-pixel feather, so they stay clean at any size the canvas scales them to. One owner (a <see cref="StoryHud"/>)
    /// releases them all.
    /// </summary>
    public sealed class HudSprites : IDisposable
    {
        readonly List<UnityEngine.Object> owned = new List<UnityEngine.Object>();
        readonly Dictionary<string, Sprite> cache = new Dictionary<string, Sprite>();

        public static Color C(HudColor c) => new Color(c.R, c.G, c.B, c.A);
        public static Color C(HudColor c, float alpha) => new Color(c.R, c.G, c.B, c.A * alpha);

        Sprite Make(string key, int width, int height, Func<float, float, Color> pixel, Vector4 border = default, float pixelsPerUnit = 100, bool mips = false)
        {
            if (cache.TryGetValue(key, out var made)) return made;
            var texture = new Texture2D(width, height, TextureFormat.RGBA32, mips) { name = "HUD " + key, wrapMode = TextureWrapMode.Clamp, filterMode = mips ? FilterMode.Trilinear : FilterMode.Bilinear, hideFlags = HideFlags.DontSave };
            var pixels = new Color[width * height];
            for (var y = 0; y < height; y++) for (var x = 0; x < width; x++) pixels[y * width + x] = pixel(x + 0.5f, y + 0.5f);
            texture.SetPixels(pixels); texture.Apply(mips, true);
            var sprite = Sprite.Create(texture, new Rect(0, 0, width, height), new Vector2(0.5f, 0.5f), pixelsPerUnit, 0, SpriteMeshType.FullRect, border);
            sprite.name = texture.name; sprite.hideFlags = HideFlags.DontSave;
            owned.Add(texture); owned.Add(sprite); cache.Add(key, sprite);
            return sprite;
        }

        /// <summary>Coverage of a shape at a signed distance (negative inside), feathered over one pixel.</summary>
        static float Cover(float distance) => Mathf.Clamp01(0.5f - distance);
        static Color White(float alpha) => new Color(1, 1, 1, alpha);
        static float Box(Vector2 p, Vector2 centre, Vector2 half, float radius)
        {
            var q = new Vector2(Mathf.Abs(p.x - centre.x) - half.x + radius, Mathf.Abs(p.y - centre.y) - half.y + radius);
            return Mathf.Min(Mathf.Max(q.x, q.y), 0) + new Vector2(Mathf.Max(q.x, 0), Mathf.Max(q.y, 0)).magnitude - radius;
        }
        static float Segment(Vector2 p, Vector2 a, Vector2 b)
        {
            var ab = b - a; var t = Mathf.Clamp01(Vector2.Dot(p - a, ab) / ab.sqrMagnitude);
            return (p - (a + ab * t)).magnitude;
        }
        static float Triangle(Vector2 p, Vector2 a, Vector2 b, Vector2 c)
        {
            float Cross(Vector2 u, Vector2 v) => u.x * v.y - u.y * v.x;
            var inside = Cross(b - a, p - a) >= 0 && Cross(c - b, p - b) >= 0 && Cross(a - c, p - c) >= 0
                || Cross(b - a, p - a) <= 0 && Cross(c - b, p - b) <= 0 && Cross(a - c, p - c) <= 0;
            var d = Mathf.Min(Segment(p, a, b), Mathf.Min(Segment(p, b, c), Segment(p, c, a)));
            return inside ? -d : d;
        }

        /// <summary>A white rounded rectangle to tint and slice: the scrim of every box, at the theme's corner radius.</summary>
        public Sprite Rounded()
        {
            const int scale = 2; var r = HudTheme.CornerRadiusPx * scale; var size = (int)(2 * r + 4);
            return Make("rounded", size, size, (x, y) => White(Cover(Box(new Vector2(x, y), new Vector2(size / 2f, size / 2f), new Vector2(size / 2f, size / 2f), r))),
                Vector4.one * (r + 1), 100 * scale);
        }

        /// <summary>A white pill outline to tint and slice: the frame of a small mark.</summary>
        public Sprite PillOutline()
        {
            const int size = 40; const float r = 19, line = 2;
            return Make("pill", size, size, (x, y) =>
            {
                var d = Box(new Vector2(x, y), new Vector2(20, 20), new Vector2(20, 20), r);
                return White(Cover(Mathf.Abs(d + line / 2 + 0.5f) - line / 2));
            }, Vector4.one * 19, 200);
        }

        public Sprite Dot() => Make("dot", 48, 48, (x, y) => White(Cover((new Vector2(x, y) - new Vector2(24, 24)).magnitude - 21)));

        /// <summary>A dot hatched with diagonal lines: a number its sender counted, which the platform did not.</summary>
        public Sprite HatchedDot() => Make("hatched", 48, 48, (x, y) =>
        {
            var d = (new Vector2(x, y) - new Vector2(24, 24)).magnitude - 21;
            var rim = Cover(Mathf.Abs(d + 2.5f) - 2.5f);
            var hatch = Mathf.Abs(Mathf.Repeat(x + y, 12) - 6) < 2.2f ? 1 : 0;
            return White(Mathf.Max(rim, Cover(d) * hatch));
        });

        /// <summary>An outlined diamond: what started a chain.</summary>
        public Sprite Diamond() => Make("diamond", 48, 48, (x, y) =>
        {
            var d = Mathf.Abs(x - 24) + Mathf.Abs(y - 24) - 20;
            return White(Cover(Mathf.Abs(d * 0.7071f + 2) - 2));
        });

        public Sprite Ring() => Make("ring", 96, 96, (x, y) => White(Cover(Mathf.Abs((new Vector2(x, y) - new Vector2(48, 48)).magnitude - 42) - 2.5f)));

        public Sprite Glow() => Make("glow", 64, 64, (x, y) =>
        {
            var r = Mathf.Clamp01((new Vector2(x, y) - new Vector2(32, 32)).magnitude / 32);
            return White((1 - r) * (1 - r));
        });

        /// <summary>
        /// The teardrop pin of one severity, 96 x 128, its tip at the bottom centre. A dark outline, the style's edge, its fill, and the
        /// "!" glyph. The shape is a circle and the cone from it down to the tip.
        /// </summary>
        public Sprite Pin(PinSeverity severity)
        {
            var style = AlarmPins.Style(severity); const float s = 4; // 24 x 32 design units
            Vector2 centre = new Vector2(12, 20.5f) * s, tip = new Vector2(12, 1) * s; var radius = 9.5f * s;
            // the two points where the cone from the tip touches the circle
            var axis = (tip - centre).normalized; var reach = (tip - centre).magnitude; var along = radius * radius / reach;
            var across = Mathf.Sqrt(radius * radius - along * along); var normal = new Vector2(-axis.y, axis.x);
            Vector2 left = centre + axis * along + normal * across, right = centre + axis * along - normal * across;
            var outline = AlarmPins.OutlinePx / AlarmPins.NominalPx * 32 * s; var edge = (style.Outlined ? 2.4f : 1.5f) * s;
            Color dark = C(HudTheme.Edge), line = C(style.Edge), fill = C(style.Fill), glyph = C(style.Glyph);
            return Make("pin-" + severity, 96, 128, (x, y) =>
            {
                var p = new Vector2(x, y);
                var d = Mathf.Min((p - centre).magnitude - radius, Triangle(p, tip, left, right)) - outline;
                var colour = Color.Lerp(dark, line, Cover(d + outline));
                colour = Color.Lerp(colour, fill, Cover(d + outline + edge));
                var mark = Mathf.Min(Box(p, new Vector2(12, 22.2f) * s, new Vector2(1.3f, 4.3f) * s, 1.3f * s), (p - new Vector2(12, 14.4f) * s).magnitude - 1.6f * s);
                colour = Color.Lerp(colour, glyph, Cover(mark));
                colour.a = Cover(d);
                return colour;
            }, default, 100, true);
        }

        /// <summary>The green check of a cleared alarm, with the same dark outline as a pin.</summary>
        public Sprite Tick()
        {
            Vector2 a = new Vector2(20, 50), b = new Vector2(40, 28), c = new Vector2(78, 72);
            Color dark = C(HudTheme.Edge), green = C(HudTheme.Green);
            return Make("tick", 96, 96, (x, y) =>
            {
                var p = new Vector2(x, y); var d = Mathf.Min(Segment(p, a, b), Segment(p, b, c));
                var colour = Color.Lerp(dark, green, Cover(d - 7)); colour.a = Cover(d - 11);
                return colour;
            }, default, 100, true);
        }

        public void Dispose()
        {
            foreach (var o in owned)
            {
                if (o == null) continue;
                if (Application.isPlaying) UnityEngine.Object.Destroy(o); else UnityEngine.Object.DestroyImmediate(o);
            }
            owned.Clear(); cache.Clear();
        }
    }
}
