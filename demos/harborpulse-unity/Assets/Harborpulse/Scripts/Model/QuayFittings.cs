// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

using System;
using System.Collections.Generic;
using System.Linq;
using DeviceChain.Harborpulse.Model.Director;

namespace DeviceChain.Harborpulse.Model
{
    /// <summary>A covered cable trench between two points on the deck.</summary>
    public readonly struct DuctRun
    {
        public DuctRun(Ground a, Ground b, double width) { A = a; B = b; Width = width; }
        public Ground A { get; }
        public Ground B { get; }
        public double Width { get; }
        public double Length => SceneryStreet.Distance(A, B);
        /// <summary>The run's ground rectangle, for a clearance check against what stands on the quay.</summary>
        public PropFootprint Footprint(string id)
        {
            var yaw = Math.Atan2(B.X - A.X, B.Z - A.Z) * 180 / Math.PI;
            return new PropFootprint(id, new Ground((A.X + B.X) / 2, (A.Z + B.Z) / 2), Width, Math.Max(Length, .01), yaw);
        }
    }

    /// <summary>
    /// A pair of chargers that hold their own session, and what is laid for them: a sett apron under the two bays, a feeder cabinet behind
    /// the pedestals, a covered trench from the cabinet to each pedestal, and a feeder trench from the cabinet back to the quay road.
    /// </summary>
    public sealed class ChargingCourt
    {
        public string Kind { get; internal set; }
        /// <summary>The quay the pair stands on, as the presentation has it.</summary>
        public string Quay { get; internal set; }
        public IReadOnlyList<LayoutDevice> Devices { get; internal set; }
        /// <summary>The pair's own frame: its origin midway between the two pedestals, +z towards the water.</summary>
        public PropFootprint Frame { get; internal set; }
        public PropFootprint Apron { get; internal set; }
        public PropFootprint CabinetFootprint { get; internal set; }
        public Ground Cabinet => CabinetFootprint.At;
        public IReadOnlyList<DuctRun> Ducts { get; internal set; }
        public bool Certificate => Kind == DeviceKind.CertCharger;
    }

    /// <summary>
    /// The landing the pull lamps stand at: a sett apron by the wall and a ladder head between them. Nothing is trenched to these lamps. A
    /// lamp that asks for its commands needs no feeder and no signal cable; each carries its own panel and battery.
    /// </summary>
    public sealed class LampLanding
    {
        public IReadOnlyList<LayoutDevice> Lamps { get; internal set; }
        public string Quay { get; internal set; }
        public PropFootprint Frame { get; internal set; }
        public PropFootprint Apron { get; internal set; }
        public Ground Ladder { get; internal set; }
        /// <summary>The lifebuoy's post, on the landing beside the ladder.</summary>
        public Ground Lifebuoy { get; internal set; }
    }

    /// <summary>
    /// What is laid on a quay round the devices that hold their own session, so they stand in the quay and not on it. Planned here, from the
    /// same anchors the devices are drawn at, and drawn from this plan alone; the cabinets are ground props and are registered as such.
    /// </summary>
    public sealed class QuayFittings
    {
        /// <summary>The apron under a pair of bays: how far it reaches past the outer pedestals, behind the pedestals' line, and towards the water.</summary>
        public const double ApronSideM = 3.2, ApronBackM = 3.4, ApronFrontM = 8.2;
        /// <summary>The cabinet: how far behind the pedestals' line it stands, and its footprint.</summary>
        public const double CabinetBackM = 2.6, CabinetWidthM = 1.5, CabinetDepthM = .8;
        /// <summary>The trench along the back of the pedestals, and its width.</summary>
        public const double TrunkBackM = 1.6, DuctWidthM = .45;
        /// <summary>How far short of the quay's landward edge the feeder trench ends: at the near kerb of the quay road.</summary>
        public const double RoadFromEdgeM = 9;
        /// <summary>The lamps' landing: how far its apron reaches past the lamps along the wall, where it starts (at the coping's inland edge), and its depth.</summary>
        public const double LandingSideM = 2.2, LandingFromWallM = SitePlan.CopingInlandM, LandingDepthM = 6.4;
        /// <summary>The wall's railing stops this far either side of a ladder head, at a post.</summary>
        public const double RailOpeningHalfM = .8;
        /// <summary>The lifebuoy's post: how far along the wall from the ladder, and behind the wall's face.</summary>
        public const double LifebuoyAlongM = 1.9, LifebuoyBehindWallM = 2.4;

        public IReadOnlyList<ChargingCourt> Courts { get; private set; }
        public IReadOnlyList<LampLanding> Landings { get; private set; }

