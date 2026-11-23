// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

using System;
using System.Collections.Generic;
using System.Linq;
using DeviceChain.Harborpulse.Model;
using DeviceChain.Harborpulse.Model.Director;
using NUnit.Framework;

namespace DeviceChain.Harborpulse.Tests
{
    /// <summary>
    /// U10: the six devices that hold their own session are drawn. Two chargers on MQTT under a token and two under a certificate continue
    /// North Quay's charging row; two lamps that ask for their commands flank the landing at the wall's east end. Each kind has its own
    /// board, reads only what its profile reports, and stands in ground that was laid for it.
    /// </summary>
    public class U10Tests
    {
        const double Deck = SitePlan.DeckHeightM;
        const double Wide = 16.0 / 9, Tall = 9.0 / 16;
        static readonly string[] New = { DeviceKind.MqttCharger, DeviceKind.CertCharger, DeviceKind.PullLight };

        static DateTimeOffset Now => Kit.T0;
        static ClockReading Clock(double h) => new ClockReading(h * 3600, ClockMode.Live, "");
        static ClockReading Preview(double h) => new ClockReading(h * 3600, ClockMode.Preview, "not platform data");
        static LayoutDevice Device(string kind, int index) => U5Plan.Layout.Devices.Single(d => d.Kind == kind && d.Index == index);
        static IEnumerable<LayoutDevice> Six => U5Plan.Layout.Devices.Where(d => New.Contains(d.Kind));

        // ---------------------------------------------------------------- the layout and the anchors

        [Test]
        public void TheSixAreInTheLayoutEachKindUnderItsOwnDeviceType()
        {
            Assert.That(Six.Count(), Is.EqualTo(6));
            foreach (var kind in New) Assert.That(Six.Count(d => d.Kind == kind), Is.EqualTo(2), kind);
            // one device type to a kind, across the whole layout: the binder resolves a kind to a single type
            foreach (var group in U5Plan.Layout.Devices.GroupBy(d => d.Kind))
                Assert.That(group.Select(d => d.DeviceType).Distinct().Count(), Is.EqualTo(1), group.Key);
            Assert.That(Six.Select(d => d.DeviceType).Distinct().Count(), Is.EqualTo(3));
            foreach (var d in Six)
            {
                Assert.That(d.Quay, Is.EqualTo("hp-north-quay"), d.Token);
                Assert.That(d.Position.HasValue, Is.False, d.Token + ": the manifest surveys none of them");
            }
            // none of them is a district charger or a street light: the rollout's six chargers and the campaign's twelve lamps are what they were
            Assert.That(U5Plan.Layout.Devices.Count(d => d.Kind == DeviceKind.Charger), Is.EqualTo(6));
            Assert.That(U5Plan.Layout.Devices.Count(d => d.Kind == DeviceKind.Light), Is.EqualTo(12));
        }

        [Test]
        public void TheChargersContinueNorthQuaysRowAndLeaveTheFourthLightItsPlace()
        {
            var map = U5Plan.Map;
            var row = U5Plan.Layout.Devices.Where(d => DeviceKind.HasBay(d.Kind) && d.Quay == "hp-north-quay").Select(d => map.Anchors[d.Token]).OrderBy(a => a.X).ToArray();
            Assert.That(row.Length, Is.EqualTo(6));
            foreach (var a in row) Assert.That(a.Z, Is.EqualTo(row[0].Z).Within(1e-9), "one row, one line");
            // 9 m bays, and one bay's width left where the street light stands
            Assert.That(row.Select(a => a.X), Is.EqualTo(new[] { 24.0, 33, 42, 51, 69, 78 }).Within(1e-9));
            var lamp = map.Anchors["hp-light-north-4"];
            Assert.That(lamp.X, Is.EqualTo(60).Within(1e-9));
            Assert.That(map.Anchors[Device(DeviceKind.MqttCharger, 2).Token].X, Is.LessThan(lamp.X - 4));
            Assert.That(map.Anchors[Device(DeviceKind.CertCharger, 1).Token].X, Is.GreaterThan(lamp.X + 4));
            // the two on a token stand together, and the two on a certificate
            foreach (var kind in new[] { DeviceKind.MqttCharger, DeviceKind.CertCharger })
                Assert.That(Math.Abs(map.Anchors[Device(kind, 1).Token].X - map.Anchors[Device(kind, 2).Token].X), Is.EqualTo(9).Within(1e-9), kind);
        }

