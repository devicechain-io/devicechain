// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

using DeviceChain.Sitepulse.Simulation;
using DeviceChain.Sitepulse.Tasks;
using UnityEngine;

namespace DeviceChain.Sitepulse.Visuals
{
    /// <summary>
    /// One machine of the preview fleet as the task layer sees its body: its pose is the rig's transform,
    /// and detaching, driving and re-attaching go through the fleet, which is the only thing that moves a
    /// rig. Main thread only.
    /// </summary>
    public sealed class FleetBody : IMachineBody
    {
        readonly QuarryFleetPreview fleet;

        public FleetBody(QuarryFleetPreview fleet, MachineRig rig)
        {
            this.fleet = fleet;
            Rig = rig;
            Id = rig.name;
            Kind = rig.Kind == MachineKind.Hauler ? EquipmentKind.Hauler : rig.Kind == MachineKind.Loader ? EquipmentKind.Loader : EquipmentKind.Dozer;
        }

        public MachineRig Rig { get; }
        public string Id { get; }
        public EquipmentKind Kind { get; }

        public double X => Rig.transform.position.x;
        public double Z => Rig.transform.position.z;

        public double HeadingDegrees => SiteDefinition.Canonical(Rig.transform.eulerAngles.y);

        public bool Attached => fleet.IsOnTrack(Id);

        public void Detach() => fleet.Detach(Id);

        public void Drive(double x, double z, double headingDegrees, double distance, double steerDegrees) =>
            fleet.Drive(Id, (float)x, (float)z, (float)headingDegrees, (float)distance, (float)steerDegrees);

        public bool TryNearestTrackPoint(double x, double z, out TrackPoint point)
        {
            if (!fleet.TryNearestTrackPoint(Id, (float)x, (float)z, out var p, out var h, out var t))
            {
                point = default;
                return false;
            }

            point = new TrackPoint(p.x, p.y, h, t);
            return true;
        }

        public void Attach(TrackPoint point) => fleet.Attach(Id, (float)point.TrackSeconds);

        public void SetTrackRate(double rate) => fleet.SetTrackRate(Id, (float)rate);
    }
}
