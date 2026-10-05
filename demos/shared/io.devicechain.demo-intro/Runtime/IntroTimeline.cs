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
        /// <summary>The frame's rotation (its tilt and its spin); identity at the lock.</summary>
        public Quaternion frameRotation;
        /// <summary>The cube's rotation as the viewer sees it (it shares the frame's tilt but not
        /// its spin); identity at the lock.</summary>
        public Quaternion cubeRotation;
        /// <summary>The cube's scale: a little small while it spins so it clears the frame.</summary>
        public float cubeScale;
        /// <summary>The moving mark's chamfer, 0..1: 0 is the drawing's sharp edges.</summary>
        public float chamfer;
        /// <summary>How much of the frame's edge the rim light has traced, 0..1.</summary>
        public float trace;
        /// <summary>Brightness of the rim light on the frame's edges.</summary>
        public float glow;
        /// <summary>Brightness of the light on the cube's edges.</summary>
        public float cubeGlow;
        /// <summary>How much light falls on the mark: 0 black, 1 fully lit.</summary>
        public float light;
        /// <summary>The light sweep's position across the mark, in units; far outside = none.</summary>
        public float sweep;
        /// <summary>0 = lit shading, 1 = the exact brand colours.</summary>
        public float flat;
        /// <summary>The lock pulse: opacity, scale (1 = the frame's outline) and line width in
        /// pixels at 1080 lines.</summary>
        public float pulseAlpha, pulseScale, pulseWidth;
        /// <summary>Whether the data packets converging on the frame are shown (each packet
        /// fades itself in and out).</summary>
        public float packets;
        /// <summary>Opacity of the wordmark, and how much of its rise is still to come (1 = all).</summary>
        public float wordmark, wordmarkRise;
        /// <summary>The background's soft glow.</summary>
        public float backdrop;
        /// <summary>The bloom's strength; 0, so the lock's colours are untouched, from the lock on.</summary>
        public float bloom;
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
    /// <item>a thin rim light traces the frame's edges as it turns and untilts into view, and data
    /// packets travel in toward the hexagon's corners;</item>
    /// <item>the cube turns inside the frame against its spin; a light sweeps across the faces
    /// and catches their chamfered edges; bright edges bloom;</item>
    /// <item>the cube lands in the isometric pose at <see cref="Lock"/>, carrying a few degrees past
    /// it and settling like a spring; the shading eases into the exact brand colours as it arrives,
    /// and a thin pulse marks the lock;</item>
    /// <item>the wordmark rises in below, the lockup holds, the wordmark fades, and the frame's
    /// inner hexagon opens out onto the demo's first shot.</item>
    /// </list>
    /// </summary>
    public static class IntroTimeline
    {
        public const float TraceStart = 0f, TraceEnd = 0.9f;
        public const float SpinStart = 0.1f;
        /// <summary>When the frame has finished turning and untilting.</summary>
        public const float FrameSettled = 1.65f;
        public const float SweepStart = 0.75f, SweepEnd = 1.45f;
        public const float Lock = 1.75f;
        /// <summary>How long before the lock the shading starts to ease into the brand colours.</summary>
        public const float FlattenLead = 0.35f;
        /// <summary>When the glow and the bloom start to fade, so they are gone at the lock.</summary>
        public const float GlowFadeStart = 1.45f;
        /// <summary>When the cube's landing has settled.</summary>
        public const float Settled = Lock + 0.25f;
        public const float PulseAttack = 0.05f, PulseDecay = 0.45f;
        public const float WordmarkIn = 1.9f, WordmarkFull = 2.3f;
        /// <summary>The hold ends and the exit begins: the wordmark fades out first.</summary>
        public const float DissolveStart = WordmarkFull + 0.8f;
        public const float WordmarkGone = DissolveStart + 0.2f;
        /// <summary>The frame's inner hexagon opens out onto the demo.</summary>
        public const float OpenStart = DissolveStart + 0.15f;
        public const float End = OpenStart + 0.35f;

        /// <summary>Reduced motion: the lockup shown still, then a quick fade.</summary>
        public const float ReducedHold = 1.2f, ReducedEnd = ReducedHold + 0.3f;
        /// <summary>A skip fades out over this long from wherever the intro is.</summary>
        public const float SkipFade = 0.25f;

        /// <summary>The cube's turn before it lands (degrees), and its speed as it arrives
        /// (degrees a second).</summary>
        public const float CubeTurn = 450f, CubeArrival = 190f;
        public static readonly Vector3 CubeAxis = new Vector3(0.45f, 0.75f, -0.5f).normalized;
        /// <summary>The landing spring: damping ratio and damped frequency (Hz).</summary>
        public const float SpringDamping = 0.55f, SpringHz = 3f;
        /// <summary>The frame's spin about its own axis, against the cube's turn, and its tilt about
        /// X (degrees), both eased out to nothing by <see cref="FrameSettled"/>.</summary>
        public const float FrameSpin = 120f, FrameTilt = 12f;
        public const float SpinScale = 0.95f;
        /// <summary>How far the light sweep travels either side of the centre, in units.</summary>
        public const float SweepReach = 0.9f;
        /// <summary>How far the opening grows: by then it is past every corner of the screen.</summary>
        public const float HoleGrowth = 25f;
        public const float Bloom = 0.55f;

        public static IntroPose Evaluate(float t)
        {
            var p = new IntroPose();

            // the frame turns against the cube and untilts, easing out (quintic) into the pose;
            // the cube shares the tilt, so it stays upright in the frame's space
            float f = 1f - EaseOutQuint(Clamp01((t - SpinStart) / (FrameSettled - SpinStart)));
            var tilt = Quaternion.AngleAxis(FrameTilt * f, Vector3.right);
            p.frameRotation = tilt * Quaternion.AngleAxis(FrameSpin * f, Vector3.forward);
            p.cubeRotation = tilt * Quaternion.AngleAxis(CubeAngle(t), CubeAxis);
            p.cubeScale = Mathf.Lerp(SpinScale, 1f, Smooth(Clamp01((t - (Lock - 0.3f)) / 0.3f)));

            p.trace = Smooth(Clamp01((t - TraceStart) / (TraceEnd - TraceStart)));
            float fadeOut = 1f - Smooth(Clamp01((t - GlowFadeStart) / (Lock - GlowFadeStart)));
            p.glow = Clamp01((t - TraceStart) / 0.1f) * Mathf.Lerp(1f, 0.5f, Smooth(Clamp01((t - TraceEnd) / 0.4f))) * fadeOut;
            p.cubeGlow = Smooth(Clamp01((t - 0.45f) / 0.4f)) * fadeOut;
            p.bloom = Bloom * fadeOut;
            p.light = Smooth(Clamp01((t - 0.05f) / 0.8f));
            p.sweep = t < SweepStart || t > SweepEnd ? 100f
                : Mathf.Lerp(-SweepReach, SweepReach, Smooth((t - SweepStart) / (SweepEnd - SweepStart)));
            p.flat = EaseInOutSine(Clamp01((t - (Lock - FlattenLead)) / FlattenLead));
            p.chamfer = 1f - p.flat;

            // the pulse: a three-frame attack at the lock, then it widens, thins and fades
            float d = Clamp01((t - Lock - PulseAttack) / PulseDecay);
            float decay = EaseOutQuad(d);
            p.pulseAlpha = t < Lock || d >= 1f ? 0f : 0.6f * Mathf.Min(Clamp01((t - Lock) / PulseAttack), 1f - decay);
            p.pulseScale = 1f + 0.35f * decay;
            p.pulseWidth = Mathf.Lerp(3f, 1f, decay);

            p.packets = t < Lock ? 1f : 0f;
            float w = EaseOutCubic(Clamp01((t - WordmarkIn) / (WordmarkFull - WordmarkIn)));
            float gone = Clamp01((t - DissolveStart) / (WordmarkGone - DissolveStart));
            p.wordmark = w * (1f - gone * gone * gone);
            p.wordmarkRise = 1f - w;
            p.backdrop = Mathf.Lerp(0.3f, 1f, Smooth(Clamp01(t / 0.9f)));

            // the exit: the inner hexagon opens (quartic ease in) and the cube fades as it grows
            float o = Clamp01((t - OpenStart) / (End - OpenStart));
            p.hole = t < OpenStart ? 0f : Smooth(Clamp01((t - OpenStart) / 0.08f));
            p.holeScale = 1f + (HoleGrowth - 1f) * o * o * o * o;
            p.cubeAlpha = 1f - Smooth(Clamp01(o / 0.55f));
            p.cover = 1f;
            return p;
        }

        /// <summary>The still lockup reduced motion shows: the locked mark and the wordmark,
        /// then a short fade. No pulse, no packets, no opening.</summary>
        public static IntroPose EvaluateReduced(float t)
        {
            var p = Evaluate(DissolveStart);
            p.cover = 1f - Smooth(Clamp01((t - ReducedHold) / (ReducedEnd - ReducedHold)));
            return p;
        }

        /// <summary>The cube's remaining turn about <see cref="CubeAxis"/>, in degrees: a cubic
        /// ease out that still arrives at <see cref="CubeArrival"/>, then a damped spring that
        /// carries it a few degrees past the pose and has settled by <see cref="Settled"/>.</summary>
        public static float CubeAngle(float t)
        {
            float duration = Lock - SpinStart;
            if (t < Lock)
            {
                float k = 1f - Clamp01((t - SpinStart) / duration);
                float w = CubeArrival * duration / CubeTurn;     // the end speed over the mean speed
                return CubeTurn * ((1f - w) * k * k * k + w * k);
            }
            float tau = t - Lock, settle = Settled - Lock;
            if (tau >= settle) return 0f;
            float wd = 2f * Mathf.PI * SpringHz;
            float decay = SpringDamping / Mathf.Sqrt(1f - SpringDamping * SpringDamping) * wd;
            // the last of the ring-down is eased away, so the pose is exact from Settled on
            float window = 1f - Smooth(Clamp01((tau - 0.15f) / (settle - 0.15f)));
            return -CubeArrival / wd * Mathf.Exp(-decay * tau) * Mathf.Sin(wd * tau) * window;
        }

        static float Smooth(float x) => x * x * (3f - 2f * x);
        static float Clamp01(float x) => x < 0f ? 0f : x > 1f ? 1f : x;
        static float EaseOutCubic(float x) { float k = 1f - x; return 1f - k * k * k; }
        static float EaseOutQuint(float x) { float k = 1f - x; return 1f - k * k * k * k * k; }
        static float EaseOutQuad(float x) => 1f - (1f - x) * (1f - x);
        static float EaseInOutSine(float x) => (1f - Mathf.Cos(Mathf.PI * x)) / 2f;
    }
}