        [Test]
        public void ThePullLampsStandByTheWallAtTheQuaysEastEnd()
        {
            var map = U5Plan.Map; var quay = map.Quays["hp-north-quay"];
            var wall = quay.Min(p => p.Z); var east = quay.Max(p => p.X);
            var a = map.Anchors[Device(DeviceKind.PullLight, 1).Token]; var b = map.Anchors[Device(DeviceKind.PullLight, 2).Token];
            Assert.That(a.Z - wall, Is.EqualTo(PresentationMap.PullLampDepthM).Within(1e-9));
            Assert.That(b.Z, Is.EqualTo(a.Z).Within(1e-9));
            Assert.That(b.X - a.X, Is.EqualTo(PresentationMap.PullLampSpacingM).Within(1e-9));
            Assert.That(east - b.X, Is.InRange(2, 4), "inside the quay's end");
            // east of every charger and every street light on the quay: the landing is its own place
            foreach (var d in U5Plan.Layout.Devices.Where(d => d.Quay == "hp-north-quay" && (DeviceKind.HasBay(d.Kind) || d.Kind == DeviceKind.Light)))
                Assert.That(map.Anchors[d.Token].X, Is.LessThan(a.X - 8), d.Token);
        }

        [Test]
        public void EveryNewAnchorIsOnItsPlatformQuayAndInBothHomeFrames()
        {
            var map = U5Plan.Map;
            foreach (var d in Six)
            {
                var at = map.Anchors[d.Token];
                Assert.That(PresentationMap.Contains(map.Quays[d.Quay], at), Is.True, d.Token);
                Assert.That(map.QuayFor(d), Is.EqualTo(d.Quay), d.Token);
                foreach (var (pose, aspect) in new[] { ("pose:home", Wide), ("pose:home:portrait", Tall) })
                {
                    Assert.That(CameraMath.Project(map.Pose(pose), aspect, at.X, Deck + 1, at.Z, out var nx, out var ny), Is.True, d.Token + " " + pose);
                    Assert.That(Math.Max(Math.Abs(nx), Math.Abs(ny)), Is.LessThanOrEqualTo(.94), d.Token + " " + pose);
                }
            }
        }

        [Test]
        public void AMembershipThatPutsOneOnAnotherQuayStillBuildsTheWorld()
        {
            // the platform's own answer wins over the layout's quay; each of the six then stands in a row of its own on that quay
            foreach (var quay in new[] { "hp-east-quay", "hp-basin" })
            {
                var membership = U5Plan.Layout.Devices.ToDictionary(d => d.Token, d => d.Quay);
                foreach (var d in Six) membership[d.Token] = quay;
                var map = new PresentationMap(U5Plan.Layout, membership);
                foreach (var d in Six) Assert.That(PresentationMap.Contains(map.Quays[quay], map.Anchors[d.Token]), Is.True, d.Token + " on " + quay);
                var plan = WorldPropPlan.Generate(map, U5Plan.Layout);
                Assert.That(FootprintRegistry.FindOverlaps(plan.Registry.Entries.Where(e => e.Id.StartsWith("device:", StringComparison.Ordinal) || e.Id.StartsWith("car:", StringComparison.Ordinal) || e.Id.StartsWith("court:", StringComparison.Ordinal)).ToArray()), Is.Empty, quay);
                Assert.That(plan.Fittings.Courts.Count, Is.EqualTo(2), quay);
                Assert.That(plan.Fittings.Courts.Select(c => c.Quay).Distinct(), Is.EqualTo(new[] { quay }));
                // the lamps are by that quay's wall too, on their landing, and the ladder between them is clear of the wall's fittings
                var landing = plan.Fittings.Landings.Single();
                Assert.That(landing.Quay, Is.EqualTo(quay));
                foreach (var d in landing.Lamps) Assert.That(Inside(landing.Apron, map.Anchors[d.Token]), Is.True, d.Token + " on its landing on " + quay);
                foreach (var apron in plan.Fittings.Courts.Select(c => c.Apron).Concat(new[] { landing.Apron }))
                    foreach (var c in Corners(apron)) Assert.That(PresentationMap.Contains(map.Quays[quay], c), Is.True, apron.Id + " on " + quay);
                var wallX = quay == "hp-east-quay" ? map.Quays[quay].Min(p => p.X) : map.Quays[quay].Max(p => p.X);
                Assert.That(landing.Ladder.X, Is.EqualTo(wallX).Within(1e-9), quay);
                var ladder = new PropFootprint("ladder", landing.Ladder, 1.6, 1.0);
                foreach (var p in plan.Props.Where(p => (p.Kind == "Bollard" || p.Kind == "Cleat") && p.Area == quay))
                    Assert.That(ladder.Intersects(p.Footprint, 0), Is.False, p.Footprint.Id + " on " + quay);
                foreach (var duct in plan.Fittings.DuctFootprints())
                    foreach (var e in plan.Registry.Entries.Where(e => !Six.Any(d => e.Id == "device:" + d.Token) && !e.Id.EndsWith(":cabinet", StringComparison.Ordinal)))
                        Assert.That(duct.Intersects(e, 0), Is.False, duct.Id + " under " + e.Id + " on " + quay);
            }
        }

