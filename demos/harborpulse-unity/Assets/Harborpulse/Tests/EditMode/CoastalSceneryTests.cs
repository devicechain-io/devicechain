// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

using System;
using DeviceChain.Harborpulse.Model;
using NUnit.Framework;

namespace DeviceChain.Harborpulse.Tests
{
    public class CoastalSceneryTests
    {
        [Test]
        public void MappedCoreAndCoastStayAtDeckHeight()
        {
            for(var x=-240;x<=240;x+=20)
                for(var z=-300;z<=140;z+=20)
                    Assert.That(CoastalTerrain.Height(x,z),Is.EqualTo(SitePlan.DeckHeightM));
            for(var x=-3400;x<=3400;x+=100)
                Assert.That(CoastalTerrain.Height(x,-140),Is.EqualTo(SitePlan.DeckHeightM));
        }

        [Test]
        public void SummitHasShouldersAndASaddleBelowTheHomeUpperRay()
        {
            var summit=CoastalTerrain.Height(-90,1050);
            Assert.That(summit,Is.GreaterThan(CoastalTerrain.Height(-340,980)+25));
            Assert.That(summit,Is.GreaterThan(CoastalTerrain.Height(650,700)+16));
            var upperAngle=Math.Atan2(-110,460)+15*Math.PI/180;
            var upperRay=110+(1050+420)*Math.Tan(upperAngle);
            Assert.That(summit,Is.LessThan(upperRay-5),"sky above the rounded summit from the fixed HOME camera");
            // Sample the whole north-facing skyline: no far rising wall may swallow that upper ray.
            for(var z=600;z<=3400;z+=50)
                for(var x=-1200;x<=1200;x+=50)
                    Assert.That(CoastalTerrain.Height(x,z),Is.LessThan(110+(z+420)*Math.Tan(upperAngle)));
            Assert.That(CoastalTerrain.Clearing(-90,1050),Is.True,"summit ledges remain exposed");
            Assert.That(CoastalTerrain.Clearing(-300,650),Is.True,"a meadow breaks the forest behind town");
        }

        [Test]
        public void LandformIsFiniteRepeatableAndKeepsTheTownSlope()
        {
            for(var z=-140;z<=3400;z+=71)
                for(var x=-3400;x<=3400;x+=79)
                {
                    var h=CoastalTerrain.Height(x,z);
                    Assert.That(double.IsNaN(h)||double.IsInfinity(h),Is.False);
                    Assert.That(h,Is.InRange(SitePlan.DeckHeightM,150));
                    Assert.That(CoastalTerrain.Height(x,z),Is.EqualTo(h));
                }
            foreach(var z in new[]{160,200,280,400})
                Assert.That(CoastalTerrain.Height(0,z),Is.EqualTo(SitePlan.DeckHeightM+30*CoastalTerrain.Smooth(0,400,z-140)).Within(1e-9));
        }

        [Test]
        public void ThinningRetainsSixtyPercentOfHousesAndAtMostTwentyPercentOfTrees()
        {
            var houses=0;var trees=0;
            for(var i=0;i<1000;i++)
            {
                if(CoastalTerrain.KeepHouse(i))houses++;
                if(CoastalTerrain.KeepTree(i))trees++;
            }
            Assert.That(houses,Is.EqualTo(600));
            Assert.That(trees,Is.EqualTo(200));
        }

        [TestCase(12.5,0)]
        [TestCase(17.5,1)]
        [TestCase(23,0)]
        [TestCase(23,1)]
        public void FogProtectsTheTownAndDissolvesTheFarForest(double hour,double storm)
        {
            var s=DayLighting.Evaluate(hour,new Weather(storm,storm));
            Assert.That(s.FogColor,Is.EqualTo(s.Horizon),"fog follows the actual day, night and storm horizon");
            Assert.That(s.SceneryTransmission(600,110,20,80),Is.EqualTo(1));
            Assert.That(s.SceneryTransmission(900,300,15,140),Is.GreaterThan(.96),"portrait foreground stays crisp");
            Assert.That(s.SceneryTransmission(1500,110,80,1000),Is.GreaterThan(.99),"near and middle hillside stay clear");
            Assert.That(s.SceneryTransmission(2300,110,90,1900),Is.InRange(.2,.8),"far woodland retains form through haze");
            Assert.That(s.SceneryTransmission(3800,110,40,3000),Is.LessThan(.4));
            var low=s.SceneryTransmission(2100,110,40,1700);
            var high=s.SceneryTransmission(2100,110,170,1700);
            Assert.That(low,Is.LessThan(high),"height fog pools below the upper slopes");
            Assert.That(high,Is.InRange(0,1));
        }


        [Test]
        public void NoonHazeIsCoolAndMatchesTheHorizon()
        {
            var s=DayLighting.Evaluate(12.5,new Weather(0,0));
            Assert.That(s.FogColor.B,Is.GreaterThan(s.FogColor.G));
            Assert.That(s.FogColor.G,Is.GreaterThan(s.FogColor.R));
        }

