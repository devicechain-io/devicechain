// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

using System;
using System.Linq;
using DeviceChain.Harborpulse.Model;
using DeviceChain.Harborpulse.Model.Director;
using NUnit.Framework;

namespace DeviceChain.Harborpulse.Tests
{
    public class WorldPropPlanTests
    {
        [Test]
        public void GeneratedWorldHasZeroFootprintOverlapsAndKeepsEveryDevice()
        {
            var layout=Kit.Layout();var map=new PresentationMap(layout);
            var plan=WorldPropPlan.Generate(map,layout);
            Assert.That(FootprintRegistry.FindOverlaps(plan.Registry.Entries),Is.Empty);
            foreach(var d in layout.Devices)
                Assert.That(plan.Registry.Entries.Any(p=>p.Id=="device:"+d.Token),Is.True,d.Token);
            foreach(var d in layout.Devices.Where(d=>d.Kind==DeviceKind.Charger))
                Assert.That(plan.Find("car:"+d.Token),Is.Not.Null);
            foreach(var kind in new[]{"House","Commercial","Church","Shed","Tree","Rock","Bench","Gazebo","StreetLamp","Bollard","Cleat","ParkedCar","ChargingCar","Depot"})
                Assert.That(plan.Props.Any(p=>p.Kind==kind),Is.True,"missing class "+kind);
            Assert.That(plan.Props.Count(p=>p.Kind=="Tree"),Is.GreaterThan(5000),"a wooded coast, far more than the 444 of the U4 plan");
            foreach(var tree in plan.Props.Where(p=>p.Kind=="Tree" && p.Area=="forest"))
                Assert.That(CoastalTerrain.Woodland(tree.At.X,tree.At.Z),Is.True,"woodland leaves meadow gaps");
            Assert.That(plan.Props.Count(p=>p.Kind=="Rock"),Is.GreaterThanOrEqualTo(20),"granite outcrops in clusters");
            Assert.That(plan.Props.Any(p=>p.Kind=="Rock" && p.At.Z<-30 && Math.Abs(p.At.X)>300),Is.True,"shore ledges outside the quays");
            Assert.That(plan.Props.Count(p=>p.Area=="town" && p.Kind!="Tree"),Is.GreaterThan(50));
        }

        [Test]
        public void PlantedCarLampOverlapFailsTheIndependentAuditAndPlacementRefusesIt()
        {
            var layout=Kit.Layout();var map=new PresentationMap(layout);var plan=WorldPropPlan.Generate(map,layout);
            var lamp=layout.Devices.Last(d=>d.Kind==DeviceKind.Light && map.QuayFor(d)=="hp-east-quay");
            var planted=new PropFootprint("planted-car",map.Anchors[lamp.Token],WorldPropPlan.CarWidth,WorldPropPlan.CarDepth);
            Assert.That(FootprintRegistry.FindOverlaps(plan.Registry.Entries.Concat(new[]{planted}).ToArray()),Is.Not.Empty);
            Assert.That(plan.Registry.TryAdd(planted),Is.False);
            // The original car was 4 m toward the water from the charger: it hit East lamp 4.
            Assert.That(plan.Props.Where(p=>p.Kind=="ChargingCar").Any(p=>
                SceneryStreet.Distance(p.At,new PropFootprint("frame",map.Anchors[p.Area],1,1,p.Yaw).Local(0,4))>.5),Is.True);
        }

        [Test]
        public void AttachedWellClearsNorthLampTwoWithoutMovingEitherAnchor()
        {
            var layout=Kit.Layout();var map=new PresentationMap(layout);var plan=WorldPropPlan.Generate(map,layout);
            var pump=layout.Devices.Single(d=>d.Kind==DeviceKind.Pump && map.QuayFor(d)=="hp-north-quay");
            var lamp=plan.Registry.Entries.Single(p=>p.Id=="device:hp-light-north-2");
            var frame=new PropFootprint("pump",map.Anchors[pump.Token],1,1,180);
            Assert.That(new PropFootprint("old-well",frame.Local(9,6),5.6,5.6,180).Intersects(lamp),Is.True);
            Assert.That(plan.Registry.Entries.Single(p=>p.Id=="device:"+pump.Token+":well").Intersects(lamp),Is.False);
        }

        [Test]
        public void WorldPlanIsRepeatableAndAnchorsAreUnchanged()
        {
            var layout=Kit.Layout();var map=new PresentationMap(layout);var anchors=map.Anchors.ToDictionary(p=>p.Key,p=>p.Value);
            var a=WorldPropPlan.Generate(map,layout);var b=WorldPropPlan.Generate(map,layout);
            Assert.That(a.Registry.Entries.Select(p=>(p.Id,p.At.X,p.At.Z,p.Width,p.Depth,p.Yaw)),
                Is.EqualTo(b.Registry.Entries.Select(p=>(p.Id,p.At.X,p.At.Z,p.Width,p.Depth,p.Yaw))));
            foreach(var p in anchors){Assert.That(map.Anchors[p.Key].X,Is.EqualTo(p.Value.X));Assert.That(map.Anchors[p.Key].Z,Is.EqualTo(p.Value.Z));}
        }

        [Test]
        public void RotatedCrossingAndContainedRectanglesCannotEvadeClearance()
        {
            var a=new PropFootprint("a",new Ground(0,0),10,1,45);
            Assert.That(a.Intersects(new PropFootprint("cross",new Ground(0,0),10,1,-45)),Is.True);
            Assert.That(a.Intersects(new PropFootprint("inside",new Ground(0,0),.1,.1)),Is.True);
            Assert.That(a.Intersects(new PropFootprint("far",new Ground(20,0),1,1)),Is.False);
            var registry=new FootprintRegistry();registry.AddFixed(a);
            Assert.Throws<InvalidOperationException>(()=>registry.AddFixed(new PropFootprint("b",new Ground(0,0),1,1)));
            Assert.Throws<ArgumentException>(()=>new PropFootprint("bad",new Ground(double.NaN,0),1,1));
        }
    }
}