        // ---------------------------------------------------------------- nothing that stood there moved

        static IEnumerable<(string, double, double, double, double, double)> Scenery(WorldPropPlan plan, HashSet<string> six) =>
            plan.Registry.Entries.Where(e => !six.Any(t => e.Id == "device:" + t || e.Id == "car:" + t) && !e.Id.StartsWith("court:", StringComparison.Ordinal))
                .Select(e => (e.Id, e.At.X, e.At.Z, e.Width, e.Depth, e.Yaw));

        [Test]
        public void TheSixDisplaceNothingThatStoodOnTheQuayOrInTheTown()
        {
            // Every prop is placed against what was registered before it, so a new device could have moved a bollard, a bay or a house. The
            // same world with the six standing somewhere else on North Quay has exactly the same scenery: none of it was placed round them.
            var six = new HashSet<string>(Six.Select(d => d.Token));
            PresentationMap Standing(double x0, double z)
            {
                var moved = new Dictionary<string, Ground>(); var n = 0;
                foreach (var d in Six) moved[d.Token] = new Ground(x0 + 4 * n++, z);
                var map = new PresentationMap(U5Plan.Layout, null, moved);
                foreach (var d in Six) Assert.That(map.Anchors[d.Token].Z, Is.EqualTo(z));
                return map;
            }
            var other = WorldPropPlan.Generate(Standing(0, 86), U5Plan.Layout);
            var a = Scenery(U5Plan.Plan, six).ToArray(); var b = Scenery(other, six).ToArray();
            Assert.That(a.Length, Is.GreaterThan(400));
            Assert.That(b, Is.EqualTo(a));
            // and the forest, whose trees are props without a registry entry
            Assert.That(other.Props.Count, Is.EqualTo(U5Plan.Plan.Props.Count));
            // the control: stood at the back of the quay, by the town, the same six do move a house lot, and this comparison sees it
            var crowding = Scenery(WorldPropPlan.Generate(Standing(-58, 118), U5Plan.Layout), six).ToArray();
            Assert.That(crowding, Is.Not.EqualTo(a));
            // the district's own anchors are where U9 left them
            Assert.That(U5Plan.Map.Anchors["hp-charger-north-2"].X, Is.EqualTo(33).Within(1e-9));
            Assert.That(U5Plan.Map.Anchors["hp-gauge-north-3"].X, Is.EqualTo(67.5).Within(1e-9));
        }

        // ---------------------------------------------------------------- the ground laid for them