        [Test]
        public void FixedLandscapeHomeHasSkyAboveTheTerrainAndExposedSummit()
        {
            const int columns=100;
            var top=new double[columns];
            for(var i=0;i<columns;i++)top[i]=-1;
            var pitch=Math.Atan2(-110,460);var fy=Math.Sin(pitch);var fz=Math.Cos(pitch);
            var tan=Math.Tan(15*Math.PI/180);
            void Project(double x,double y,double z)
            {
                var depth=(z+420)*fz+(y-110)*fy;
                var nx=x/(depth*tan*16/9);
                if(nx<=-1||nx>=1)return;
                var col=(int)((nx+1)*.5*columns);
                var ny=((y-110)*fz-(z+420)*fy)/(depth*tan);
                top[col]=Math.Max(top[col],ny);
            }
            for(var z=500;z<=4800;z+=10)
                for(var x=-4800;x<=4800;x+=10)Project(x,CoastalTerrain.Height(x,z),z);
            var sky=0.0;foreach(var y in top)sky+=(1-y)*.5/columns;
            TestContext.WriteLine("Landscape terrain sky estimate: "+sky.ToString("P5"));
            Assert.That(sky,Is.InRange(.08,.16),"analytic terrain silhouette; actual HOME masks still require Unity");
            var summit=CoastalTerrain.Height(-90,1050);
            Assert.That(summit,Is.GreaterThan(CoastalTerrain.Height(-90,790)+25));
            Assert.That(summit,Is.GreaterThan(CoastalTerrain.Height(-90,1310)+25));
            // The summit must make a visible central bulge, rather than merely being high in WORLD metres.
            var center=0.0;for(var i=30;i<60;i++)center+=top[i]/30;
            var sides=(top[5]+top[95])*.5;
            Assert.That(center-sides,Is.GreaterThan(.025),"central summit silhouette has at least 13.5 px mean prominence at 1080p");
        }

        [Test]
        public void WoodlandHasConnectedStandsAndBroadGaps()
        {
            var trees=0;var neighbors=0;var gaps=0;
            for(var z=500;z<1600;z+=20)for(var x=-1000;x<1000;x+=20)
            {
                if(CoastalTerrain.Woodland(x,z))
                {trees++;if(CoastalTerrain.Woodland(x+20,z))neighbors++;}
                else gaps++;
            }
            Assert.That(neighbors/(double)trees,Is.GreaterThan(.7));
            Assert.That(gaps,Is.GreaterThan(trees));
            Assert.That(CoastalTerrain.Woodland(160,1080),Is.False);
        }

        [TestCase(0,-420)]
        [TestCase(-560,-60)]
        [TestCase(400,-2000)]
        [TestCase(300,2000)]
        public void FarWaterFollowsTheCameraButCannotCoverInlandSky(double cx,double cz)
        {
            var patches=new OceanPatch[OceanRing.PatchCount];
            OceanRing.Fill(cx,cz,720,9000,patches);
            for(var z=-9000.5;z<9000;z+=241)for(var x=-9000.5;x<9000;x+=239)
            {
                var inRing=Math.Abs(x-cx)<9000 && Math.Abs(z-cz)<9000 && (Math.Abs(x-cx)>720 || Math.Abs(z-cz)>720);
                var water=z<CoastalTerrain.CoastZ || (Math.Abs(x)<CoastalTerrain.CoreHalfX && z<CoastalTerrain.CoreNorthZ);
                var hits=0;
                foreach(var p in patches)if(!p.Empty && x>p.MinX && x<p.MaxX && z>p.MinZ && z<p.MaxZ)hits++;
                Assert.That(hits,Is.EqualTo(inRing&&water?1:0),"ring coverage at "+x+","+z);
            }
            var again=new OceanPatch[OceanRing.PatchCount];OceanRing.Fill(cx,cz,720,9000,again);
            Assert.That(again,Is.EqualTo(patches));
        }

        [Test]
        public void WoodlandStopsBeforeTheSummitAndIncludesTheWholeCrown()
        {
            var plan=WorldPropPlan.Generate(new Model.Director.PresentationMap(Kit.Layout()),Kit.Layout());
            var trees=0;var rocks=0;
            foreach(var tree in plan.Props)
            {
                if(tree.Kind=="Tree")trees++;
                if(tree.Kind=="Rock")rocks++;
                if(tree.Kind=="Tree" && tree.Area=="forest")
                    Assert.That(CoastalTerrain.ForestFits(tree.At.X,tree.At.Z,tree.Height),Is.True);
            }
            Assert.That(trees,Is.LessThanOrEqualTo(806),"round-2 tree count is the placement budget");
            TestContext.WriteLine("Round-3 plan: "+trees+" trees, "+rocks+" granite clusters");
            Assert.That(CoastalTerrain.ForestFits(0,990,30),Is.False);
            Assert.That(CoastalTerrain.ForestFits(0,1800,20),Is.False);
        }

        [Test]
        public void StormThickensBothFogTerms()
        {
            var day=DayLighting.Evaluate(12.5,new Weather(0,0));
            var storm=DayLighting.Evaluate(12.5,new Weather(1,1));
            Assert.That(storm.HeightFogDensity,Is.GreaterThan(day.HeightFogDensity));
            Assert.That(storm.SceneryTransmission(2300,110,120,1900),Is.LessThan(day.SceneryTransmission(2300,110,120,1900)));
        }
    }
}
