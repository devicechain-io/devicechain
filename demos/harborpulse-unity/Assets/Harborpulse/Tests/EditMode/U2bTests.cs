// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

using System;
using System.Collections.Generic;
using System.IO;
using System.Linq;
using System.Text;
using DeviceChain.Harborpulse.Model;
using DeviceChain.Harborpulse.Model.Director;
using NUnit.Framework;

namespace DeviceChain.Harborpulse.Tests
{
    public class U2bTests
    {
        static TideField.Region[] Regions() => new PresentationMap(Kit.Layout()).Quays.Values.Select(p => new TideField.Region(p)).ToArray();
        [Test] public void TideFieldPinsQuayInteriorsExactly()
        {
            var regions = Regions(); var field = new TideField(regions); var levels = new[] { 1.0, 1.15, 1.3 };
            for (var i = 0; i < regions.Length; i++)
            {
                var r = regions[i];
                foreach (var p in new[] { new Ground(r.MinX,r.MinZ), new Ground(r.MaxX,r.MaxZ), new Ground((r.MinX+r.MaxX)/2,(r.MinZ+r.MaxZ)/2) })
                    Assert.That(field.LevelAt(p.X,p.Z,levels), Is.EqualTo(levels[i]));
            }
        }
        [Test] public void TideFieldHalfMetreGridHasNoCentimetreSteps()
        {
            var f = new TideField(Regions()); var levels = new[] { 1.0, 1.15, 1.3 };
            // 0.30 m regional spread; an arbitrary level difference cannot have a universal
            // 1 cm gradient bound between disjoint pinned quays (mean value theorem).
            for (double z=-180;z<=150;z+=.5) for(double x=-250;x<=250;x+=.5)
            {
                var h=f.LevelAt(x,z,levels);
                Assert.That(Math.Abs(f.LevelAt(x+.5,z,levels)-h),Is.LessThanOrEqualTo(.01));
                Assert.That(Math.Abs(f.LevelAt(x,z+.5,levels)-h),Is.LessThanOrEqualTo(.01));
            }
        }
        [Test] public void TideFieldIsBoundedAndReorderingSymmetric()
        {
            var r=Regions(); var levels=new[]{-.3,2.1,1.3};var a=new TideField(r);var b=new TideField(r.Reverse());
            for(var z=-250;z<=200;z+=5)for(var x=-300;x<=300;x+=5)
            {
                var h=a.LevelAt(x,z,levels);Assert.That(h,Is.InRange(-.3000000001,2.1000000001));
                Assert.That(b.LevelAt(x,z,levels.Reverse().ToArray()),Is.EqualTo(h).Within(1e-12));
            }
        }
        [Test] public void TideFieldMissingUsesDatumAndHeldHeightIsIdentity()
        {
            var missing=PresentationMap.WaterHeight(null);Assert.That(missing.GeometryM,Is.Zero);Assert.That(missing.Label,Is.EqualTo("no reading"));
            var held=PresentationMap.ReadingHeight(null,DateTimeOffset.UtcNow,TimeSpan.FromSeconds(5),2.2);
            Assert.That(held.GeometryM,Is.EqualTo(2.2));Assert.That(held.Label,Is.EqualTo("held"));
            var r=Regions()[0];var f=new TideField(new[]{r});Assert.That(f.LevelAt(r.MinX,r.MinZ,new[]{held.GeometryM}),Is.EqualTo(2.2));
        }
        [Test] public void LayoutDigestNormalizesCrLfToLf()
        { Assert.That(LayoutDigest.Compute(Encoding.UTF8.GetBytes("a\r\nb\r\n")),Is.EqualTo(LayoutDigest.Compute(Encoding.UTF8.GetBytes("a\nb\n")))); }
        [Test] public void AbsentRunnerDigestIsMismatchWithReason()
        { Assert.That(LayoutDigest.Matches("abc",null,out var reason),Is.False);Assert.That(reason,Is.EqualTo("runner reports no layout digest")); }
        [Test] public void DigestOverrideIsExplicitHexControl()
        { Assert.That(LayoutDigest.Override("local",new string('A',64)),Is.EqualTo(new string('a',64)));Assert.Throws<FormatException>(()=>LayoutDigest.Override("local","bad")); }
        [Test] public void BannerPrecedenceDigestThenStaleThenControl()
        {
            Assert.That(HudModel.Banner(false,true,new[]{"defect"}),Is.EqualTo("layout mismatch: director off"));
            Assert.That(HudModel.Banner(true,true,new[]{"defect"}),Is.EqualTo("reconnecting"));
            Assert.That(HudModel.Banner(true,false,new[]{"defect"}),Is.EqualTo("TEST CONTROL: defect"));
            Assert.That(HudModel.Banner(true,false,Array.Empty<string>()),Is.Empty);
        }
        [Test] public void HudCaptionCannotEndBeforeThreeSeconds()
        {
            var h=new HudModel();h.SetCaption("A","claim","1",0);h.SetCaption("","","1",1);h.Tick(2.99);
            Assert.That(h.Caption,Is.EqualTo("A"));h.Tick(3);Assert.That(h.Caption,Is.Empty);
            h.SetCaption("B","pre","2",4);h.SetCaption("C","claim","2",5);h.Tick(6.99);Assert.That(h.Caption,Is.EqualTo("B"));
            h.Tick(7);Assert.That(h.Caption,Is.EqualTo("C"));Assert.That(h.CaptionAtS,Is.EqualTo(7));
        }
        [Test] public void HudRailCapsFiveNewestAtBottomWithDeclaredLinksOnly()
        {
            var h=new HudModel();for(var i=0;i<7;i++)h.AddNode(i.ToString(),"label",EvidenceSource.Platform,i*4);
            Assert.That(h.Rail.Select(n=>n.Id),Is.EqualTo(new[]{"2","3","4","5","6"}));
            Assert.That(h.Rail.Last().SincePreviousS,Is.EqualTo(4));Assert.That(h.Links,Is.Empty);
            h.AddLink("5->6");Assert.That(h.Links,Does.Contain("5->6"));
        }
        [TestCase("E")][TestCase("D")][TestCase("H")][TestCase("Space")]
        public void ReservedKeysNeverTakeCamera(string key)
        { var f=new FreeRoam();f.Input(0,key);Assert.That(f.DirectorWeight(0),Is.EqualTo(1)); }
        [Test] public void NavigationTakesImmediatelyAndReturnsWithinThreeSecondsAfterTwentyIdle()
        { var f=new FreeRoam();f.Input(10,"W");Assert.That(f.DirectorWeight(10),Is.Zero);Assert.That(f.DirectorWeight(30),Is.Zero);Assert.That(f.DirectorWeight(31.5),Is.EqualTo(.5));Assert.That(f.DirectorWeight(33),Is.EqualTo(1)); }
        sealed class TimelineFeed : IDirectorFeed
        {
            readonly ObservationStore clean=DirectorTimeline.Clean();
            public IReadOnlyList<string> Problems => Array.Empty<string>();
            public void Update(DirectorInputs input,Action<Observation> observe,Action<CoverageInterval> cover)
            {
                var wall=(input.Now-input.RecordingEpoch).TotalSeconds;
                foreach(var o in clean.Records.Where(o=>o.ObservedAtS<=wall))observe(o);
                foreach(var stream in new[]{"alarms","notifications","credentials","pump","measurements"})cover(new CoverageInterval("run",stream,6,input.Sim,false));
            }
        }
        [Test] public void HostMissingFeedIsLoudAndMismatchLeavesFreeRoam()
        {
            var l=Kit.Layout();var host=new DirectorHost(DirectorKit.Story(),l,new PresentationMap(l));
            var v=host.Tick(new DirectorSample("run",6,0,digestMatches:false),new DirectorInputs());
            Assert.That(v.Problem,Is.EqualTo("no observer feed"));Assert.That(v.Banner,Is.EqualTo("layout mismatch: director off"));Assert.That(v.DirectorWeight,Is.Zero);
        }
        sealed class BrokenFeed : IDirectorFeed
        {
            public IReadOnlyList<string> Problems => Array.Empty<string>();
            public void Update(DirectorInputs input, Action<Observation> observe, Action<CoverageInterval> cover) => throw new FormatException("probe record malformed");
        }
        [Test] public void HostFeedFailureIsLoudWithoutInventingObservations()
        {
            var l=Kit.Layout();var host=new DirectorHost(DirectorKit.Story(),l,new PresentationMap(l),new BrokenFeed());
            var view=host.Tick(new DirectorSample("run",6,0),new DirectorInputs());
            Assert.That(view.Problem,Is.EqualTo("observer feed: probe record malformed"));Assert.That(host.Store.Records,Is.Empty);
        }
        [Test] public void HostFollowUsesTriggerDeviceAnchor()
        {
            var l=Kit.Layout();var map=new PresentationMap(l);var host=new DirectorHost(DirectorKit.Story(),l,map,new TimelineFeed());
            var epoch=DateTimeOffset.UtcNow;var seen=false;
            for(var t=0;t<1200;t++)
            {
                var sim=6+t/60.0;var v=host.Tick(new DirectorSample("run",sim,t),new DirectorInputs{Run="run",RecordingEpoch=epoch,Now=epoch.AddSeconds(t),Sim=sim});
                if(v.FollowDevice==null)continue;
                var anchor=map.Anchors[v.FollowDevice];Assert.That(v.FollowAnchor.Value.X,Is.EqualTo(anchor.X));Assert.That(v.FollowAnchor.Value.Z,Is.EqualTo(anchor.Z));seen=true;
            }
            Assert.That(seen,Is.True);
        }
        [Test] public void CleanSyntheticLogExportsRecorderFormatAndPassesCaptionChecker()
        {
            var scheduler=DirectorTimeline.Run();var path=Path.Combine(Path.GetTempPath(),"harbor-director-"+Guid.NewGuid().ToString("N")+".ndjson");
            try
            {
                using(var output=File.CreateText(path))
                {
                    var recorder=new Recorder(TextWriter.Null,TextWriter.Null,output,TextWriter.Null);
                    foreach(var e in scheduler.Log)recorder.Decision(e);
                }
                using(var reader=File.OpenText(path))Assert.That(DirectorLogCheck.Check(reader),Is.Empty);
                var fixture=Path.Combine(TestContext.CurrentContext.WorkDirectory,"clean-director.ndjson");File.Copy(path,fixture,true);
                TestContext.WriteLine("Operator checker fixture: "+fixture);
            }
            finally{File.Delete(path);}
        }
        [TestCase(false)][TestCase(true)]
        public void StaleCaptionFloorAlsoSurvivesShortReconnect(bool recoverEarly)
        {
            var scheduler=new Scheduler(DirectorKit.Story(),Kit.Layout());var store=DirectorTimeline.Clean();
            scheduler.Tick(new DirectorSample("run",6,0),store);
            Assert.That(scheduler.Log.Any(e=>e.Kind=="caption"&&e.ShowS==0),Is.True);
            scheduler.Tick(new DirectorSample("run",6,1,stale:true),store);
            Assert.That(scheduler.Log.Any(e=>e.Kind=="caption-end"&&e.ShowS<3),Is.False,"stale must not shorten a visible caption");
            scheduler.Tick(new DirectorSample("run",6,2,stale:!recoverEarly),new ObservationStore());
            scheduler.Tick(new DirectorSample("run",6,3,stale:!recoverEarly),new ObservationStore());
            Assert.That(scheduler.Log.Last(e=>e.Kind=="caption-end").ShowS,Is.EqualTo(3));
            Assert.That(scheduler.Log.Any(e=>e.Kind=="caption"&&e.ShowS>=3),Is.False,"recovery must recheck proof");
            var log=new StringWriter();var recorder=new Recorder(TextWriter.Null,TextWriter.Null,log,TextWriter.Null);
            foreach(var e in scheduler.Log)recorder.Decision(e);
            Assert.That(DirectorLogCheck.Check(new StringReader(log.ToString())),Is.Empty);
        }
        [Test] public void CaptionCheckerRejectsShortCaptionOnSamePageOnly()
        {
            var output=new StringWriter();var r=new Recorder(TextWriter.Null,TextWriter.Null,output,TextWriter.Null);
            r.Decision(new DirectorEvent(0,"caption","4","hello"));r.Decision(new DirectorEvent(1,"caption-end","5","other"));r.Decision(new DirectorEvent(2,"caption-end","4","hello"));
            Assert.That(DirectorLogCheck.Check(new StringReader(output.ToString())).Single(),Does.Contain("visible 2 s"));
        }
    }
}
