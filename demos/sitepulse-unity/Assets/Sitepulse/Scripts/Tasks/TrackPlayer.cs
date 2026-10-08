// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

using System;

namespace DeviceChain.Sitepulse.Tasks
{
    /// <summary>One frame of a choreography track as played: where the machine is and how its implements are.</summary>
    public readonly struct TrackFrame
    {
        public TrackFrame(float x, float z, float heading, float travel, float p1, float p2, float steer, bool loaded)
        {
            X = x;
            Z = z;
            Heading = heading;
            Travel = travel;
            P1 = p1;
            P2 = p2;
            Steer = steer;
            Loaded = loaded;
        }

        public float X { get; }
        public float Z { get; }

        /// <summary>Degrees clockwise from north, as the track holds it (not wrapped).</summary>
        public float Heading { get; }

        /// <summary>Signed metres along the ground: what the wheels and tracks turn by.</summary>
        public float Travel { get; }

        /// <summary>The first and second implement angles (dozer: blade arm, ripper; loader: boom, bucket; hauler: dump raise).</summary>
        public float P1 { get; }

        public float P2 { get; }
        public float Steer { get; }

        /// <summary>A hauler is carrying a load.</summary>
        public bool Loaded { get; }
    }

    /// <summary>
    /// A machine's routine track as it is played: the frames, the clock they are read at, and how fast that clock runs. One machine plays one
    /// track at <c>time - Offset</c>; a track played at less than its own pace plays less of itself, so its clock is put back by what it did
    /// not play (<see cref="Advance"/>) and the machine stands where it was and goes on from there. The scene's fleet and the EditMode bodies
    /// the task layer is tested over play their tracks through this one class, so what the tests measure is what is drawn.
    /// </summary>
    /// <remarks>Single precision throughout, as the scene's own arithmetic was: a machine is drawn exactly where it was before the class existed.</remarks>
    public sealed class TrackPlayer
    {
        /// <summary>Values per frame: x, z, heading, travel, p1, p2, steer, flag.</summary>
        public const int Stride = 8;

        readonly float[] data;
        readonly int frames;
        readonly float frameSeconds;

        public TrackPlayer(float[] frames, float frameSeconds, float periodSeconds, float offset)
        {
            data = frames ?? throw new ArgumentNullException(nameof(frames));
            this.frames = frames.Length / Stride;
            if (this.frames < 1) throw new ArgumentException("a track holds at least one frame", nameof(frames));
            if (frameSeconds <= 0f || periodSeconds <= 0f) throw new ArgumentException("a track has a frame time and a period");
            this.frameSeconds = frameSeconds;
            PeriodSeconds = periodSeconds;
            Offset = offset;
        }

        /// <summary>How long the loop is, in seconds.</summary>
        public float PeriodSeconds { get; }

        /// <summary>The scene time at which the machine is at the start of its loop (its clock is read as time - Offset).</summary>
        public float Offset { get; private set; }

        /// <summary>How fast it plays: 1 is the track's own pace, 0 holds it where it is.</summary>
        public float Rate { get; private set; } = 1f;

        public void SetRate(float rate) => Rate = Math.Max(0f, Math.Min(1f, rate));

        /// <summary>Time passes: a track played at less than its pace falls behind the scene's clock by what it did not play.</summary>
        public void Advance(float sceneSeconds)
        {
            if (Rate < 1f) Offset += sceneSeconds * (1f - Rate);
        }

        /// <summary>Taken off its track: whatever held it back is over, it plays at its own pace once it is put back.</summary>
        public void Detach() => Rate = 1f;

        /// <summary>Put back on the loop at <paramref name="trackSeconds"/> into it, at scene time <paramref name="time"/>.</summary>
        public void Attach(float time, float trackSeconds) => Offset = time - trackSeconds;

        /// <summary>How far into the loop the machine is at scene time <paramref name="time"/>.</summary>
        public float TrackSecondsAt(float time) => Repeat(time - Offset, PeriodSeconds);

        /// <summary>The frame the machine is on at scene time <paramref name="time"/> (interpolated between two).</summary>
        public TrackFrame SampleAt(float time) => FrameAt(Repeat(time - Offset, PeriodSeconds));

        /// <summary>The frame the loop holds <paramref name="trackSeconds"/> into it (interpolated between two), whether or not the machine is there now.</summary>
        public TrackFrame Sample(float trackSeconds) => FrameAt(Repeat(trackSeconds, PeriodSeconds));

        TrackFrame FrameAt(float wrapped)
        {
            var ft = wrapped / frameSeconds;
            var i = Math.Min((int)ft, frames - 1);
            var j = (i + 1) % frames;
            var w = ft - (int)ft;
            float V(int k) => Lerp(data[i * Stride + k], data[j * Stride + k], w);
            var heading = data[i * Stride + 2] + DeltaAngle(data[i * Stride + 2], data[j * Stride + 2]) * w;
            // a jump of the travel means the track looped
            var travel = j == 0 ? data[i * Stride + 3] : V(3);
            return new TrackFrame(V(0), V(1), heading, travel, V(4), V(5), V(6), data[i * Stride + 7] > 0.5f);
        }

        /// <summary>The place on the loop nearest to a point: where, which way it faces and how far into the loop.</summary>
        public bool TryNearest(float x, float z, out TrackPoint point)
        {
            var best = -1;
            var bestD = float.MaxValue;
            for (var i = 0; i < frames; i++)
            {
                float dx = data[i * Stride] - x, dz = data[i * Stride + 1] - z;
                var d = dx * dx + dz * dz;
                if (d < bestD) { bestD = d; best = i; }
            }

            point = best < 0 ? default : new TrackPoint(data[best * Stride], data[best * Stride + 1], data[best * Stride + 2], best * frameSeconds);
            return best >= 0;
        }

        // the engine's own float arithmetic (Mathf.Repeat, Lerp, DeltaAngle), so the played frames are the ones the scene drew before
        static float Repeat(float t, float length) => Math.Max(0f, Math.Min(length, t - (float)Math.Floor(t / length) * length));

        static float Lerp(float a, float b, float t) => a + (b - a) * Math.Max(0f, Math.Min(1f, t));

        static float DeltaAngle(float current, float target)
        {
            var delta = Repeat(target - current, 360f);
            if (delta > 180f) delta -= 360f;
            return delta;
        }
    }
}
