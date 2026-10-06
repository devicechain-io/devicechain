// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

using UnityEngine;

namespace DeviceChain.Demos
{
    /// <summary>
    /// Everything the intro shows at time <c>t</c>, as plain numbers. <see cref="IntroTimeline"/>
    /// computes it; <see cref="DemoIntro"/> applies it. Keeping the pose a pure function of time is
    /// what makes a frame sequence reproducible and the beats testable.
    /// </summary>
    public struct IntroPose
    {
        /// <summary>The frame's rotation; identity at the lock.</summary>
        public Quaternion frameRotation;
        /// <summary>The cube's rotation relative to the frame; identity at the lock.</summary>
        public Quaternion cubeRotation;
        /// <summary>The cube's scale: a little small while it spins so it clears the frame.</summary>
        public float cubeScale;
        /// <summary>How much of the frame's edge the rim light has traced, 0..1.</summary>
        public float trace;
        /// <summary>Brightness of the rim light and the glow on the edges.</summary>
        public float glow;
        /// <summary>The cube's edge glow (a soft glow on its edges).</summary>
        public float cubeGlow;
        /// <summary>How much light falls on the mark: 0 black, 1 fully lit.</summary>
        public float light;
        /// <summary>The light sweep's position across the mark, in units; far outside = none.</summary>
        public float sweep;
        /// <summary>0 = lit shading, 1 = the exact brand colours.</summary>
        public float flat;
        /// <summary>The lock pulse: 0 before it, then 0..1 as it expands and fades.</summary>
        public float pulse;
        /// <summary>Opacity of the wordmark, and how much of its rise is still to come (1 = all).</summary>
        public float wordmark, wordmarkRise;
        /// <summary>The background's soft glow.</summary>
        public float backdrop;
        /// <summary>The exit: how far the frame's inner hexagon has opened onto the demo (0 shut,
        /// 1 open) and its scale (1 = the drawn inner hexagon; the frame and cube grow with it).</summary>
        public float hole, holeScale;
        /// <summary>The cube's opacity: it fades as the opening grows through it.</summary>
        public float cubeAlpha;
        /// <summary>How much the intro covers the screen: 1 covers it, 0 shows the demo. Only a
        /// skip and reduced motion fade it; the full intro exits through the opening.</summary>
        public float cover;
    }

    /// <summary>
    /// The intro's choreography, about 3.6 seconds on a dark background:
    /// <list type="number">
    /// <item>from black, a thin rim light traces the frame's edges as it rotates into view;</item>
    /// <item>the cube turns inside the frame with a slower, offset spin; a light sweeps across the
    /// faces; the edges glow softly;</item>
    /// <item>everything decelerates and locks into the isometric pose at <see cref="Lock"/>, where the
    /// shading flattens to the exact brand colours and a pulse marks the lock;</item>
    /// <item>the wordmark fades in below and the lockup holds; then the wordmark fades out, and
    /// once it has gone the frame's inner hexagon opens out onto the demo's first shot.</item>
    /// </list>
    /// </summary>
    public static class IntroTimeline
    {
        public const float TraceStart = 0.1f, TraceEnd = 1.15f;
        public const float SweepStart = 1.05f, SweepEnd = 1.85f;
        public const float Lock = 2.0f;
        /// <summary>How long before the lock the shading starts to flatten.</summary>
        public const float FlattenLead = 0.12f;
        public const float PulseLength = 0.6f;
        public const float WordmarkIn = Lock + 0.15f, WordmarkFull = Lock + 0.55f;
        /// <summary>The hold ends and the exit begins: the wordmark fades out first.</summary>
        public const float DissolveStart = WordmarkFull + 0.5f;
        public const float WordmarkGone = DissolveStart + 0.2f;
        /// <summary>Once the wordmark has gone, the frame's inner hexagon opens out onto the demo.</summary>
        public const float OpenStart = WordmarkGone;
        public const float End = OpenStart + 0.35f;

        /// <summary>Reduced motion: the lockup shown still, then a quick fade.</summary>
        public const float ReducedHold = 1.2f, ReducedEnd = ReducedHold + 0.3f;
        /// <summary>A skip fades out over this long from wherever the intro is.</summary>
        public const float SkipFade = 0.25f;

