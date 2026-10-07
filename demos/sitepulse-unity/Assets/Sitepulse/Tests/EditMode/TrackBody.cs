// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

using System;
using DeviceChain.Sitepulse.Simulation;
using DeviceChain.Sitepulse.Tasks;

namespace DeviceChain.Sitepulse.Tests
{
    /// <summary>
    /// A machine on a choreography track with no scene: the track's frames played through <see cref="TrackPlayer"/>, the class the fleet in
    /// the scene plays them through, and driven by someone else once it is taken off. It answers the questions the scene's body does, so the
    /// task layer cannot tell the two apart.
    /// </summary>
    internal sealed class TrackBody : IMachineBody
    {
        readonly TrackPlayer player;
        float time;

        public TrackBody(string id, EquipmentKind kind, float[] frames, float frameSeconds, float period, float offset)
        {
            Id = id;
            Kind = kind;
            player = new TrackPlayer(frames, frameSeconds, period, offset);
            Attached = true;
            Place();
        }

        public string Id { get; }
        public EquipmentKind Kind { get; }
        public double X { get; private set; }
        public double Z { get; private set; }
        public double HeadingDegrees { get; private set; }
        public bool Attached { get; private set; }

        /// <summary>How fast its track plays now (see <see cref="TrackPlayer.Rate"/>).</summary>
        public float Rate => player.Rate;

        /// <summary>How many seconds of its track it has played (a held machine plays less than the clock).</summary>
        public double PlayedSeconds { get; private set; }

        /// <summary>How far into its loop it is now.</summary>
        public float TrackSeconds => player.TrackSecondsAt(time);

        public int Attaches { get; private set; }

        void Place()
        {
            var f = player.SampleAt(time);
            X = f.X;
            Z = f.Z;
            HeadingDegrees = SiteDefinition.Canonical(f.Heading);
        }

        /// <summary>The scene's clock runs on by <paramref name="dt"/>; a machine on its track plays that much of it, at its rate.</summary>
        public void Advance(double dt)
        {
            time += (float)dt;
            if (!Attached) return;
            player.Advance((float)dt);
            PlayedSeconds += dt * player.Rate;
            Place();
        }

        public void Detach()
        {
            Attached = false;
            player.Detach();
        }

        public void Drive(double x, double z, double headingDegrees, double distance, double steerDegrees)
        {
            if (Attached) throw new InvalidOperationException("driving a machine that is still on its track");
            X = x;
            Z = z;
            HeadingDegrees = headingDegrees;
        }

        public bool TryNearestTrackPoint(double x, double z, out TrackPoint point) => player.TryNearest((float)x, (float)z, out point);

        public void Attach(TrackPoint point)
        {
            player.Attach(time, (float)point.TrackSeconds);
            Attached = true;
            Attaches++;
            Place();
        }

        public void SetTrackRate(double rate) => player.SetRate((float)rate);
    }
}