        [Test]
        public void EachPairOfChargersHasACabinetAndATrenchToEveryPedestal()
        {
            var plan = U5Plan.Plan; var map = U5Plan.Map; var fittings = plan.Fittings;
            Assert.That(fittings.Courts.Select(c => c.Kind), Is.EquivalentTo(new[] { DeviceKind.MqttCharger, DeviceKind.CertCharger }));
            foreach (var court in fittings.Courts)
            {
                Assert.That(court.Devices.Count, Is.EqualTo(2));
                Assert.That(plan.Registry.Entries.Any(e => e.Id == court.CabinetFootprint.Id), Is.True, "the cabinet stands on the ground and is registered");
                // behind the pedestals, between them
                Assert.That(court.Cabinet.Z, Is.GreaterThan(map.Anchors[court.Devices[0].Token].Z + 1.5));
                Assert.That(court.Cabinet.X, Is.EqualTo(court.Devices.Average(d => map.Anchors[d.Token].X)).Within(1e-9));
                // a trench ends at every pedestal and one at the cabinet
                foreach (var d in court.Devices)
                    Assert.That(court.Ducts.Min(r => Math.Min(SceneryStreet.Distance(r.A, map.Anchors[d.Token]), SceneryStreet.Distance(r.B, map.Anchors[d.Token]))), Is.LessThan(.6), d.Token);
                Assert.That(court.Ducts.Min(r => Math.Min(SceneryStreet.Distance(r.A, court.Cabinet), SceneryStreet.Distance(r.B, court.Cabinet))), Is.LessThan(.6));
                // and the trunk, the pedestals' stubs and the cabinet's stub are one connected run (the feeder leaves the cabinet's other side)
                var joined = new HashSet<int> { 0 }; var grew = true;
                while (grew)
                {
                    grew = false;
                    for (var i = 0; i < court.Ducts.Count; i++)
                        if (!joined.Contains(i) && joined.Any(j => Touches(court.Ducts[i], court.Ducts[j]))) { joined.Add(i); grew = true; }
                }
                Assert.That(joined.Count, Is.EqualTo(court.Ducts.Count - 1));
                // the feeder reaches the quay road and stops at its kerb
                var quay = map.Quays["hp-north-quay"]; var road = quay.Max(p => p.Z) - QuayFittings.RoadFromEdgeM;
                Assert.That(court.Ducts.Max(r => Math.Max(r.A.Z, r.B.Z)), Is.EqualTo(road).Within(1e-9));
                // the apron holds both pedestals and both bays
                foreach (var d in court.Devices)
                {
                    Assert.That(Inside(court.Apron, map.Anchors[d.Token]), Is.True, d.Token);
                    var car = plan.Find("car:" + d.Token);
                    Assert.That(car, Is.Not.Null, d.Token + " has a bay");
                    foreach (var corner in Corners(car.Footprint)) Assert.That(Inside(court.Apron, corner), Is.True, d.Token + " bay inside its apron");
                }
            }
            // the two courts, and the district's bays and the street light beside them, do not overlap
            Assert.That(fittings.Courts[0].Apron.Intersects(fittings.Courts[1].Apron), Is.False);
            foreach (var court in fittings.Courts) foreach (var t in new[] { "hp-charger-north-1", "hp-charger-north-2", "hp-light-north-4" })
                Assert.That(Inside(court.Apron, map.Anchors[t]), Is.False, t);
        }

        static bool Touches(DuctRun a, DuctRun b) => new[] { a.A, a.B }.Any(p => SceneryStreet.SegmentDistance(p, b.A, b.B) < .05) || new[] { b.A, b.B }.Any(p => SceneryStreet.SegmentDistance(p, a.A, a.B) < .05);
        static IEnumerable<Ground> Corners(PropFootprint f) => new[] { f.Local(-f.Width / 2, -f.Depth / 2), f.Local(f.Width / 2, -f.Depth / 2), f.Local(-f.Width / 2, f.Depth / 2), f.Local(f.Width / 2, f.Depth / 2) };
        static bool Inside(PropFootprint f, Ground p) => f.Intersects(new PropFootprint("point", p, .01, .01), 0);

        [Test]
        public void NoTrenchRunsUnderAnythingThatStandsOnTheQuay()
        {
            var plan = U5Plan.Plan;
            var ducts = plan.Fittings.DuctFootprints().ToArray();
            Assert.That(ducts.Length, Is.EqualTo(10), "two courts: a trunk, two stubs, a cabinet stub and a feeder each");
            var own = new HashSet<string>(U5Plan.Layout.Devices.Where(d => DeviceKind.IsLinkedCharger(d.Kind)).Select(d => "device:" + d.Token));
            foreach (var duct in ducts)
                foreach (var e in plan.Registry.Entries)
                {
                    // a trench ends under its own pedestal's plinth and at its own cabinet's foot
                    if (own.Contains(e.Id) || e.Id.EndsWith(":cabinet", StringComparison.Ordinal)) continue;
                    Assert.That(duct.Intersects(e, 0), Is.False, duct.Id + " under " + e.Id);
                }
            // planted: a trench through the district's second charger would be caught
            var through = new DuctRun(U5Plan.Map.Anchors["hp-charger-north-1"], U5Plan.Map.Anchors["hp-charger-north-2"], QuayFittings.DuctWidthM).Footprint("planted");
            Assert.That(plan.Registry.Entries.Any(e => e.Id == "device:hp-charger-north-2" && through.Intersects(e, 0)), Is.True);
        }

