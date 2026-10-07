// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

using System;
using UnityEngine;

namespace DeviceChain.Sitepulse.Visuals
{
    /// <summary>The times that make the cards calm.</summary>
    public static class CardTiming
    {
        /// <summary>A slot must be invalid this long, continuously, before its card is moved.</summary>
        public const float HoldSeconds = 0.5f;

        /// <summary>A card that changes slot glides over about this long.</summary>
        public const float GlideSeconds = 0.3f;

        /// <summary>The fastest a card may glide, in layout units per second.</summary>
        public const float GlideMaxSpeed = 2400f;

        public const float FadeSeconds = 0.25f;

        /// <summary>Reduced motion: no glide; a short cross-fade instead.</summary>
        public const float ReducedFadeSeconds = 0.12f;

        /// <summary>A full re-layout of cards that need a new slot is evaluated at most this often.</summary>
        public const float SolveIntervalSeconds = 0.5f;
    }

    /// <summary>Lets a re-layout through at most once per interval.</summary>
    public sealed class SolveGate
    {
        readonly float interval;
        float since;

        public SolveGate(float interval = CardTiming.SolveIntervalSeconds)
        {
            this.interval = interval;
            since = interval;
        }

        public void Tick(float dt) => since += dt;

        /// <summary>True, and the wait begins again, when the interval has passed.</summary>
        public bool TryTake()
        {
            if (since < interval) return false;
            since = 0f;
            return true;
        }
    }

    /// <summary>
    /// One card's calm: its slot (where it sits, as offsets from the point it labels, so it travels with the
    /// machine), how long that slot has been invalid, the glide to a new slot, and its fade. The pin of the
    /// leader is not here: it follows the machine every frame; only the card's place is debounced.
    /// </summary>
    public sealed class CardSlot
    {
        Vector2 cardVelocity, elbowVelocity;
        bool visibleOnce;

        public bool HasSlot { get; private set; }

        /// <summary>Where the card has been given to sit: its lower-left corner, from the anchor.</summary>
        public Vector2 Offset { get; private set; }

        /// <summary>Where its leader bends to, from the anchor.</summary>
        public Vector2 ElbowOffset { get; private set; }

        /// <summary>Where the card is drawn now (gliding towards <see cref="Offset"/>), from the anchor.</summary>
        public Vector2 Display { get; private set; }

        public Vector2 DisplayElbow { get; private set; }

        public float InvalidFor { get; private set; }
        public float Alpha { get; private set; }

        /// <summary>Faded out and wanted by no one: it can be hidden and forgotten.</summary>
        public bool Gone(bool wanted) => !wanted && Alpha <= 0f;

        /// <summary>
        /// Says whether the slot is valid this frame. True when the card needs a new slot: it has none, or the one
        /// it has has been invalid for longer than <see cref="CardTiming.HoldSeconds"/> without a break.
        /// </summary>
        public bool Observe(bool valid, float dt)
        {
            if (!HasSlot) return true;
            if (valid) InvalidFor = 0f;
            else InvalidFor += dt;
            return InvalidFor > CardTiming.HoldSeconds;
        }

        /// <summary>Gives the card a slot. From nothing, or from invisible, it appears there; otherwise it glides (reduced motion: it fades out and in at the new place).</summary>
        public void Assign(Vector2 offset, Vector2 elbowOffset, bool reduced)
        {
            bool moving = HasSlot && Alpha > 0f;
            HasSlot = true;
            InvalidFor = 0f;
            Offset = offset;
            ElbowOffset = elbowOffset;
            if (!moving || reduced)
            {
                Display = offset;
                DisplayElbow = elbowOffset;
                cardVelocity = elbowVelocity = Vector2.zero;
                if (moving) Alpha = 0f;
            }
        }

        public void Release()
        {
            HasSlot = false;
            InvalidFor = 0f;
            Alpha = 0f;
            cardVelocity = elbowVelocity = Vector2.zero;
        }

        /// <summary>Advances the glide and the fade by <paramref name="dt"/>. <paramref name="snap"/>: no animation at all (a still from the editor).</summary>
        public void Step(float dt, bool wanted, bool reduced, bool snap)
        {
            if (snap)
            {
                Alpha = wanted ? 1f : 0f;
                Display = Offset;
                DisplayElbow = ElbowOffset;
                return;
            }

            if (!reduced && HasSlot)
            {
                Display = Vector2.SmoothDamp(Display, Offset, ref cardVelocity, CardTiming.GlideSeconds, CardTiming.GlideMaxSpeed, dt);
                DisplayElbow = Vector2.SmoothDamp(DisplayElbow, ElbowOffset, ref elbowVelocity, CardTiming.GlideSeconds, CardTiming.GlideMaxSpeed, dt);
            }

            float fade = reduced ? CardTiming.ReducedFadeSeconds : CardTiming.FadeSeconds;
            float step = dt / fade;
            Alpha = wanted ? Mathf.Min(1f, Alpha + step) : Mathf.Max(0f, Alpha - step);
        }
    }