        /// <summary>Angles the frame and the cube turn through before they lock, in degrees.</summary>
        public const float FrameTurn = 118f, CubeTurn = 300f;
        public static readonly Vector3 FrameAxis = new Vector3(0.22f, 1f, 0.12f).normalized;
        public static readonly Vector3 CubeAxis = new Vector3(0.45f, 0.75f, -0.5f).normalized;
        public const float SpinScale = 0.95f;
        /// <summary>How far the light sweep travels either side of the centre, in units.</summary>
        public const float SweepReach = 0.9f;
        /// <summary>How far the opening grows: by then it is past every corner of the screen.</summary>
        public const float HoleGrowth = 25f;

        public static IntroPose Evaluate(float t)
        {
            var p = new IntroPose();
            // the turn decelerates into the lock but still has a little speed when it gets
            // there, so the lock reads as a catch rather than a drift to a stop
            float u = Clamp01(t / Lock);
            float frameLeft = 1f - Settle(u);
            float cubeLeft = 1f - Settle(Clamp01((t - 0.35f) / (Lock - 0.35f)));
            p.frameRotation = Quaternion.AngleAxis(FrameTurn * frameLeft, FrameAxis);
            p.cubeRotation = Quaternion.AngleAxis(CubeTurn * cubeLeft, CubeAxis);
            p.cubeScale = Mathf.Lerp(SpinScale, 1f, Smooth(Clamp01((t - (Lock - 0.3f)) / 0.3f)));

            p.trace = Smooth(Clamp01((t - TraceStart) / (TraceEnd - TraceStart)));
            float glowIn = Clamp01((t - TraceStart) / 0.15f);
            float glowOut = 1f - Smooth(Clamp01((t - Lock) / 0.35f));
            p.glow = glowIn * Mathf.Lerp(1f, 0.45f, Smooth(Clamp01((t - TraceEnd) / 0.5f))) * glowOut;
            p.cubeGlow = Smooth(Clamp01((t - 0.7f) / 0.5f)) * glowOut * 0.6f;
            p.light = Smooth(Clamp01((t - 0.45f) / 0.95f));
            p.sweep = t < SweepStart || t > SweepEnd ? 100f
                : Mathf.Lerp(-SweepReach, SweepReach, Smooth((t - SweepStart) / (SweepEnd - SweepStart)));
            p.flat = Smooth(Clamp01((t - (Lock - FlattenLead)) / FlattenLead));
            p.pulse = t < Lock ? 0f : Clamp01((t - Lock) / PulseLength);
            float w = Smooth(Clamp01((t - WordmarkIn) / (WordmarkFull - WordmarkIn)));
            float gone = Clamp01((t - DissolveStart) / (WordmarkGone - DissolveStart));
            p.wordmark = w * (1f - gone * gone * gone);
            p.wordmarkRise = 1f - w;
            p.backdrop = Smooth(Clamp01((t - 0.2f) / 1.2f));

            // the exit: the inner hexagon opens (quartic ease in) and the cube fades as it grows;
            // the intro itself never fades, so nothing of it is left as a see-through ghost
            float o = Clamp01((t - OpenStart) / (End - OpenStart));
            p.hole = t < OpenStart ? 0f : Smooth(Clamp01((t - OpenStart) / 0.08f));
            p.holeScale = 1f + (HoleGrowth - 1f) * o * o * o * o;
            p.cubeAlpha = 1f - Smooth(Clamp01(o / 0.55f));
            p.cover = 1f;
            return p;
        }

        /// <summary>The still lockup reduced motion shows: the locked mark and the wordmark,
        /// then a short fade. No pulse and no opening.</summary>
        public static IntroPose EvaluateReduced(float t)
        {
            var p = Evaluate(DissolveStart);
            p.cover = 1f - Smooth(Clamp01((t - ReducedHold) / (ReducedEnd - ReducedHold)));
            return p;
        }

        /// <summary>Ease out with a small residual speed at the end (derivative 0.08 at u = 1).</summary>
        public static float Settle(float u)
        {
            float k = 1f - u;
            return 0.92f * (1f - k * k * k) + 0.08f * u;
        }

        static float Smooth(float x) => x * x * (3f - 2f * x);
        static float Clamp01(float x) => x < 0f ? 0f : x > 1f ? 1f : x;
    }
}
