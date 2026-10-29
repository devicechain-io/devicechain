// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

using System;
using System.IO;
using System.Linq;
using DeviceChain.Towerpulse.Model;
using DeviceChain.Towerpulse.Plane;
using DeviceChain.Towerpulse.Story;
using NUnit.Framework;

namespace DeviceChain.Towerpulse.Tests
{
    /// <summary>The campus layout the scene is built from: it must agree with the model's own layout, zone for zone and unit for unit.</summary>
    public class CampusPlanTests
    {
        static CampusPlan Plan()
        {
            var path = StoryTests.FindData("campus_layout.json");
            Assert.That(path, Is.Not.Null, "Assets/Towerpulse/Data/campus_layout.json must exist");
            return CampusPlan.Parse(File.ReadAllText(path));
        }

        [Test]
        public void EveryModelZoneHasAPlaceInTheScene()
        {
            var plan = Plan();
            var layout = BuildingLayout.Standard();
            Assert.That(plan.Zones.Count, Is.EqualTo(layout.Zones.Count));
            foreach (var z in layout.Zones)
            {
                var p = plan.Zone(z.Id);
                Assert.That(p, Is.Not.Null, z.Id);
                Assert.That(p.Building, Is.EqualTo(z.Building), z.Id);
                Assert.That(p.Floor, Is.EqualTo(z.Floor), z.Id);
                Assert.That(p.Node, Does.StartWith("Zone_F" + z.Floor), z.Id);
            }
            Assert.That(plan.Zones.Select(z => z.Node).Distinct().Count(), Is.EqualTo(plan.Zones.Count), "one node per zone");
        }

        [Test]
        public void EveryModelUnitHasAPlaceInTheScene()
        {
            var plan = Plan();
            var layout = BuildingLayout.Standard();
            foreach (var a in layout.Ahus) Assert.That(plan.RooftopUnits.Any(u => u.Id == a.Id), Is.True, a.Id);
            foreach (var c in layout.Chillers) Assert.That(plan.Chillers.Any(u => u.Id == c.Id), Is.True, c.Id);
            Assert.That(plan.Chillers.All(u => u.FanNodes.Count == 4 && u.LampNode == "StatusLamp"));
            Assert.That(plan.RooftopUnits.All(u => u.FanNodes.Count == 1 && u.Building.Length > 0));
        }

        [Test]
        public void TheMeetingRoomHasFewerSeatsThanPeopleSoSomeStand()
        {
            var z = Plan().Zone(BuildingLayout.HotZone);
            Assert.That(z.Seats.Count, Is.EqualTo(6));
            Assert.That(TowerDay.MeetingPeople, Is.GreaterThan(z.Seats.Count));
            var standing = CampusPlan.StandingSpots(z, TowerDay.MeetingPeople - z.Seats.Count);
            Assert.That(standing.Count, Is.EqualTo(8));
            Assert.That(standing.Select(s => Math.Round(s.X, 3) + "," + Math.Round(s.Z, 3)).Distinct().Count(), Is.EqualTo(8), "nobody stands on anybody");
        }

        [Test]
        public void ABrokenLayoutIsRefused()
        {
            Assert.Throws<FormatException>(() => CampusPlan.Parse("{}"));
            Assert.Throws<FormatException>(() => CampusPlan.Parse("[]"));
            Assert.Throws<MiniJson.ParseException>(() => CampusPlan.Parse("{"));
        }
    }
}