        [Test]
        public void ThePullLampsHaveALandingAndNoTrench()
        {
            var plan = U5Plan.Plan; var map = U5Plan.Map; var landing = plan.Fittings.Landings.Single();
            Assert.That(landing.Lamps.Select(d => d.Kind).Distinct(), Is.EqualTo(new[] { DeviceKind.PullLight }));
            foreach (var d in landing.Lamps) Assert.That(Inside(landing.Apron, map.Anchors[d.Token]), Is.True, d.Token);
            // a lamp that asks for its commands carries its own panel and battery: no court, no cabinet and no trench is laid to it
            foreach (var court in plan.Fittings.Courts)
            {
                Assert.That(court.Devices.Any(d => d.Kind == DeviceKind.PullLight), Is.False);
                Assert.That(court.Apron.Intersects(landing.Apron), Is.False);
            }
            foreach (var duct in plan.Fittings.DuctFootprints()) Assert.That(duct.Intersects(landing.Apron, 0), Is.False, duct.Id);
            // the landing and both courts lie wholly on their quay
            foreach (var apron in plan.Fittings.Courts.Select(c => c.Apron).Concat(new[] { landing.Apron }))
                foreach (var c in Corners(apron)) Assert.That(PresentationMap.Contains(map.Quays["hp-north-quay"], c), Is.True, apron.Id);
            // the apron lies behind the coping and the ladder is on the wall between the lamps
            var wall = map.Quays["hp-north-quay"].Min(p => p.Z);
            foreach (var c in Corners(landing.Apron)) Assert.That(c.Z, Is.GreaterThanOrEqualTo(wall + QuayFittings.LandingFromWallM - 1e-9));
            Assert.That(landing.Ladder.Z, Is.EqualTo(wall).Within(1e-9));
            Assert.That(landing.Ladder.X, Is.EqualTo(landing.Lamps.Average(d => map.Anchors[d.Token].X)).Within(1e-9));
            // and the ladder's place is clear of the mooring bollards and cleats along the wall
            var ladder = new PropFootprint("ladder", landing.Ladder, 1.0, 1.6);
            var fittings = plan.Props.Where(p => p.Kind == "Bollard" || p.Kind == "Cleat").ToArray();
            Assert.That(fittings.Length, Is.GreaterThan(10));
            var buoy = new PropFootprint("lifebuoy", landing.Lifebuoy, .7, .7);
            Assert.That(Inside(landing.Apron, landing.Lifebuoy), Is.True, "the lifebuoy's post stands on the landing");
            Assert.That(buoy.Intersects(ladder, 0), Is.False);
            foreach (var p in fittings)
            {
                Assert.That(ladder.Intersects(p.Footprint, 0), Is.False, p.Footprint.Id);
                Assert.That(buoy.Intersects(p.Footprint, 0), Is.False, p.Footprint.Id);
            }
            // the control: a ladder head a few metres west would stand in the mooring bollard there, and this check sees it
            var bollard = fittings.Where(p => p.Kind == "Bollard").OrderBy(p => SceneryStreet.Distance(p.At, landing.Ladder)).First();
            Assert.That(SceneryStreet.Distance(bollard.At, landing.Ladder), Is.LessThan(6));
            Assert.That(new PropFootprint("planted", new Ground(bollard.At.X, landing.Ladder.Z), 1.0, 1.6).Intersects(bollard.Footprint, 0), Is.True);
        }

        // ---------------------------------------------------------------- what each kind reads

        [Test]
        public void EachKindReadsOnlyWhatItsProfileReports()
        {
            Assert.That(HarborObservedModel.MetricsFor(DeviceKind.MqttCharger), Is.EqualTo(new[] { Metrics.ChargerPower }));
            Assert.That(HarborObservedModel.MetricsFor(DeviceKind.CertCharger), Is.EqualTo(new[] { Metrics.ChargerPower }));
            Assert.That(HarborObservedModel.MetricsFor(DeviceKind.PullLight), Is.EqualTo(new[] { Metrics.Dim }));
            foreach (var kind in DeviceKind.All) Assert.That(HarborObservedModel.MetricsFor(kind), Is.Not.Empty, kind);
        }

