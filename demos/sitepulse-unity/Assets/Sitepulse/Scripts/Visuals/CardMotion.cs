// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

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