    /// <summary>
    /// The part of the frame a card may be drawn in: the frame less a margin of about two percent (and, in a portrait frame, the
    /// bands a platform's own controls cover). A card is never drawn outside it, whatever the slot it holds says.
    /// </summary>
    public static class CardSafeArea
    {
        /// <summary>The margin, as a fraction of the frame's height (the layout is 1080 units high, so about 22 units).</summary>
        public const float MarginFraction = 0.02f;

        /// <summary>The area for a frame <paramref name="refW"/> by <paramref name="refH"/> layout units, with bands of <paramref name="insetTop"/> and <paramref name="insetBottom"/> (fractions of the height) held clear.</summary>
        public static Rect Of(float refW, float refH, float insetTop = 0f, float insetBottom = 0f)
        {
            float m = refH * MarginFraction;
            float yMin = Mathf.Max(m, refH * insetBottom);
            float yMax = Mathf.Min(refH - m, refH * (1f - insetTop));
            return Rect.MinMaxRect(m, yMin, Mathf.Max(m, refW - m), Mathf.Max(yMin, yMax));
        }

        /// <summary>Whether <paramref name="r"/> is inside <paramref name="area"/>, give or take float rounding.</summary>
        public static bool Inside(Rect r, Rect area)
        {
            const float eps = 0.001f;
            return r.xMin >= area.xMin - eps && r.yMin >= area.yMin - eps && r.xMax <= area.xMax + eps && r.yMax <= area.yMax + eps;
        }

        /// <summary>How far <paramref name="r"/> must move to be inside <paramref name="area"/> (centred on it along an axis it is too big for). Zero when it already is.</summary>
        public static Vector2 Shift(Rect r, Rect area) => new Vector2(ShiftAxis(r.xMin, r.xMax, area.xMin, area.xMax), ShiftAxis(r.yMin, r.yMax, area.yMin, area.yMax));

        // A clamp: it moves no faster than the rect it holds, so a card that rides the camera is clamped without a jump.
        static float ShiftAxis(float lo, float hi, float areaLo, float areaHi)
        {
            if (hi - lo >= areaHi - areaLo) return (areaLo + areaHi) / 2f - (lo + hi) / 2f;
            if (lo < areaLo) return areaLo - lo;
            if (hi > areaHi) return areaHi - hi;
            return 0f;
        }

        /// <summary>The point of <paramref name="r"/> nearest to <paramref name="p"/>.</summary>
        public static Vector2 Nearest(Rect r, Vector2 p) => new Vector2(Mathf.Clamp(p.x, r.xMin, r.xMax), Mathf.Clamp(p.y, r.yMin, r.yMax));

        /// <summary>The area of the overlap of two rectangles.</summary>
        public static float OverlapArea(Rect a, Rect b)
        {
            float w = Mathf.Min(a.xMax, b.xMax) - Mathf.Max(a.xMin, b.xMin);
            float h = Mathf.Min(a.yMax, b.yMax) - Mathf.Max(a.yMin, b.yMin);
            return w > 0f && h > 0f ? w * h : 0f;
        }
    }

    /// <summary>
    /// Finds the place for a card round its anchor. The best valid slot when there is one; when there is none (a narrow frame, a
    /// crowded one) the least bad: the card pulled inside the safe area where it covers the least. A card that is to be shown always
    /// gets a slot.
    /// </summary>
    public static class CardSlotSearch
    {
        static readonly float[] Angles = { 90f, 65f, 115f, 40f, 140f, 15f, 165f, -15f, -165f, -50f, -130f };
        static readonly float[] Lengths = { 55f, 95f, 140f, 190f, 250f, 320f, 400f };

        /// <summary>The cost of the least-bad slot: what a leader that crosses something adds.</summary>
        public const float LeaderFault = 6000f;

        /// <summary>
        /// <paramref name="valid"/>: whether a slot is fully usable; <paramref name="cover"/>: what it costs (extra) to sit over the cut;
        /// <paramref name="badness"/>: how bad an unusable slot is (overlap area and leader faults), for the least-bad pass.
        /// </summary>
        public static void Find(Vector2 anchor, Vector2 size, Rect safe, Func<Rect, Vector2, bool> valid, Func<Rect, float> cover,
            Func<Rect, Vector2, float> badness, out Rect rect, out Vector2 elbow, out bool exact, out float cost)
        {
            float best = float.PositiveInfinity;
            rect = default;
            elbow = default;
            foreach (float ang in Angles)
            {
                var dir = new Vector2(Mathf.Cos(ang * Mathf.Deg2Rad), Mathf.Sin(ang * Mathf.Deg2Rad));
                foreach (float len in Lengths)
                {
                    var e = anchor + dir * len;
                    var r = At(e, dir, size);
                    float c = len + Mathf.Abs(ang - 90f) * 0.6f;
                    if (c >= best) continue;
                    if (!CardSafeArea.Inside(r, safe) || !valid(r, e)) continue;
                    c += cover(r) * 900f;
                    if (c < best)
                    {
                        best = c;
                        rect = r;
                        elbow = e;
                    }
                }
            }

            exact = !float.IsPositiveInfinity(best);
            cost = best;
            if (exact) return;

            best = float.PositiveInfinity;
            foreach (float ang in Angles)
            {
                var dir = new Vector2(Mathf.Cos(ang * Mathf.Deg2Rad), Mathf.Sin(ang * Mathf.Deg2Rad));
                foreach (float len in Lengths)
                {
                    var r = At(anchor + dir * len, dir, size);
                    r.position += CardSafeArea.Shift(r, safe);
                    var e = CardSafeArea.Nearest(r, anchor);
                    float c = badness(r, e) + len * 0.5f + Mathf.Abs(ang - 90f) * 0.3f;
                    if (c < best)
                    {
                        best = c;
                        rect = r;
                        elbow = e;
                    }
                }
            }

            cost = best;
        }