        [Test]
        public void ALinkedChargerIsReportingSilentOrNeverHeardAndIsNotADistrictCharger()
        {
            var layout = Kit.Layout(); var src = new HarborTestSource(); var model = new HarborObservedModel(layout);
            var mqtt = Device(DeviceKind.MqttCharger, 1).ExternalId; var cert = Device(DeviceKind.CertCharger, 2).ExternalId;
            var frame = model.Update(src, Clock(9), Now);
            Assert.That(frame.LinkedChargers.Keys, Is.EquivalentTo(layout.Devices.Where(d => DeviceKind.IsLinkedCharger(d.Kind)).Select(d => d.ExternalId)));
            Assert.That(frame.Chargers.ContainsKey(mqtt), Is.False, "not in the rollout's chargers");
            Assert.That(frame.Chargers.Count, Is.EqualTo(6));
            Assert.That(frame.LinkedChargers[mqtt].NoReading, Is.True);
            Assert.That(frame.LinkedChargers[mqtt].Link, Is.EqualTo("never reported"));
            Assert.That(frame.LinkedChargers[mqtt].Delivering, Is.False, "no reading, no car");
            src.Set(mqtt, Metrics.ChargerPower, 7.2, Now); src.Set(cert, Metrics.ChargerPower, 0.2, Now);
            frame = model.Update(src, Clock(9), Now.AddSeconds(45));
            Assert.That(frame.LinkedChargers[mqtt].Reporting, Is.True);
            Assert.That(frame.LinkedChargers[mqtt].Delivering, Is.True);
            Assert.That(frame.LinkedChargers[cert].Reporting, Is.True);
            Assert.That(frame.LinkedChargers[cert].Delivering, Is.False, "reporting, but under the delivering line");
            frame = model.Update(src, Clock(9), Now.AddSeconds(45.1));
            Assert.That(frame.LinkedChargers[mqtt].Stale, Is.True);
            Assert.That(frame.LinkedChargers[mqtt].Link, Is.EqualTo("silent"));
            Assert.That(frame.LinkedChargers[mqtt].Delivering, Is.False, "a silent charger's last power is not drawn as a car");
            // a district charger's other metrics are never read for one of these, whatever a source holds
            src.Set(mqtt, Metrics.SessionActive, 1, Now); src.Set(mqtt, Metrics.ConnectorTemp, 90, Now);
            Assert.That(model.Update(src, Clock(9), Now).Measurements[mqtt].Keys, Is.EqualTo(new[] { Metrics.ChargerPower }));
        }

        [TestCase(null, 0)] [TestCase(0.0, 0)] [TestCase(1.0, 1)] [TestCase(25.0, 1)] [TestCase(25.1, 2)] [TestCase(35.0, 2)] [TestCase(50.0, 2)]
        [TestCase(75.0, 3)] [TestCase(75.1, 4)] [TestCase(100.0, 4)] [TestCase(140.0, 4)] [TestCase(-5.0, 0)]
        public void APullLampsPipsFollowItsReportedLevel(double? pct, int pips) => Assert.That(PullLampState.PipsFor(pct), Is.EqualTo(pips));

        [Test]
        public void APullLampShowsItsLevelOnlyWhileTheReportIsCurrentAndIsNotAStreetLight()
        {
            var layout = Kit.Layout(); var src = new HarborTestSource(); var model = new HarborObservedModel(layout);
            var id = Device(DeviceKind.PullLight, 1).ExternalId;
            var frame = model.Update(src, Clock(9), Now);
            Assert.That(frame.PullLamps.Count, Is.EqualTo(2));
            Assert.That(frame.Lamps.ContainsKey(id), Is.False);
            Assert.That(frame.Lamps.Count, Is.EqualTo(12));
            Assert.That(frame.PullLamps[id].DimPct, Is.Null);
            Assert.That(frame.PullLamps[id].Pips, Is.EqualTo(0));
            src.Set(id, Metrics.Dim, 35, Now);
            frame = model.Update(src, Clock(10.1), Now.AddSeconds(10));
            Assert.That(frame.PullLamps[id].DimPct, Is.EqualTo(35));
            Assert.That(frame.PullLamps[id].Pips, Is.EqualTo(2));
            frame = model.Update(src, Clock(10.1), Now.AddSeconds(46));
            Assert.That(frame.PullLamps[id].Stale, Is.True);
            Assert.That(frame.PullLamps[id].DimPct, Is.Null, "a silent lamp's last level is not shown as its level now");
            Assert.That(frame.PullLamps[id].Pips, Is.EqualTo(0));
            // a lamp_on a source happened to hold is not this lamp's to report
            src.Set(id, Metrics.LampOn, 1, Now);
            Assert.That(model.Update(src, Clock(10.1), Now).Measurements[id].ContainsKey(Metrics.LampOn), Is.False);
        }

