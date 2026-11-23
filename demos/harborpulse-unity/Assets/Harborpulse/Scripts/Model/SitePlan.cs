// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

using System;
using System.Collections.Generic;
using DeviceChain.Harborpulse.Model.Director;

namespace DeviceChain.Harborpulse.Model
{
    /// <summary>A point on the ground plane, metres: X east, Z north.</summary>
    public readonly struct Ground
    {
        public Ground(double x, double z)
        {
            X = x;
            Z = z;
        }

        public double X { get; }
        public double Z { get; }
    }

    /// <summary>
    /// WGS84 to scene metres, equirectangular about an origin. Over the district's few kilometres the error is centimetres, and nothing is
    /// compressed or rescaled: 430 m between two lamps on the manifest is 430 m in the scene.
    /// </summary>
    public sealed class WorldFrame
    {
        public const double EarthRadiusM = 6371008.8;

        readonly double cosLat;

        public WorldFrame(LatLon origin)
        {
            Origin = origin;
            cosLat = Math.Cos(origin.Lat * Math.PI / 180.0);
        }

        public LatLon Origin { get; }

        public Ground ToGround(LatLon p) => new Ground(
            (p.Lon - Origin.Lon) * Math.PI / 180.0 * EarthRadiusM * cosLat,
            (p.Lat - Origin.Lat) * Math.PI / 180.0 * EarthRadiusM);
    }

    /// <summary>One quay's footprint on the waterfront.</summary>
    public sealed class QuayPlan
    {
        public string Token { get; internal set; }
        public string Name { get; internal set; }

        /// <summary>West and east ends of the quay wall, metres.</summary>
        public double XMin { get; internal set; }

        public double XMax { get; internal set; }
        public double CenterX => (XMin + XMax) / 2;
    }

    /// <summary>Where the scene stands one device, and whether that place is the manifest's (surveyed) or the scene's own choice.</summary>
    public sealed class Placement
    {
        public Placement() { }
        public Placement(LayoutDevice device, Ground at, double yawDeg = 0) { Device = device; At = at; YawDeg = yawDeg; }
        public LayoutDevice Device { get; internal set; }
        public Ground At { get; internal set; }

        /// <summary>Heading the model faces, degrees clockwise from north.</summary>
        public double YawDeg { get; internal set; }

        /// <summary>True only when <see cref="At"/> is the manifest's surveyed position (the street lights). Everything else is placed by the scene.</summary>
        public bool Surveyed { get; internal set; }
    }

    /// <summary>
    /// The waterfront, laid out from the manifest. The street lights stand where the manifest surveys them, along one promenade latitude, and
    /// each quay's wall spans its own lights. The manifest gives no other device a position, so the scene places them by rule along their
    /// quay (the gauges on the wall, the pump station behind the promenade, the chargers in the car park), and every such placement is marked
    /// <see cref="Placement.Surveyed"/> false. The response truck's depot is the manifest's.
    ///
    /// Orientation: the harbour water is north of the quay walls, the town south of the promenade.
    /// </summary>
    public sealed class SitePlan
    {
        /// <summary>The quay wall stands this far north of the promenade the lights are on.</summary>
        public const double WallOffsetM = 14;

        /// <summary>Wall length past the outermost light at each end of a quay.</summary>
        public const double QuayMarginM = 90;

        /// <summary>The quay deck above quay datum (the gauges' zero), metres. East Quay's critical flood level, 2.2 m, is just below it.</summary>
        public const double DeckHeightM = 2.4;

        /// <summary>How far the jetty that carries a quay's LoRaWAN gauge reaches into the harbour.</summary>
        public const double JettyLengthM = 46;

        public WorldFrame Frame { get; private set; }
        public IReadOnlyList<QuayPlan> Quays { get; private set; }
        public IReadOnlyList<Placement> Placements { get; private set; }

        /// <summary>The Z of every quay wall.</summary>
        public double WallZ { get; private set; }

        public Ground Depot { get; private set; }

        public const double CopingInlandM = 1.8, CrewClearanceM = 2.5, CrewFrontExtentM = 3.275;
        public static Ground GaugeCrewAt(Ground gauge, double wallZ) => new Ground(gauge.X + 4,
            Math.Min(gauge.Z - 3, wallZ - CopingInlandM - CrewClearanceM - CrewFrontExtentM));

        /// <summary>West, east, south and north extent of everything placed, metres.</summary>
        public double MinX { get; private set; }

        public double MaxX { get; private set; }
        public double MinZ { get; private set; }
        public double MaxZ { get; private set; }

        public QuayPlan Quay(string token)
        {
            foreach (var q in Quays)
                if (q.Token == token) return q;
            return null;
        }

        public Placement Find(string externalId)
        {
            foreach (var p in Placements)
                if (p.Device.ExternalId == externalId) return p;
            return null;
        }