        /// <summary>The card whose leader bends at <paramref name="e"/>, meeting it at the corner or side nearest the anchor.</summary>
        static Rect At(Vector2 e, Vector2 dir, Vector2 size)
        {
            float x = Mathf.Abs(dir.x) < 0.2f ? e.x - size.x * 0.18f : dir.x > 0f ? e.x : e.x - size.x;
            float y = dir.y > 0.35f ? e.y : dir.y < -0.35f ? e.y - size.y : e.y - size.y / 2f;
            return new Rect(x, y, size.x, size.y);
        }
    }

    /// <summary>Which machine a pointer click lands on: the nearest of the machines' boxes the ray enters.</summary>
    public static class CardPicking
    {
        /// <summary>How far past the machine's own box a click still counts, in metres.</summary>
        public const float Slop = 0.6f;

        public static string Pick(Ray ray, System.Collections.Generic.IReadOnlyList<(string id, Bounds bounds)> targets, out float distance)
        {
            string best = null;
            distance = float.PositiveInfinity;
            foreach (var (id, b) in targets)
            {
                if (b.size == Vector3.zero) continue;
                var padded = new Bounds(b.center, b.size + Vector3.one * (2f * Slop));
                if (!padded.IntersectRay(ray, out float d)) continue;
                // a ray that starts inside the box hits it at 0
                if (d < distance)
                {
                    distance = d;
                    best = id;
                }
            }

            return best;
        }
    }
}

namespace DeviceChain.Sitepulse.Visuals
{
    /// <summary>
    /// The geofence label's calm, the same rules as a card's: it keeps the fence point it labels while that point's
    /// place on screen is valid, moves only after the place has been invalid for <see cref="CardTiming.HoldSeconds"/>
    /// (a truck driving past does not move it), picks a new point at most once per
    /// <see cref="CardTiming.SolveIntervalSeconds"/>, glides to it, and fades in and out instead of popping.
    /// </summary>
    public sealed class LabelPlacer
    {
        readonly SolveGate gate = new SolveGate();
        Vector2 shown, glide, velocity;
        float invalidFor, alpha;
        bool placedOnce;

        /// <summary>The fence point being labelled, or -1.</summary>
        public int Index { get; private set; } = -1;

        /// <summary>Where the label is drawn this frame.</summary>
        public Vector2 Position => shown;

        public float Alpha => alpha;

        /// <summary>
        /// One frame. <paramref name="candidates"/> is the number of fence points in preference order;
        /// <paramref name="placeOf"/> gives a point's label position and whether that place is valid now.
        /// </summary>
        public void Step(int candidates, System.Func<int, (Vector2 at, bool valid)> placeOf, float dt, bool reduced)
        {
            gate.Tick(dt);
            bool keep = false;
            Vector2 target = shown;
            if (Index >= 0 && Index < candidates)
            {
                var (at, valid) = placeOf(Index);
                invalidFor = valid ? 0f : invalidFor + dt;
                keep = invalidFor <= CardTiming.HoldSeconds;
                target = at;
            }

            if (!keep && gate.TryTake())
            {
                Index = -1;
                for (int i = 0; i < candidates; i++)
                {
                    var (at, valid) = placeOf(i);
                    if (!valid) continue;
                    // a new point: start from where the label is drawn and glide the difference away
                    glide = placedOnce && !reduced ? shown - at : Vector2.zero;
                    velocity = Vector2.zero;
                    placedOnce = true;
                    Index = i;
                    invalidFor = 0f;
                    target = at;
                    keep = true;
                    break;
                }
            }

            bool on = keep && Index >= 0;
            if (on)
            {
                // the label rides its fence point exactly (the point moves with the camera); only the jump
                // between two points is eased out, as an offset decaying to zero
                glide = reduced ? Vector2.zero : Vector2.SmoothDamp(glide, Vector2.zero, ref velocity, CardTiming.GlideSeconds / 3f, CardTiming.GlideMaxSpeed, dt);
                shown = target + glide;
            }

            float fade = reduced ? CardTiming.ReducedFadeSeconds : CardTiming.FadeSeconds;
            alpha = Mathf.MoveTowards(alpha, on ? 1f : 0f, dt / fade);
        }
    }
}