        // ---------------------------------------------------------------- the preview's stand-in day

        static HarborFrame PreviewAt(double hour)
        {
            var layout = Kit.Layout(); var source = new PreviewSource(layout); source.SetTime(hour * 3600, Now);
            return new HarborObservedModel(layout).Update(source, Preview(hour), Now);
        }

        [Test]
        public void ThePreviewFollowsTheSimulatorsDayForTheSix()
        {
            var mqtt1 = Device(DeviceKind.MqttCharger, 1).ExternalId; var mqtt2 = Device(DeviceKind.MqttCharger, 2).ExternalId;
            var cert1 = Device(DeviceKind.CertCharger, 1).ExternalId; var cert2 = Device(DeviceKind.CertCharger, 2).ExternalId;
            var lamp = Device(DeviceKind.PullLight, 1).ExternalId;
            // the morning: all four charging, the lamps at full level
            var f = PreviewAt(9.5);
            foreach (var id in new[] { mqtt1, mqtt2, cert1, cert2 }) Assert.That(f.LinkedChargers[id].Delivering, Is.True, id);
            Assert.That(f.PullLamps[lamp].Pips, Is.EqualTo(4));
            Assert.That(f.AlarmCounts[mqtt1], Is.EqualTo(0));
            // 10:00: the lamps have fetched their set-dim
            f = PreviewAt(10.02);
            Assert.That(f.PullLamps[lamp].DimPct, Is.EqualTo(PreviewSource.PullDimPct));
            Assert.That(f.PullLamps[lamp].Pips, Is.EqualTo(2));
            // 12:10: the first charger on a token has dropped its session and the platform's connectivity rule says so at once, while
            // its last report is still inside the silence window (an hour of the day is a minute of the preview's wall time)
            f = PreviewAt(12.17);
            Assert.That(f.LinkedChargers[mqtt1].Reporting, Is.True, "ten seconds after its last report it is not yet called silent");
            Assert.That(f.AlarmCounts[mqtt1], Is.EqualTo(1));
            // 12:54: silent now, and the alarm still stands; its neighbour never dropped
            f = PreviewAt(12.9);
            Assert.That(f.LinkedChargers[mqtt1].Link, Is.EqualTo("silent"));
            Assert.That(f.LinkedChargers[mqtt2].Reporting, Is.True);
            Assert.That(f.Beacons.Any(b => b.Device == mqtt1 && b.Key == "mqtt-charger-disconnected" && !b.Cleared), Is.True);
            Assert.That(f.AlarmCounts[mqtt1], Is.EqualTo(1));
            Assert.That(f.AlarmCounts[mqtt2], Is.EqualTo(0));
            // 13:30: back, and the alarm cleared
            f = PreviewAt(13.5);
            Assert.That(f.LinkedChargers[mqtt1].Reporting, Is.True);
            Assert.That(f.AlarmCounts[mqtt1], Is.EqualTo(0));
            // 14:54: the second charger on a certificate has been refused since 14:00 and is silent; the first is not
            f = PreviewAt(14.9);
            Assert.That(f.LinkedChargers[cert2].Link, Is.EqualTo("silent"));
            Assert.That(f.LinkedChargers[cert1].Reporting, Is.True);
            // the district's own stand-ins did not change: six chargers, six rollout targets
            Assert.That(f.Chargers.Count, Is.EqualTo(6));
            Assert.That(f.Rollout.Targets.Count, Is.EqualTo(6));
        }

        [Test]
        public void ThePreviewsFreshCountHoldsAllFortyThreeButTheLegacyGaugeAndDropsASilentCharger()
        {
            var layout = Kit.Layout();
            int Fresh(double hour)
            {
                var source = new PreviewSource(layout); source.SetTime(hour * 3600, Now);
                return layout.Devices.Count(d => HarborObservedModel.MetricsFor(d.Kind).Any(m => source.TryGet(d.ExternalId, m, out var r) && Now - r.OccurredAt <= TimeSpan.FromSeconds(60)));
            }
            // the preview gives the legacy gauge nothing (its decoder is the wrong one), so it is never among the fresh
            Assert.That(Fresh(6.1), Is.EqualTo(PresentationMap.DeviceCount - 1));
            Assert.That(Fresh(15.5), Is.EqualTo(PresentationMap.DeviceCount - 2), "nor, an hour and a half after its last report, is the charger whose certificate is refused");
        }