        public static SitePlan Build(HarborLayout layout)
        {
            if (layout == null) throw new ArgumentNullException(nameof(layout));

            // the origin is the middle of the surveyed lights, so the scene is centred on the promenade
            double latSum = 0, lonSum = 0;
            var surveyed = 0;
            foreach (var d in layout.Devices)
            {
                if (!d.Position.HasValue) continue;
                latSum += d.Position.Value.Lat;
                lonSum += d.Position.Value.Lon;
                surveyed++;
            }

            if (surveyed == 0) throw new FormatException("harbor layout: no device has a surveyed position, so the waterfront has nothing to stand on");
            var frame = new WorldFrame(new LatLon(layout.PromenadeLat, lonSum / surveyed));
            var plan = new SitePlan { Frame = frame, Depot = frame.ToGround(layout.Depot) };
            var promenadeZ = frame.ToGround(new LatLon(layout.PromenadeLat, frame.Origin.Lon)).Z;
            plan.WallZ = promenadeZ + WallOffsetM;

            // each quay spans its own surveyed lights
            var quays = new List<QuayPlan>();
            foreach (var q in layout.Quays)
            {
                double min = double.MaxValue, max = double.MinValue;
                foreach (var d in layout.Devices)
                {
                    if (d.Quay != q.Token || !d.Position.HasValue) continue;
                    var g = frame.ToGround(d.Position.Value);
                    min = Math.Min(min, g.X);
                    max = Math.Max(max, g.X);
                }

                if (min > max) throw new FormatException("harbor layout: quay " + q.Token + " has no surveyed light, so the scene cannot tell where it is");
                quays.Add(new QuayPlan { Token = q.Token, Name = q.Name, XMin = min - QuayMarginM, XMax = max + QuayMarginM });
            }

            plan.Quays = quays;

            var placements = new List<Placement>();
            foreach (var d in layout.Devices)
            {
                var quay = plan.Quay(d.Quay);
                placements.Add(Place(d, quay, plan, promenadeZ));
            }

            plan.Placements = placements;

            plan.MinX = plan.MinZ = double.MaxValue;
            plan.MaxX = plan.MaxZ = double.MinValue;
            void Grow(Ground g)
            {
                plan.MinX = Math.Min(plan.MinX, g.X);
                plan.MaxX = Math.Max(plan.MaxX, g.X);
                plan.MinZ = Math.Min(plan.MinZ, g.Z);
                plan.MaxZ = Math.Max(plan.MaxZ, g.Z);
            }

            foreach (var p in placements) Grow(p.At);
            foreach (var q in quays)
            {
                Grow(new Ground(q.XMin, plan.WallZ));
                Grow(new Ground(q.XMax, plan.WallZ));
            }

            Grow(plan.Depot);
            return plan;
        }

        static Placement Place(LayoutDevice d, QuayPlan q, SitePlan plan, double promenadeZ)
        {
            if (d.Position.HasValue) return new Placement { Device = d, At = plan.Frame.ToGround(d.Position.Value), YawDeg = 0, Surveyed = true };

            var width = q.XMax - q.XMin;
            Ground at;
            double yaw = 0;
            switch (d.Kind)
            {
                case DeviceKind.Gauge:
                    // three gauges on the wall face, spread along the quay
                    at = new Ground(q.XMin + width * (2 * Math.Max(1, d.Index) - 1) / 6.0, plan.WallZ + 0.6);
                    break;
                case DeviceKind.LoraGauge:
                    // at the head of a jetty off the middle of the quay
                    at = new Ground(q.CenterX + 30, plan.WallZ + JettyLengthM);
                    break;
                case DeviceKind.LegacyGauge:
                    // the older gauge, on its own pile further out
                    at = new Ground(q.CenterX - 140, plan.WallZ + 70);
                    break;
                case DeviceKind.Pump:
                    // the pump station behind the promenade, its outfall through the wall
                    at = new Ground(q.CenterX - 60, promenadeZ - 30);
                    yaw = 180;
                    break;
                case DeviceKind.Charger:
                    at = new Ground(q.CenterX + 70 + 9 * (d.Index - 1), promenadeZ - 22);
                    yaw = 180;
                    break;
                case DeviceKind.MqttCharger:
                case DeviceKind.CertCharger:
                    // further along the same car park, past the district's chargers
                    at = new Ground(q.CenterX + 70 + 9 * (PresentationMap.LinkedBay(d.Kind, d.Index) - 1), promenadeZ - 22);
                    yaw = 180;
                    break;
                case DeviceKind.PullLight:
                    // by the wall at the quay's east end
                    at = new Ground(q.XMax - 30 + 12 * (Math.Max(1, d.Index) - 1), plan.WallZ - 3);
                    break;
                case DeviceKind.Meter:
                    // the meter's building: the Lofts set back from North Quay, the Ferry Terminal on the Basin
                    at = d.Index == 1 ? new Ground(q.CenterX - 260, promenadeZ - 80) : new Ground(q.CenterX + 220, promenadeZ - 25);
                    break;
                case DeviceKind.Truck:
                    at = plan.Depot;
                    yaw = 90;
                    break;
                default:
                    throw new FormatException("harbor layout: no placement rule for kind " + d.Kind);
            }

            return new Placement { Device = d, At = at, YawDeg = yaw, Surveyed = false };
        }
    }
}
