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
    /// <summary>One generated plan for every U5 test that only reads it: the plan is seeded and immutable once built.</summary>
    static class U5Plan
    {
        static WorldPropPlan plan; static PresentationMap map; static HarborLayout layout;
        public static HarborLayout Layout => layout ?? (layout = Kit.Layout());
        public static PresentationMap Map => map ?? (map = new PresentationMap(Layout));
        public static WorldPropPlan Plan => plan ?? (plan = WorldPropPlan.Generate(Map, Layout));
    }

    public class CoastalSceneryTests
    {
        const double Deck = SitePlan.DeckHeightM;

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
        public void TerrainIsContinuousWithTheCoreAndHasNoCliffsOrHoles()
        {
            // pins: no step at the core edge (the quay deck meets the ground without a cliff) and no cliff anywhere on the grid
            for(var t=-240;t<=240;t+=10)
            {
                Assert.That(CoastalTerrain.Height(t,140.001)-Deck,Is.LessThan(.05),"north edge at x="+t);
                Assert.That(Math.Abs(CoastalTerrain.Height(t,141)-Deck),Is.LessThan(.2));
            }
            for(var z=-139;z<=140;z+=10)
            {
                Assert.That(CoastalTerrain.Height(240.001,z)-Deck,Is.LessThan(.05),"east edge at z="+z);
                Assert.That(CoastalTerrain.Height(-240.001,z)-Deck,Is.LessThan(.05));
            }
            var grid=TerrainGrid.Standard();
            var worst=0.0;
            for(var z=-140;z<2400;z+=10)for(var x=-3800;x<3800;x+=10)
            {
                var h=CoastalTerrain.Height(x,z);
                Assert.That(double.IsNaN(h)||double.IsInfinity(h),Is.False);
                Assert.That(h,Is.InRange(Deck,Deck+CoastalTerrain.MaxReliefM));
                worst=Math.Max(worst,Math.Abs(CoastalTerrain.Height(x+10,z)-h));
                worst=Math.Max(worst,Math.Abs(CoastalTerrain.Height(x,z+10)-h));
            }
            TestContext.WriteLine("Steepest 10 m step on the terrain: "+worst.ToString("F2")+" m");
            Assert.That(worst,Is.LessThan(2.6));
            // grid lines fall exactly on the core's edges, so the visual mesh has no gap or T-junction there
            foreach(var line in new[]{-240f,240f})Assert.That(grid.Xs.Contains(line),Is.True);
            foreach(var line in new[]{-140f,140f})Assert.That(grid.Zs.Contains(line),Is.True);
            Assert.That(grid.Xs.Last(),Is.EqualTo((float)CoastalTerrain.TerrainHalfWidthM),"flat far flanks reach past the far clip; no 4.8 km continuation");
            Assert.That(grid.Zs.Last(),Is.EqualTo((float)CoastalTerrain.TerrainReachM));
            Assert.That((grid.Xs.Count-1)*(grid.Zs.Count-1),Is.LessThan(40000),"terrain quads");
        }

        [Test]
        public void MeshHeightIsTheDrawnSurfaceAndMatchesTheFieldAtEveryVertex()
        {
            var grid=TerrainGrid.Standard();
            for(var j=0;j<grid.Zs.Count;j+=7)for(var i=0;i<grid.Xs.Count;i+=7)
                Assert.That(grid.MeshHeight(grid.Xs[i],grid.Zs[j]),Is.EqualTo(CoastalTerrain.Height(grid.Xs[i],grid.Zs[j])).Within(1e-9));
            // between vertices it is the two-triangle surface: the midpoint of a quad's shared diagonal is the mean of its ends
            double x0=grid.Xs[200],x1=grid.Xs[201],z0=grid.Zs[90],z1=grid.Zs[91];
            var diagonal=(CoastalTerrain.Height(x0,z0)+CoastalTerrain.Height(x1,z1))/2;
            Assert.That(grid.MeshHeight((x0+x1)/2,(z0+z1)/2),Is.EqualTo(diagonal).Within(1e-9));
        }

        [Test]
        public void TownApronRisesNoMoreThanTenMetresInTheFirstFourHundredOutsideTheCore()
        {
            // pins A1: the old ground rose 30 m here and filled the frame with hill
            for(var z=-139;z<2400;z+=9)for(var x=-2400;x<2400;x+=11)
                if(CoastalTerrain.CoreDistance(x,z)<=400)
                    Assert.That(CoastalTerrain.Height(x,z)-Deck,Is.LessThan(10.01),"at "+x+","+z);
        }

        [Test]
        public void NothingWithinThirteenHundredMetresStandsMoreThanThirtyFourMetresAboveTheDeck()
        {
            var highest=0.0;
            for(var z=-139;z<=3000;z+=7)for(var x=-3000;x<=3000;x+=7)
                if(CoastalTerrain.CoreDistance(x,z)<=1300)highest=Math.Max(highest,CoastalTerrain.Height(x,z)-Deck);
            TestContext.WriteLine("Highest ground within 1300 m of the core: "+highest.ToString("F1")+" m above the deck");
            Assert.That(highest,Is.LessThanOrEqualTo(34));
            Assert.That(highest,Is.GreaterThan(14),"the knolls still stand (6-16 m of relief above the apron)");
        }

        /// <summary>The steepest elevation angle, degrees, at which bare ground rises above the horizontal from an eye, over a fan of azimuths.</summary>
        static double Skyline(double eyeX,double eyeZ,double centreAzimuthDegrees,double halfFanDegrees)
        {
            var eye=Deck+6;var best=-90.0;
            for(var a=-halfFanDegrees;a<=halfFanDegrees;a+=1)
            {
                var az=(centreAzimuthDegrees+a)*Math.PI/180;
                for(var d=8.0;d<=2400;d+=8)
                {
                    var x=eyeX+Math.Sin(az)*d;var z=eyeZ+Math.Cos(az)*d;
                    best=Math.Max(best,Math.Atan2(CoastalTerrain.Height(x,z)-eye,d)*180/Math.PI);
                }
            }
            return best;
        }

        [Test]
        public void BareGroundSkylineIsLowFromTheQuays()
        {
            // pins A1: from 6 m above North Quay looking north (+-35 degrees) the bare ground is within 2.5 degrees of the horizontal
            var north=0.0;
            foreach(var x in new[]{-110.0,-60,0,60,100})north=Math.Max(north,Skyline(x,100,0,35));
            // ...and within 3 degrees from the East Quay and Basin eyes looking east and west
            var east=Math.Max(Skyline(150,0,90,35),Math.Max(Skyline(160,-40,90,35),Skyline(150,40,90,35)));
            var west=Math.Max(Skyline(-190,0,-90,35),Math.Max(Skyline(-190,-60,-90,35),Skyline(-190,60,-90,35)));
            TestContext.WriteLine("Bare-ground skyline: north "+north.ToString("F2")+" deg, east "+east.ToString("F2")+", west "+west.ToString("F2"));
            Assert.That(north,Is.LessThanOrEqualTo(2.5));
            Assert.That(east,Is.LessThanOrEqualTo(3.0));
            Assert.That(west,Is.LessThanOrEqualTo(3.0));
            Assert.That(north,Is.GreaterThan(.5),"the land still has some shape");
        }

        [Test]
        public void ADistantRidgeCarriesTheSkylineWithSeveralDistinctHills()
        {
            // pins A1: one hazy ridge 2400-3000 m out, the highest crest about 2.6 degrees above the horizontal from the harbour,
            // lower shoulders 1-1.8 degrees, nothing at the two ends
            var eye=Deck+6;
            double Angle(double x)=>Math.Atan2(CoastalTerrain.RidgeHeight(x,2760)-eye,2760-100)*180/Math.PI;
            var peak=-90.0;var angles=new List<double>();
            for(var x=-2400.0;x<=2400;x+=50){var a=Angle(x);angles.Add(a);peak=Math.Max(peak,a);}
            TestContext.WriteLine("Ridge crest: highest "+peak.ToString("F2")+" deg");
            Assert.That(peak,Is.InRange(2.4,2.8));
            var summits=new List<double>();
            for(var i=2;i<angles.Count-2;i++)
                if(angles[i]>angles[i-1]&&angles[i]>=angles[i+1]&&angles[i]>angles[i-2]&&angles[i]>=angles[i+2]&&angles[i]>.6)summits.Add(angles[i]);
            Assert.That(summits.Count,Is.InRange(2,5),"two or three overlapping hills (plus a low far shoulder)");
            Assert.That(summits.Count(a=>a>=.9&&a<=1.9),Is.GreaterThanOrEqualTo(1),"a lower shoulder of 1-1.8 degrees");
            Assert.That(summits.Distinct().Count(),Is.EqualTo(summits.Count),"distinct profiles");
            Assert.That(CoastalTerrain.RidgeHeight(-2400,2760)-Deck,Is.LessThan(.5));
            Assert.That(CoastalTerrain.RidgeHeight(2400,2760)-Deck,Is.LessThan(.5),"open sky and sea horizon beside it");
            // inside the viewing range: from the HOME eye it stays clear of the 3,600 m far clip
            Assert.That(Math.Sqrt(3050.0*3050+420*420+130*130),Is.LessThan(3600));
            Assert.That(CoastalTerrain.RidgeRowZ[0],Is.GreaterThanOrEqualTo(2300));
            Assert.That(CoastalTerrain.RidgeRowZ.Last(),Is.LessThanOrEqualTo(3100));
            var columns=(int)(2*CoastalTerrain.RidgeHalfWidth/CoastalTerrain.RidgeColumnStep);
            Assert.That(columns*(CoastalTerrain.RidgeRowZ.Length-1)*2,Is.LessThan(700),"a few hundred triangles");
        }

        [Test]
        public void WoodlandCoversAboutSeventyPercentOfTheOpenLandInBroadConnectedStands()
        {
            var woods=0;var land=0;var neighbours=0;
            for(var z=-80;z<1300;z+=15)for(var x=-1500;x<1500;x+=15)
            {
                var d=CoastalTerrain.CoreDistance(x,z);
                if(d<250||d>1100)continue;
                land++;
                if(CoastalTerrain.Woodland(x,z)){woods++;if(CoastalTerrain.Woodland(x+15,z))neighbours++;}
            }
            var cover=woods/(double)land;
            TestContext.WriteLine("Woodland covers "+cover.ToString("P1")+" of the land 250-1100 m out");
            Assert.That(cover,Is.InRange(.65,.75));
            Assert.That(neighbours/(double)woods,Is.GreaterThan(.8),"stands are connected, not scattered");
            // clearings: several open patches of 60-200 m, some touching the town edge
            Assert.That(CoastalTerrain.Clearing(-80,1060),Is.True,"a clearing on a knoll");
            Assert.That(CoastalTerrain.ClearingWeight(-250,470),Is.GreaterThan(.9));
            Assert.That(CoastalTerrain.ClearingWeight(330,440),Is.GreaterThan(.9));
            Assert.That(CoastalTerrain.ForestCover(0,2000),Is.InRange(0,1));
        }

        [Test]
        public void ThinningRetainsSixtyPercentOfHouses()
        {
            var houses=0;
            for(var i=0;i<1000;i++)if(CoastalTerrain.KeepHouse(i))houses++;
            Assert.That(houses,Is.EqualTo(600));
        }

        [TestCase(12.5,0)]
        [TestCase(17.5,1)]
        [TestCase(23,0)]
        [TestCase(23,1)]
        public void FogProtectsTheTownAndDissolvesTheFarWorld(double hour,double storm)
        {
            var s=DayLighting.Evaluate(hour,new Weather(storm,storm));
            Assert.That(s.FogColor,Is.EqualTo(s.Horizon),"fog follows the actual day, night and storm horizon");
            Assert.That(s.SceneryTransmission(600,110,20,80),Is.EqualTo(1),"inside the town ramp: crisp");
            Assert.That(s.SceneryTransmission(900,300,15,100),Is.EqualTo(1),"portrait foreground stays crisp");
            Assert.That(s.SceneryTransmission(700,100,10,140),Is.GreaterThan(.95),"the town's edge is barely touched");
            Assert.That(s.SceneryTransmission(1500,100,30,1000),Is.LessThan(.35));
            Assert.That(s.SceneryTransmission(2800,100,5,2000),Is.LessThan(.05));
            var low=s.SceneryTransmission(1000,100,10,800);
            var high=s.SceneryTransmission(1000,100,170,800);
            Assert.That(low,Is.LessThanOrEqualTo(high),"height fog pools below the upper slopes");
            Assert.That(high,Is.InRange(0,1));
        }

        [Test]
        public void ViewDistanceCloses_HalfFoggedAt900mAndNinetyPercentBy1500mAtNoon_HalfAt550mInTheStorm()
        {
            // pins A2: far from the core (full ramp) at the HOME-like camera height over mid ground
            var noon=DayLighting.Evaluate(12.5,new Weather(0,0));
            var storm=DayLighting.Evaluate(12.5,new Weather(1,1));
            var at900=noon.SceneryTransmission(900,100,25,800);
            var at1500=noon.SceneryTransmission(1500,100,25,1200);
            TestContext.WriteLine("Noon scenery transmission: 900 m "+at900.ToString("F3")+", 1500 m "+at1500.ToString("F3")+"; storm 550 m "+storm.SceneryTransmission(550,100,25,500).ToString("F3"));
            Assert.That(at900,Is.InRange(.40,.60));
            Assert.That(at1500,Is.InRange(.07,.16));
            Assert.That(storm.SceneryTransmission(550,100,25,500),Is.InRange(.40,.60));
            Assert.That(storm.SceneryTransmission(900,100,25,800),Is.LessThan(.25),"the storm closes the view further");
            // ground-level eye (8 m): the same curve
            Assert.That(noon.SceneryTransmission(900,8,20,800),Is.InRange(.35,.60));
        }

        [Test]
        public void TheRidgeIsReadableAtNoonAndNearlyVanishesInTheStorm()
        {
            var noon=DayLighting.Evaluate(12.5,new Weather(0,0));
            var storm=DayLighting.Evaluate(12.5,new Weather(1,1));
            foreach(var d in new[]{2800.0,3200,3500})
            {
                Assert.That(noon.RidgeTransmission(d),Is.InRange(.20,.70),"a pale shape at "+d+" m");
                Assert.That(noon.RidgeTransmission(d),Is.GreaterThan(noon.SceneryTransmission(d,100,40,2500)*20),"lighter fog than the shared curve");
                Assert.That(storm.RidgeTransmission(d),Is.LessThan(.2),"nearly gone in the storm");
            }
            Assert.That(storm.RidgeTransmission(3200),Is.LessThan(noon.RidgeTransmission(3200)));
        }

        [Test]
        public void NoonHazeIsCoolAndMatchesTheHorizon()
        {
            var s=DayLighting.Evaluate(12.5,new Weather(0,0));
            Assert.That(s.FogColor.B,Is.GreaterThan(s.FogColor.G));
            Assert.That(s.FogColor.G,Is.GreaterThan(s.FogColor.R));
        }

        [Test]
        public void ShaderFogGainMatchesTheStrengthTheLitMaterialsKeep()
        {
            // The planar and URP fog on every other material read FogDensity itself: it must stay small, so the town and the water are not hazed.
            var noon=DayLighting.Evaluate(12.5,new Weather(0,0));
            Assert.That(noon.FogDensity,Is.LessThan(.0003));
            Assert.That(Math.Exp(-Math.Pow(noon.FogDensity*700,2)),Is.GreaterThan(.97),"the lit town at 700 m under the URP fog");
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
    }
}