        public static QuayFittings Plan(PresentationMap map, HarborLayout layout)
        {
            if (map == null) throw new ArgumentNullException(nameof(map));
            if (layout == null) throw new ArgumentNullException(nameof(layout));
            var courts = new List<ChargingCourt>(); var landings = new List<LampLanding>();
            foreach (var group in layout.Devices.Where(d => DeviceKind.IsLinkedCharger(d.Kind)).GroupBy(d => (d.Kind, Quay: map.QuayFor(d))))
            {
                var devices = group.OrderBy(d => d.Index).ToArray();
                var quay = WorldPropPlan.QuayFrame(map, group.Key.Quay);
                var frame = Between(map, devices, quay.Yaw, "court:" + group.Key.Kind + ":" + group.Key.Quay);
                var half = HalfSpan(map, devices, frame);
                // the feeder runs straight back from the cabinet to the quay road's near kerb
                var behind = Depth(quay, frame.At);
                var roadZ = -(quay.Depth - RoadFromEdgeM - behind);
                var ducts = new List<DuctRun> { new DuctRun(frame.Local(-half, -TrunkBackM), frame.Local(half, -TrunkBackM), DuctWidthM) };
                foreach (var d in devices)
                {
                    var x = Along(frame, map.Anchors[d.Token]);
                    ducts.Add(new DuctRun(frame.Local(x, -TrunkBackM), frame.Local(x, -.5), DuctWidthM));
                }
                ducts.Add(new DuctRun(frame.Local(0, -TrunkBackM), frame.Local(0, -CabinetBackM + CabinetDepthM / 2), DuctWidthM));
                if (roadZ < -CabinetBackM - CabinetDepthM / 2 - 1)
                    ducts.Add(new DuctRun(frame.Local(0, -CabinetBackM - CabinetDepthM / 2), frame.Local(0, roadZ), DuctWidthM));
                courts.Add(new ChargingCourt
                {
                    Kind = group.Key.Kind, Quay = group.Key.Quay, Devices = devices, Frame = frame,
                    Apron = new PropFootprint(frame.Id + ":apron", frame.Local(0, (ApronFrontM - ApronBackM) / 2), 2 * (half + ApronSideM), ApronFrontM + ApronBackM, frame.Yaw),
                    CabinetFootprint = new PropFootprint(frame.Id + ":cabinet", frame.Local(0, -CabinetBackM), CabinetWidthM, CabinetDepthM, frame.Yaw),
                    Ducts = ducts.AsReadOnly(),
                });
            }
            foreach (var group in layout.Devices.Where(d => d.Kind == DeviceKind.PullLight).GroupBy(d => map.QuayFor(d)))
            {
                var lamps = group.OrderBy(d => d.Index).ToArray();
                var quay = WorldPropPlan.QuayFrame(map, group.Key);
                var frame = Between(map, lamps, quay.Yaw, "landing:" + group.Key);
                var half = HalfSpan(map, lamps, frame);
                var behind = Depth(quay, frame.At);
                // the apron starts at the coping's inland edge, wherever behind the wall the lamps stand
                var front = behind - LandingFromWallM; var back = front - LandingDepthM;
                landings.Add(new LampLanding
                {
                    Lamps = lamps, Quay = group.Key, Frame = frame,
                    Apron = new PropFootprint(frame.Id + ":apron", frame.Local(0, (front + back) / 2), 2 * (half + LandingSideM), LandingDepthM, frame.Yaw),
                    Ladder = frame.Local(0, behind), Lifebuoy = frame.Local(-LifebuoyAlongM, behind - LifebuoyBehindWallM),
                });
            }
            return new QuayFittings { Courts = courts.AsReadOnly(), Landings = landings.AsReadOnly() };
        }

        /// <summary>Every trench of every court, with an identity: for clearance checks.</summary>
        public IEnumerable<PropFootprint> DuctFootprints()
        {
            foreach (var c in Courts) for (var i = 0; i < c.Ducts.Count; i++) yield return c.Ducts[i].Footprint(c.Frame.Id + ":duct:" + i);
        }

        static PropFootprint Between(PresentationMap map, LayoutDevice[] devices, double yaw, string id) =>
            new PropFootprint(id, new Ground(devices.Average(d => map.Anchors[d.Token].X), devices.Average(d => map.Anchors[d.Token].Z)), 1, 1, yaw);

        /// <summary>A point's place along a frame's own x axis.</summary>
        static double Along(PropFootprint frame, Ground p)
        {
            var a = frame.Yaw * Math.PI / 180;
            return (p.X - frame.At.X) * Math.Cos(a) - (p.Z - frame.At.Z) * Math.Sin(a);
        }

        static double HalfSpan(PresentationMap map, LayoutDevice[] devices, PropFootprint frame) => devices.Max(d => Math.Abs(Along(frame, map.Anchors[d.Token])));

        /// <summary>How far behind its quay's wall a point stands.</summary>
        static double Depth(PropFootprint quay, Ground p)
        {
            var a = quay.Yaw * Math.PI / 180;
            return -((p.X - quay.At.X) * Math.Sin(a) + (p.Z - quay.At.Z) * Math.Cos(a));
        }
    }
}