        // ---------------------------------------------------------------- a rig's fixed eye

        [Test]
        public void AFixedEyeIsSevenFiniteNumbersAndAnythingElseIsRefused()
        {
            var eye = CameraMath.ParsePose("72/20/20/72/3/88/50");
            Assert.That(eye.Camera.X, Is.EqualTo(72)); Assert.That(eye.Camera.Y, Is.EqualTo(20)); Assert.That(eye.Camera.Z, Is.EqualTo(20));
            Assert.That(eye.Look.Y, Is.EqualTo(3)); Assert.That(eye.Look.Z, Is.EqualTo(88)); Assert.That(eye.VerticalFov, Is.EqualTo(50));
            Assert.That(CameraMath.ParsePose("-1.5/2/3e1/0/0/0/35").Camera.Z, Is.EqualTo(30));
            // every one of the six on North Quay is inside that eye's 16:9 frame
            foreach (var d in Six)
            {
                var a = U5Plan.Map.Anchors[d.Token];
                Assert.That(CameraMath.Project(eye, Wide, a.X, Deck + 1, a.Z, out var nx, out var ny), Is.True, d.Token);
                Assert.That(Math.Max(Math.Abs(nx), Math.Abs(ny)), Is.LessThan(.95), d.Token);
            }
            foreach (var bad in new[] { null, "", "1/2/3/4/5/6", "1/2/3/4/5/6/50/8", "1/2/3/4/5/x/50", "1/2/3/NaN/5/6/50", "1/2/3/4/5/Infinity/50", "1/2/3/1/2/3/50", "1/2/3/4/5/6/4", "1/2/3/4/5/6/121", "1,5/2/3/4/5/6/50",
                // finite, but not a place a single-precision engine can stand or look from: out of reach, or eye and look a millimetre apart
                "1e40/0/0/0/0/0/50", "0/0/0/0/-100001/0/50", "1/2/3/1/2/3.001/50" })
                Assert.Throws<FormatException>(() => CameraMath.ParsePose(bad), bad ?? "(null)");
        }

        // ---------------------------------------------------------------- names

        [Test]
        public void TheSixHaveNamesOfTheirOwn()
        {
            Assert.That(HudModel.HumanName(Device(DeviceKind.MqttCharger, 1)), Is.EqualTo("MQTT charger N1"));
            Assert.That(HudModel.HumanName(Device(DeviceKind.CertCharger, 2)), Is.EqualTo("Cert charger N2"));
            Assert.That(HudModel.HumanName(Device(DeviceKind.PullLight, 2)), Is.EqualTo("Pull lamp N2"));
            Assert.That(HudModel.HumanName(U5Plan.Layout.Devices.First(d => d.Token == "hp-charger-north-1")), Is.EqualTo("Charger N1"));
            // no new name is an old one
            var old = U5Plan.Layout.Devices.Where(d => !New.Contains(d.Kind)).Select(HudModel.HumanName).ToArray();
            foreach (var d in Six) Assert.That(old, Does.Not.Contain(HudModel.HumanName(d)), d.Token);
            Assert.That(Six.Select(HudModel.HumanName).Distinct().Count(), Is.EqualTo(6));
            // a world tag's box is as wide as it was for every name the district had, and wider for these longer ones
            foreach (var d in U5Plan.Layout.Devices.Where(d => !New.Contains(d.Kind)))
                Assert.That(HudModel.TagWidth(HudModel.HumanName(d) + " · alarm"), Is.EqualTo(HudModel.TagBoxWidth), d.Token);
            foreach (var d in Six)
            {
                var text = HudModel.HumanName(d) + " · alarm";
                Assert.That(HudModel.TagWidth(text), Is.GreaterThan(HudModel.TagBoxWidth), d.Token);
                Assert.That(HudModel.TagWidth(text), Is.GreaterThanOrEqualTo(HudModel.TagPadding + HudModel.TagGlyphWidth * text.Length), d.Token);
            }
            Assert.That(HudModel.TagWidth(null), Is.EqualTo(HudModel.TagBoxWidth));
        }
    }
}
