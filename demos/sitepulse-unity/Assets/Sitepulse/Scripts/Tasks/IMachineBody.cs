// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

using DeviceChain.Sitepulse.Simulation;

namespace DeviceChain.Sitepulse.Tasks
{
    /// <summary>A place on a machine's own track, and where on the track it is in time.</summary>
    public readonly struct TrackPoint
    {
        public TrackPoint(double x, double z, double headingDegrees, double trackSeconds)
        {
            X = x;
            Z = z;
            HeadingDegrees = headingDegrees;
            TrackSeconds = trackSeconds;
        }

        public double X { get; }
        public double Z { get; }
        public double HeadingDegrees { get; }

        /// <summary>How far into its track's loop this place is.</summary>
        public double TrackSeconds { get; }
    }

    /// <summary>
    /// A machine's body in the scene, as the task layer sees it: where it is, whether its routine track
    /// is driving it, and the three things the task layer may do to it: take it off the track, move it,
    /// and put it back on the track. The scene implements this; a test double stands in for it.
    /// </summary>
    public interface IMachineBody
    {
        string Id { get; }
        EquipmentKind Kind { get; }
        double X { get; }
        double Z { get; }
        double HeadingDegrees { get; }

        /// <summary>Its routine track is moving it.</summary>
        bool Attached { get; }

        /// <summary>Stops the track moving it; it stays where it is.</summary>
        void Detach();

        /// <summary>Puts it at the place facing the way, having travelled <paramref name="distance"/> metres since the last call.</summary>
        void Drive(double x, double z, double headingDegrees, double distance, double steerDegrees);

        /// <summary>The place on its own track nearest to a point.</summary>
        bool TryNearestTrackPoint(double x, double z, out TrackPoint point);

        /// <summary>Puts it back on its track at that place: its routine work resumes from there.</summary>
        void Attach(TrackPoint point);

        /// <summary>
        /// How fast its routine track plays, from 0 (held where it is) to 1 (the track's own pace). A track played at less than its own
        /// pace plays less of itself and goes on from where it stood; a body with no track ignores it.
        /// </summary>
        void SetTrackRate(double rate)
        {
        }
    }
}
