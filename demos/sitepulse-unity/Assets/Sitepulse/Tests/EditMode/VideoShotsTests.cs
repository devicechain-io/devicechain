// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

using System;
using System.Collections.Generic;
using System.IO;
using System.Linq;
using System.Reflection;
using DeviceChain.Sitepulse.App;
using DeviceChain.Sitepulse.Domain;
using DeviceChain.Sitepulse.Recording;
using DeviceChain.Sitepulse.Replay;
using DeviceChain.Sitepulse.Visuals;
using NUnit.Framework;
using UnityEngine;

namespace DeviceChain.Sitepulse.Tests
{
    /// <summary>
    /// A synthetic recording of the feature video's live take (every event the video run produces, on the machines it uses), written through
    /// the real recorder: 18 machines and the crusher, steady work for the first 150 s, the low-fuel story on SP-HL-0006, the puncture and the
    /// operator's goto-area on SP-HL-0003.
    /// </summary>
    sealed class VideoTake : IDisposable
    {
        public static readonly DateTimeOffset Start = SyntheticRun.Start;
        public const string Truck = "SP-HL-0006", Second = "SP-HL-0003", Plant = "SP-PL-0001";
        public const double Length = 660.0;

        public static readonly string[] Machines = Enumerable.Range(1, 6).Select(i => "SP-DZ-000" + i)
            .Concat(Enumerable.Range(1, 6).Select(i => "SP-LD-000" + i)).Concat(Enumerable.Range(1, 6).Select(i => "SP-HL-000" + i)).ToArray();

        public static string Token(string id) => "tok-" + id.ToLowerInvariant();

        readonly string baseDir = Path.Combine(Path.GetTempPath(), "sitepulse-video-" + Guid.NewGuid().ToString("N").Substring(0, 12));
        public readonly RecordingData Data;

        public VideoTake(bool withOperator = true, bool withTyre = true)
        {
            var header = new RunHeader { RunId = "run-20261006T140000Z", StartedAtUtc = Start, Instance = "sitepulse", Tenant = "sitepulse-tenant", Manifest = "quarry_fleet_live · 19 devices", Seed = "per device" };
            foreach (var id in Machines) header.Devices.Add(new RunDevice { Id = id, Token = Token(id), Kind = id.StartsWith("SP-HL", StringComparison.Ordinal) ? "Hauler" : id.StartsWith("SP-LD", StringComparison.Ordinal) ? "Loader" : "Dozer" });
            header.Devices.Add(new RunDevice { Id = Plant, Token = Token(Plant), Kind = "Plant" });
            var table = Machines.Select(id => new SimMachine(id, id.StartsWith("SP-HL", StringComparison.Ordinal) ? SimKind.Hauler : id.StartsWith("SP-LD", StringComparison.Ordinal) ? SimKind.Loader : SimKind.Dozer)).ToList();
            var t = 0.0;
            var rec = RunRecorder.Create(baseDir, header, table, () => t, () => Start + TimeSpan.FromSeconds(t));
            var events = new List<(double at, int order, Action run)>();
            void At(double at, Action a) => events.Add((at, events.Count, a));
            DateTimeOffset U(double s) => Start + TimeSpan.FromSeconds(s);
            void Obs(double at, ObservedLine l) => At(at, () => rec.Observed(l));
            void Row(double at, string machine, string kind, string text) => At(at, () => rec.Device(SyntheticRun.Row(machine, kind, text)));
            void Route(double at, string machine, params double[] pts) => At(at, () =>
            {
                var l = DeviceLine.Of(DeviceKinds.Route, machine);
                l.Points.AddRange(pts);
                rec.Device(l);
            });

            for (var s = 0; s <= (int)Length; s++)
            {
                var sec = s;
                At(sec, () => rec.WriteSimFrame(sec, Machines.Select((id, i) => SyntheticRun.Sample(i * 6f, sec * 0.5f, 90f)).ToArray()));
            }

            foreach (var id in Machines)
                for (var s = 0; s < (int)Length; s += 10)
                    if (id != Truck || s < 150) Obs(s, SyntheticRun.Measurement(Token(id), "fuel_pct", 60, U(s), U(s)));

            // the low-fuel story on the protagonist
            Row(150, Truck, "presenter", "prepare low-fuel cycle: fuel 18.0% -> 15.5%, crosses 15% in about 60 s");
            for (var s = 152; s <= 230; s += 2) Obs(s, SyntheticRun.Measurement(Token(Truck), "fuel_pct", 15.5 - (s - 150) * 0.01, U(s), U(s)));
            Obs(205.0, SyntheticRun.Alarm(Token(Truck), "al-1", "low-fuel", "ACTIVE", U(205)));
            Obs(205.5, SyntheticRun.Command(Token(Truck), "c-1", "goto-refuel", "QUEUED", U(205.4), U(205.5)));
            Obs(206.5, SyntheticRun.Command(Token(Truck), "c-1", "goto-refuel", "SENT", U(205.4), U(206.5)));
            Row(207.0, Truck, "received", "goto-refuel (…c-1)");
            Row(207.1, Truck, "accepted", "refuel bay: route 212 m, ETA 48 s");
            Route(207.1, Truck, 0, 0, 40, 0, 40, 30);
            Route(240, Truck);
            Row(240, Truck, "arrived", "refuel queue: bay free, 0 in line");
            Row(250, Truck, "refuelling", "service started at 14.6% fuel");
            Obs(252.0, SyntheticRun.Alarm(Token(Truck), "al-1", "low-fuel", "CLEARED", U(252)));
            Row(290, Truck, "refuelling", "service finished: 14.6% -> 95.0%");
            Row(290, Truck, "outcome", "SUCCESS: refuelled 14.6% to 95.0%");
            Obs(291.0, SyntheticRun.Command(Token(Truck), "c-1", "goto-refuel", "SUCCESSFUL", U(205.4), U(291)));
            Row(292, Truck, "returning", "back to work: route 180 m, ETA 30 s");
            Route(292, Truck, 40, 30, 80, 30);
            Route(330, Truck);
            Row(330, Truck, "working", "back on its track");

            // the puncture, and the operator's command from the console
            if (withTyre)
            {
                Row(400, Second, "presenter", "puncture (a slow tyre leak): tyre pressure 703.4 -> 605.0 kPa, falls through 600 kPa in about 60 s");
                for (var s = 402; s <= 480; s += 2) Obs(s, SyntheticRun.Measurement(Token(Second), "tyre_pressure_kpa", 605 - (s - 400) * 0.12, U(s), U(s)));
                Obs(445.0, SyntheticRun.Alarm(Token(Second), "al-2", "tyre-pressure-low", "ACTIVE", U(445)));
            }

            if (withOperator)
            {
                Obs(520.0, SyntheticRun.Command(Token(Second), "c-2", "goto-area", "QUEUED", U(519.9), U(520)));
                Obs(521.0, SyntheticRun.Command(Token(Second), "c-2", "goto-area", "SENT", U(519.9), U(521)));
                Row(521.2, Second, "received", "goto-area sp-zone-yard (…c-2)");
                Row(521.3, Second, "accepted", "sp-zone-yard: route 190 m, ETA 60 s");
                Route(521.3, Second, 0, 0, 50, 10);
                Row(580, Second, "arrived", "sp-zone-yard: parked at slot 1");
                Row(580, Second, "outcome", "SUCCESS");
                Route(580, Second);
                Obs(581.0, SyntheticRun.Command(Token(Second), "c-2", "goto-area", "SUCCESSFUL", U(519.9), U(581)));
            }

            foreach (var (at, _, run) in events.OrderBy(e => e.at).ThenBy(e => e.order))
            {
                t = at;
                run();
            }

            rec.Close();
            Data = RecordingData.Load(rec.Directory);
        }

        public void Dispose()
        {
            try { if (Directory.Exists(baseDir)) Directory.Delete(baseDir, true); } catch (IOException) { }
        }
    }

    /// <summary>What a shot file may ask the data layer for, and what it may not: every flag there is, and no way to switch on what a render never carries.</summary>
    public sealed class ShotFlagTests
    {
        static string Shot(string extra, string camera = @"{""rig"":""follow"",""target"":""SP-HL-0006""}", string name = "s") =>
            @"{""fps"":60,""shots"":[{""name"":""" + name + @""",""startEvent"":{""kind"":""runStart""},""duration"":6," + extra + @"""camera"":" + camera + "}]}";

        static Shot One(string extra) => ShotFile.Parse(Shot(extra)).Shots[0];

        static string Refusal(string extra) => Assert.Throws<ShotException>(() => ShotFile.Parse(Shot(extra))).Message;

        [Test]
        public void ADefaultShotShowsTheCardsAndNothingElse()
        {
            var s = One("");
            Assert.IsTrue(s.Layers.Cards);
            Assert.AreEqual(DrawerMode.Off, s.Layers.Drawer);
            Assert.IsFalse(s.Layers.Panel);
            Assert.IsFalse(s.Layers.Route);
            Assert.IsFalse(s.Layers.ZoneLabels);
            Assert.IsEmpty(s.PinnedCards);
            Assert.IsEmpty(s.Chips);
            Assert.IsNull(s.Featured);
            Assert.AreEqual(1f, s.CardScale);
        }

        [Test]
        public void EveryFlagIsReadAsWritten()
        {
            var s = One(@"""cards"":false,""drawer"":true,""panel"":true,""route"":true,""zoneLabels"":true,""featured"":""tyre_pressure_kpa"",""cardScale"":0.8,");
            Assert.IsFalse(s.Layers.Cards);
            Assert.AreEqual(DrawerMode.Side, s.Layers.Drawer);
            Assert.IsTrue(s.Layers.Panel);
            Assert.IsTrue(s.Layers.Route);
            Assert.IsTrue(s.Layers.ZoneLabels);
            Assert.AreEqual(0f, s.ZoneLabelsDelay);
            Assert.AreEqual("tyre_pressure_kpa", s.Featured);
            Assert.AreEqual(0.8f, s.CardScale, 1e-6);
            Assert.AreEqual(DrawerMode.Full, One(@"""drawer"":""full"",").Layers.Drawer);
            Assert.AreEqual(DrawerMode.Side, One(@"""drawer"":""side"",").Layers.Drawer);
            Assert.AreEqual(DrawerMode.Off, One(@"""drawer"":false,").Layers.Drawer);
        }

        [Test]
        public void ACardListPinsThoseMachinesAndTurnsTheCardsOn()
        {
            var s = One(@"""cards"":[""SP-HL-0003"",""SP-PL-0001""],""cardStagger"":0.9,");
            Assert.IsTrue(s.Layers.Cards);
            CollectionAssert.AreEqual(new[] { "SP-HL-0003", "SP-PL-0001" }, s.PinnedCards);
            Assert.AreEqual(0.9, s.CardStagger, 1e-9);
        }

        [Test]
        public void ANumberForTheZoneLabelsIsTheSecondsIntoTheShotTheyFadeInAt()
        {
            var s = One(@"""zoneLabels"":3.5,");
            Assert.IsTrue(s.Layers.ZoneLabels);
            Assert.AreEqual(3.5, s.ZoneLabelsDelay, 1e-9);
            Assert.IsFalse(ShotDressing.LayersAt(s, 2.0).ZoneLabels, "not yet");
            Assert.IsTrue(ShotDressing.LayersAt(s, 3.5).ZoneLabels);
            Assert.IsTrue(ShotDressing.LayersAt(s, 5.0).ZoneLabels);
        }

        [Test]
        public void PinnedCardsPopInOneByOneAndAShotWithoutAStaggerShowsThemAll()
        {
            var s = One(@"""cards"":[""a"",""b"",""c"",""d""],""cardStagger"":1.0,");
            Assert.AreEqual(0, ShotDressing.PinnedCount(s, -2.0), "none in the lead-in");
            Assert.AreEqual(1, ShotDressing.PinnedCount(s, 0.0));
            Assert.AreEqual(1, ShotDressing.PinnedCount(s, 0.99));
            Assert.AreEqual(2, ShotDressing.PinnedCount(s, 1.0));
            Assert.AreEqual(4, ShotDressing.PinnedCount(s, 3.0));
            Assert.AreEqual(4, ShotDressing.PinnedCount(s, 50.0), "never more than there are");
            Assert.AreEqual(4, ShotDressing.PinnedCount(One(@"""cards"":[""a"",""b"",""c"",""d""],"), -1.0));
        }

        [Test]
        public void ABadFlagIsRefusedAndSaysWhich()
        {
            StringAssert.Contains("\"drawer\" must be", Refusal(@"""drawer"":""huge"","));
            StringAssert.Contains("\"panel\" must be true or false", Refusal(@"""panel"":""yes"","));
            StringAssert.Contains("\"route\" must be true or false", Refusal(@"""route"":1,"));
            StringAssert.Contains("\"cards\" must be true, false or a list", Refusal(@"""cards"":""all"","));
            StringAssert.Contains("lists SP-HL-0003 twice", Refusal(@"""cards"":[""SP-HL-0003"",""SP-HL-0003""],"));
            StringAssert.Contains("\"zoneLabels\" must be", Refusal(@"""zoneLabels"":""later"","));
            StringAssert.Contains("not an equipment measurement key", Refusal(@"""featured"":""throughput_tph"","));
            StringAssert.Contains("\"cardStagger\" is 0 to 30", Refusal(@"""cardStagger"":99,"));
            StringAssert.Contains("\"cardScale\" is 0.4 to 2", Refusal(@"""cardScale"":5,"));
        }

        [Test]
        public void ARenderNeverCarriesTheBadgeTheReadinessPanelTheKeyHelpOrAReplayTagAndNoShotCanAskForThem()
        {
            foreach (var banned in new[] { "badge", "hud", "readiness", "help", "keyHelp", "replayTag", "replay", "tag", "timeline" })
            {
                var msg = Refusal(@"""" + banned + @""":true,");
                StringAssert.Contains($"unknown field \"{banned}\"", msg);
                StringAssert.Contains("never carries the mode badge, the readiness panel, the key help or a replay tag", msg);
            }

            // and the layers a shot is given have no member that could draw one
            var members = typeof(OverlayLayers).GetProperties(BindingFlags.Public | BindingFlags.Instance).Select(p => p.Name).OrderBy(n => n).ToArray();
            CollectionAssert.AreEqual(new[] { "Cards", "Drawer", "FenceVisible", "Panel", "Route", "ZoneLabels" }, members);
            Assert.IsTrue(ReplayComposition.For(ReplayOptions.Parse(new[] { "x", "-sitepulse-replay", "r", "-sitepulse-render", "s.json", "-sitepulse-out", "o" }, out _)) is { Badge: false, Hud: false, ReplayTag: false });
        }

        [Test]
        public void TheFenceAndItsNameAreDrawnWithTheCardsOrTheZoneNamesAndNotWhenBothAreOff()
        {
            Assert.IsTrue(OverlayLayers.RenderDefault.FenceVisible);
            Assert.IsFalse(OverlayLayers.RenderDefault.With(cards: false).FenceVisible, "a shot with the data layer off has no outline either");
            Assert.IsTrue(OverlayLayers.RenderDefault.With(cards: false, zoneLabels: true).FenceVisible);
        }

        [Test]
        public void APersonAtTheKeyboardStartsWithCardsAndRouteAndAKeyForTheRest()
        {
            var d = OverlayLayers.InteractiveDefault;
            Assert.IsTrue(d.Cards);
            Assert.IsTrue(d.Route);
            Assert.AreEqual(DrawerMode.Off, d.Drawer);
            Assert.IsFalse(d.Panel);
            Assert.IsFalse(d.ZoneLabels);
        }

        [Test]
        public void ANewOverlayHasNoChipsAndNoPinnedCardsSoOnlyARenderGivesItAny()
        {
            var go = new GameObject("overlay") { hideFlags = HideFlags.HideAndDontSave };
            try
            {
                var overlay = go.AddComponent<IotOverlay>();
                Assert.IsNull(overlay.Chips, "chips are for a render's shot file; Live and an interactive replay never set one");
                Assert.IsNull(overlay.Pinned);
                Assert.IsNull(overlay.Featured);
                Assert.IsNull(overlay.RouteOf);
                Assert.IsNull(overlay.Proof);
                Assert.AreEqual(Vector2.zero, overlay.SafeInsets);
                Assert.AreEqual(OverlayLayers.InteractiveDefault.Cards, overlay.Layers.Cards);
            }
            finally
            {
                UnityEngine.Object.DestroyImmediate(go);
            }
        }

        [Test]
        public void TheZoneNamesAreThePlatformsAreaNamesAndAnUnknownZoneKeepsTheFilesLabel()
        {
            Assert.AreEqual("Excavation Face", ZoneNames.Of("sp-zone-cut", "Cut"));
            Assert.AreEqual("Fill Ground", ZoneNames.Of("sp-zone-fill", "Fill"));
            Assert.AreEqual("Equipment Yard", ZoneNames.Of("sp-zone-yard", "Yard"));
            Assert.AreEqual("Laydown", ZoneNames.Of("sp-zone-new", "Laydown"));
        }
    }

    public sealed class ChipTests
    {
        static string Doc(string chips, double duration = 6, string extra = "") =>
            @"{""shots"":[{""name"":""c"",""startEvent"":{""kind"":""alarm"",""key"":""low-fuel"",""state"":""ACTIVE"",""device"":""SP-HL-0006""},""offset"":-1,""duration"":" + duration + @"," + extra +
            @"""camera"":{""rig"":""follow"",""target"":""SP-HL-0006""}" + (chips == null ? "" : @",""chips"":" + chips) + "}]}";

        [Test]
        public void ChipsRenderOnlyWhenTheShotAsksAndAShotWithoutThemPlansNone()
        {
            using var take = new VideoTake();
            var without = ShotPlanner.Plan(ShotFile.Parse(Doc(null)), take.Data);
            Assert.IsEmpty(without[0].Chips);
            Assert.IsEmpty(ShotFile.Parse(Doc(null)).Shots[0].Chips);
            var with = ShotPlanner.Plan(ShotFile.Parse(Doc(@"[{""text"":""Command sent"",""at"":1.5,""duration"":2}]")), take.Data);
            Assert.AreEqual(1, with[0].Chips.Count);
            Assert.AreEqual("Command sent", with[0].Chips[0].Text);
            Assert.AreEqual(1.5, with[0].Chips[0].From, 1e-9);
            Assert.AreEqual(3.5, with[0].Chips[0].Until, 1e-9);
        }

        [Test]
        public void AnEventsChipStartsWhenTheEventDidSoItCanNeverSaySomethingHappenedBeforeItDid()
        {
            using var take = new VideoTake();
            // the shot starts one second before the alarm (at 205 s); the command is SENT at 206.5, so the chip starts 2.5 s into the shot
            var json = Doc(@"[{""text"":""Command sent"",""event"":{""kind"":""command"",""name"":""goto-refuel"",""status"":""SENT"",""device"":""SP-HL-0006""},""offset"":0,""duration"":2}]");
            var plan = ShotPlanner.Plan(ShotFile.Parse(json), take.Data);
            Assert.AreEqual(204.0, plan[0].Start, 1e-9);
            Assert.AreEqual(2.5, plan[0].Chips[0].From, 1e-9);
            Assert.AreEqual(4.5, plan[0].Chips[0].Until, 1e-9);
        }

        [Test]
        public void AChipThatFallsOutsideTheShotOrWhoseEventIsNotInTheRecordingStopsTheRender()
        {
            using var take = new VideoTake();
            var late = Doc(@"[{""text"":""Device confirms SUCCESSFUL"",""event"":{""kind"":""command"",""name"":""goto-refuel"",""status"":""SUCCESSFUL"",""device"":""SP-HL-0006""},""duration"":1}]");
            StringAssert.Contains("falls outside the shot", Assert.Throws<ShotException>(() => ShotPlanner.Plan(ShotFile.Parse(late), take.Data)).Message);
            var absent = Doc(@"[{""text"":""x"",""event"":{""kind"":""alarm"",""key"":""engine-overheat"",""state"":""ACTIVE""},""duration"":1}]");
            StringAssert.Contains("no recorded event matches", Assert.Throws<ShotException>(() => ShotPlanner.Plan(ShotFile.Parse(absent), take.Data)).Message);
        }

        [Test]
        public void AChipIsCheckedWhenTheFileIsReadNotWhenTheFrameIsDrawn()
        {
            void Refuses(string chips, string expect) => StringAssert.Contains(expect, Assert.Throws<ShotException>(() => ShotFile.Parse(Doc(chips))).Message);
            Refuses(@"[{""text"":"""",""at"":1,""duration"":1}]", "needs \"text\"");
            Refuses(@"[{""text"":""x"",""at"":1}]", "needs a \"duration\"");
            Refuses(@"[{""text"":""x"",""duration"":1}]", "exactly one of");
            Refuses(@"[{""text"":""x"",""at"":1,""event"":{""kind"":""runStart""},""duration"":1}]", "exactly one of");
            Refuses(@"[{""text"":""x"",""at"":-1,""duration"":1}]", "starts before the shot");
            Refuses(@"[{""text"":""x"",""at"":5,""duration"":3}]", "runs past the end");
            Refuses(@"[{""text"":""x"",""at"":1,""duration"":1,""colour"":""red""}]", "unknown field \"colour\"");
            Refuses(@"{}", "must be a list");
            Refuses(@"[{""text"":""" + new string('x', ChipSpec.MaxText + 1) + @""",""at"":1,""duration"":1}]", "1 to 60 characters");
        }
    }

    public sealed class CardPinTests
    {
        static CardInput In(string id, float share = 1f, int alarm = 0, bool cmd = false) => new CardInput(id, true, share, alarm, cmd);

        [Test]
        public void APinnedMachineShowsWhateverItsSizeAndWhateverItsStateLikeTheSelectedOne()
        {
            var sel = new CardSelection { MaxCards = 4, MinShare = 0.35f };
            var chosen = new List<string>();
            sel.Choose(0, new[] { In("a", 0.05f), In("b", 0.05f), In("c", 1f), In("d", 0.9f, alarm: 4) }, null, chosen, new[] { "a", "b" });
            CollectionAssert.AreEquivalent(new[] { "a", "b", "d" }, chosen, "the small pinned ones show; the unpinned quiet one does not");
            chosen.Clear();
            sel.Choose(1, new[] { In("a", 0.05f), In("c", 1f) }, null, chosen);
            CollectionAssert.IsEmpty(chosen, "without the pin a speck gets no card");
        }

        [Test]
        public void PinnedAndSelectedCardsOutrankAlarmsAndStillStopAtTheLimit()
        {
            var sel = new CardSelection { MaxCards = 3 };
            var chosen = new List<string>();
            sel.Choose(0, new[] { In("p1"), In("p2"), In("sel"), In("alarm", alarm: 5) }, "sel", chosen, new[] { "p1", "p2" });
            Assert.AreEqual(3, chosen.Count);
            CollectionAssert.DoesNotContain(chosen, "alarm");
        }
    }

    /// <summary>The follow camera's easing and its look-aside, which the video's pull-back, top-down tilt and split framing use.</summary>
    public sealed class FollowCameraTests
    {
        static MachineSample At(float x, float z, float heading) => new MachineSample { X = x, Y = 10f, Z = z, Heading = heading };

        static CameraSpec Spec(string camera) => ShotFile.Parse(@"{""shots"":[{""name"":""f"",""startEvent"":{""kind"":""runStart""},""duration"":4,""camera"":" + camera + "}]}").Shots[0].Camera;

        [Test]
        public void AFollowCameraEasesFromItsStartToItsEndOverTheShotAndHoldsItsEndsExactly()
        {
            var c = Spec(@"{""rig"":""follow"",""target"":""t"",""back"":3.5,""up"":1.8,""side"":-2,""fov"":50,""lookHeight"":2,""to"":{""back"":12,""up"":4,""side"":-3,""lookHeight"":1.5,""fov"":40}}");
            var s = At(100, 50, 0);
            var start = CameraRigs.Evaluate(c, _ => s, 0, 4);
            var end = CameraRigs.Evaluate(c, _ => s, 4, 4);
            var mid = CameraRigs.Evaluate(c, _ => s, 2, 4);
            Assert.AreEqual(98f, start.Position.x, 1e-4);
            Assert.AreEqual(11.8f, start.Position.y, 1e-4);
            Assert.AreEqual(46.5f, start.Position.z, 1e-4);
            Assert.AreEqual(97f, end.Position.x, 1e-4);
            Assert.AreEqual(14f, end.Position.y, 1e-4);
            Assert.AreEqual(38f, end.Position.z, 1e-4);
            Assert.AreEqual(50f, start.Fov);
            Assert.AreEqual(40f, end.Fov);
            Assert.AreEqual(45f, mid.Fov, 1e-4, "halfway through an ease-in-out is halfway");
            Assert.AreEqual(2f + 10f, start.LookAt.y, 1e-5);
            Assert.AreEqual(1.5f + 10f, end.LookAt.y, 1e-5);
        }

        [Test]
        public void ALookSideAimsBesideTheMachineAlongItsOwnRightNotTheWorldsEast()
        {
            var c = Spec(@"{""rig"":""follow"",""target"":""t"",""back"":12,""up"":4,""side"":-6,""lookSide"":4.5,""fov"":50}");
            var north = CameraRigs.Evaluate(c, _ => At(0, 0, 0), 0, 4);
            Assert.AreEqual(4.5f, north.LookAt.x, 1e-4, "a machine facing north: its right is east");
            var east = CameraRigs.Evaluate(c, _ => At(0, 0, 90), 0, 4);
            Assert.AreEqual(-4.5f, east.LookAt.z, 1e-4, "facing east: its right is south");
        }

        [Test]
        public void ATopDownStartLooksDownAtTheMachineAndTheTiltEndsAtFortyFiveDegrees()
        {
            var c = Spec(@"{""rig"":""follow"",""target"":""t"",""back"":0.5,""up"":60,""side"":0,""fov"":40,""lookHeight"":0,""to"":{""back"":28,""up"":28,""lookHeight"":0.5}}");
            var s = At(10, 20, 30);
            var a = CameraRigs.Evaluate(c, _ => s, 0, 5);
            var look = (a.LookAt - a.Position).normalized;
            Assert.Less(look.y, -0.99f, "straight down over the machine");
            var b = CameraRigs.Evaluate(c, _ => s, 5, 5);
            var d = b.LookAt - b.Position;
            var dip = Vector3.Angle(new Vector3(d.x, 0f, d.z), d);
            Assert.That(dip, Is.InRange(43f, 46f), "28 back, 28 up, looking at the machine's middle: about a 45 degree look");
        }

        [Test]
        public void EasingAFixedCameraFieldOnAFollowOneIsRefused()
        {
            Assert.Throws<ShotException>(() => Spec(@"{""rig"":""fixed"",""pos"":[0,1,2],""lookAt"":[0,0,0],""to"":{""back"":9}}"));
            Assert.Throws<ShotException>(() => Spec(@"{""rig"":""follow"",""target"":""t"",""to"":{""zoom"":9}}"));
        }
    }

    /// <summary>The two shot files the video is rendered from, and the website loop, against the events a video run records.</summary>
    public sealed class VideoShotFileTests
    {
        static string Tools => Path.GetFullPath(Path.Combine(Application.dataPath, "..", "tools", "shots"));

        static ShotFile Load(string name) => ShotFile.Parse(File.ReadAllText(Path.Combine(Tools, name)));

        static readonly string[] AllFiles = { "sitepulse-video.json", "sitepulse-video-9x16.json", "sitepulse-website-loop.json" };

        [Test]
        public void EveryShotOfEveryFileResolvesAgainstARecordingOfTheTakeBeforeAnyFrameIsRendered()
        {
            using var take = new VideoTake();
            foreach (var name in AllFiles)
            {
                var file = Load(name);
                var warnings = new List<string>();
                var plan = ShotPlanner.Plan(file, take.Data, warnings.Add);
                Assert.AreEqual(file.Shots.Count, plan.Count, name);
                Assert.IsEmpty(warnings, name + ": no shot asks to start before the recording does");
                foreach (var p in plan) Assert.IsFalse(p.StartClamped, name + " " + p.Shot.Name);
            }
        }

        [Test]
        public void TheVideoFileAuthorsEveryUnityShotOfTheScriptInSixteenByNine()
        {
            var file = Load("sitepulse-video.json");
            var names = file.Shots.Select(s => s.Name.Split('-')[0]).ToArray();
            foreach (var shot in new[] { "s02", "s03", "s04", "s05", "s06", "s07", "s08", "s09", "s10", "s11", "s12", "s13a", "s13b", "s14a", "s14b", "s15", "s16", "s21", "s22" })
                CollectionAssert.Contains(names, shot);
            Assert.IsTrue(file.Shots.All(s => s.Width == 1920 && s.Height == 1080), "16:9 at 1920x1080");
            Assert.AreEqual(60, file.Fps);
            Assert.AreEqual(file.Shots.Count, file.Shots.Select(s => s.Name).Distinct().Count());
        }

        [Test]
        public void TheCamerasAreTheScriptsAndTheCardsAreOnlyWhereTheScriptSaysTheDataLayerIsOn()
        {
            var file = Load("sitepulse-video.json");
            Shot Of(string prefix) => file.Shots.Single(s => s.Name.StartsWith(prefix, StringComparison.Ordinal));
            var s02 = Of("s02");
            CollectionAssert.AreEqual(new float[] { -130, 64, -165 }, s02.Camera.Pos);
            CollectionAssert.AreEqual(new float[] { 6, -8, -32 }, s02.Camera.LookAt);
            Assert.AreEqual(42f, s02.Camera.Fov);
            Assert.AreEqual(6.0, s02.Duration);
            Assert.IsFalse(s02.Layers.Cards, "S02: the data layer is off");
            Assert.IsTrue(s02.Layers.ZoneLabels, "S02: the zone names fade in");
            Assert.Greater(s02.ZoneLabelsDelay, 0);
            foreach (var off in new[] { "s03", "s04", "s22" }) Assert.IsFalse(Of(off).Layers.Cards, off + ": cards off");
            CollectionAssert.AreEquivalent(new[] { "SP-HL-0003", "SP-LD-0003", "SP-HL-0006", "SP-PL-0001" }, Of("s05").PinnedCards);
            CollectionAssert.AreEquivalent(new[] { "SP-LD-0003", "SP-PL-0001" }, Of("s06").PinnedCards);
            Assert.IsTrue(Of("s07").Layers.Panel);
            Assert.AreEqual("SP-DZ-0001", Of("s07").Selected, "the panel is a dozer's");
            Assert.AreEqual(5.0, Of("s07").Duration);
            Assert.AreEqual(50f, Of("s08").Camera.Fov);
            Assert.AreEqual(12f, Of("s08").Camera.Back);
            Assert.AreEqual(4f, Of("s08").Camera.Up);
            Assert.AreEqual("SP-HL-0006", Of("s08").Selected, "the protagonist");
            foreach (var withDrawer in new[] { "s09", "s10", "s16" }) Assert.AreEqual(DrawerMode.Side, Of(withDrawer).Layers.Drawer, withDrawer);
            Assert.AreEqual(DrawerMode.Full, Of("s14a").Layers.Drawer);
            Assert.IsTrue(Of("s11").Layers.Route, "S11: the route highlight");
            Assert.AreEqual(40f, Of("s11").Camera.Fov);
            Assert.AreEqual(50f, Of("s12").Camera.Fov);
            CollectionAssert.AreEqual(new float[] { -64, 3.2f, -46 }, Of("s12").Camera.Pos);
            Assert.AreEqual("SP-HL-0003", Of("s15").Selected, "the tyre story is on SP-HL-0003, never a dozer");
            Assert.AreEqual("tyre_pressure_kpa", Of("s15").Featured);
            Assert.AreEqual(RigKind.Orbit, Of("s15").Camera.Rig);
            Assert.AreEqual(35f, Of("s15").Camera.Fov);
            Assert.AreEqual(7.0, Of("s16").Duration);
            Assert.AreEqual("SP-HL-0003", Of("s21").Selected);
            Assert.AreEqual(45f, Of("s21").Camera.Fov);
        }

        [Test]
        public void TheTyreShotsAreNeverOnADozer()
        {
            foreach (var shot in Load("sitepulse-video.json").Shots.Where(s => s.Featured == "tyre_pressure_kpa"))
                StringAssert.DoesNotContain("SP-DZ", shot.Selected ?? "", shot.Name);
        }

        [Test]
        public void ThePortraitFileHoldsTheCraneAndTheSignatureLoopNativelyIn9By16WithTheCaptionZonesKeptClear()
        {
            var file = Load("sitepulse-video-9x16.json");
            foreach (var s in file.Shots)
            {
                Assert.AreEqual("9:16", s.Aspect, s.Name);
                Assert.AreEqual(1080, s.Width, s.Name);
                Assert.AreEqual(1920, s.Height, s.Name);
                StringAssert.EndsWith("-9x16", s.Name);
            }

            var prefixes = file.Shots.Select(s => s.Name.Split('-')[0]).ToArray();
            foreach (var shot in new[] { "s02", "s08", "s09", "s10", "s11", "s12", "s13a", "s13b" }) CollectionAssert.Contains(prefixes, shot);
            Assert.AreEqual(0.14f, ReplayRoot.PortraitSafeTop, 1e-6, "the top 14% stays clear");
            Assert.AreEqual(0.20f, ReplayRoot.PortraitSafeBottom, 1e-6, "and the bottom 20%");
            Assert.IsTrue(file.Shots.Where(s => s.Layers.Cards && s.PinnedCards.Count == 0 && !s.Name.StartsWith("s02", StringComparison.Ordinal)).All(s => s.CardScale < 1f), "cards are drawn smaller in a taller frame");
        }

        [Test]
        public void TheVideoFilesCarryNoChipsTheEditorAddsTheTextInPost()
        {
            foreach (var name in new[] { "sitepulse-video.json", "sitepulse-video-9x16.json" })
                Assert.IsTrue(Load(name).Shots.All(s => s.Chips.Count == 0), name);
        }

        // ---- the website loop

        static Shot[] Loop => Load("sitepulse-website-loop.json").Shots.ToArray();

        [Test]
        public void TheLoopIsTwentyFiveSecondsOfSixteenByNineWithNoDialogJustFourChips()
        {
            var shots = Loop;
            Assert.AreEqual(25.0, shots.Sum(s => s.Duration), 1e-9);
            Assert.IsTrue(shots.All(s => s.Width == 1920 && s.Height == 1080));
            var chips = shots.SelectMany(s => s.Chips).Select(c => c.Text).ToArray();
            CollectionAssert.AreEqual(new[] { "Telemetry over MQTT", "Rule fires: low fuel", "Command sent", "Device confirms SUCCESSFUL" }, chips);
        }

        [Test]
        public void TheLoopStartsAndEndsOnTheSameCameraPose()
        {
            var shots = Loop;
            var first = shots.First();
            var last = shots.Last();
            // the same machine pose at both ends: the follow rig is relative to it, so equal poses mean the camera is the same
            var s = new MachineSample { X = 12f, Y = 3f, Z = -8f, Heading = 70f };
            var a = CameraRigs.Evaluate(first.Camera, _ => s, 0, first.Duration);
            var b = CameraRigs.Evaluate(last.Camera, _ => s, last.Duration, last.Duration);
            Assert.AreEqual(a.Position.x, b.Position.x, 1e-5);
            Assert.AreEqual(a.Position.y, b.Position.y, 1e-5);
            Assert.AreEqual(a.Position.z, b.Position.z, 1e-5);
            Assert.AreEqual(a.LookAt.x, b.LookAt.x, 1e-5);
            Assert.AreEqual(a.LookAt.y, b.LookAt.y, 1e-5);
            Assert.AreEqual(a.LookAt.z, b.LookAt.z, 1e-5);
            Assert.AreEqual(a.Fov, b.Fov);
            // and every cut in between is on the same rig, so the joins do not jump the camera
            foreach (var shot in shots)
            {
                var p = CameraRigs.Evaluate(shot.Camera, _ => s, shot.Duration / 2, shot.Duration);
                Assert.AreEqual(a.Position.x, p.Position.x, 1e-5, shot.Name);
                Assert.AreEqual(a.Fov, p.Fov, shot.Name);
            }

            // the same composition at both ends: the same card on the same machine, nothing else drawn
            Assert.AreEqual(first.Selected, last.Selected);
            Assert.AreEqual(first.Layers.Cards, last.Layers.Cards);
            Assert.AreEqual(first.Layers.Drawer, last.Layers.Drawer);
            Assert.AreEqual(first.Layers.Panel, last.Layers.Panel);
        }

        [Test]
        public void TheLoopTellsTheStoryInOrderAndEveryEventChipStartsWhenItsEventDid()
        {
            using var take = new VideoTake();
            var plan = ShotPlanner.Plan(Load("sitepulse-website-loop.json"), take.Data);
            var story = new List<(string chip, double runSeconds)>();
            foreach (var p in plan)
                foreach (var c in p.Chips) story.Add((c.Text, p.Start + c.From));
            CollectionAssert.AreEqual(new[] { "Telemetry over MQTT", "Rule fires: low fuel", "Command sent", "Device confirms SUCCESSFUL" }, story.Select(x => x.chip).ToArray());
            for (var i = 1; i < story.Count; i++) Assert.Greater(story[i].runSeconds, story[i - 1].runSeconds, story[i].chip + " comes after " + story[i - 1].chip);
            Assert.AreEqual(205.0, story.Single(x => x.chip == "Rule fires: low fuel").runSeconds, 1e-6, "the alarm line the platform sent");
            Assert.AreEqual(206.5, story.Single(x => x.chip == "Command sent").runSeconds, 1e-6, "the command's SENT");
            Assert.AreEqual(291.0, story.Single(x => x.chip == "Device confirms SUCCESSFUL").runSeconds, 1e-6, "the platform's SUCCESSFUL");
            // the cuts run forward in time: the loop never plays the cycle backwards
            for (var i = 1; i < plan.Count; i++) Assert.GreaterOrEqual(plan[i].Start, plan[i - 1].Start, plan[i].Shot.Name);
            Assert.IsTrue(plan.All(p => !p.Shot.Camera.Machines().Except(new[] { "SP-HL-0006" }).Any()));
        }

        [Test]
        public void TheLoopIsRealTimeCutsAndNeverAnAcceleratedClock()
        {
            using var take = new VideoTake();
            var plan = ShotPlanner.Plan(Load("sitepulse-website-loop.json"), take.Data);
            Assert.IsFalse(take.Data.Header.AnyAccelerated(0, take.Data.Duration), "the take ran on the real clock, so no shot needs the accelerated caption");
            Assert.IsTrue(plan.All(p => p.Shot.Layers.Cards && p.Shot.Layers.Route));
        }

        // ---- a recording that lacks what the take should have produced

        [Test]
        public void ARenderFromATakeWithoutTheOperatorsCommandStopsAndSaysWhatTheRecordingHolds()
        {
            using var take = new VideoTake(withOperator: false);
            var ex = Assert.Throws<ShotException>(() => ShotPlanner.Plan(Load("sitepulse-video.json"), take.Data));
            StringAssert.Contains("no recorded event matches", ex.Message);
            StringAssert.Contains("SP-HL-0003", ex.Message);
            StringAssert.Contains("parked", ex.Message, "the operator's arrival is the event S21 starts from");
        }

        [Test]
        public void ARenderFromATakeWithoutThePunctureStopsToo()
        {
            using var take = new VideoTake(withTyre: false);
            var ex = Assert.Throws<ShotException>(() => ShotPlanner.Plan(Load("sitepulse-video.json"), take.Data));
            StringAssert.Contains("tyre", ex.Message);
        }

        [Test]
        public void TheLoopNeedsOnlyTheLowFuelCycleSoATakeWithoutTheTyreStoryStillRendersIt()
        {
            using var take = new VideoTake(withOperator: false, withTyre: false);
            Assert.DoesNotThrow(() => ShotPlanner.Plan(Load("sitepulse-website-loop.json"), take.Data));
        }

        [Test]
        public void ACardForADeviceTheRecordingDoesNotHoldIsRefusedByName()
        {
            using var take = new VideoTake();
            var json = @"{""shots"":[{""name"":""x"",""startEvent"":{""kind"":""runStart""},""duration"":2,""cards"":[""SP-HL-9999""],""camera"":{""rig"":""fixed"",""pos"":[0,1,2],""lookAt"":[0,0,0]}}]}";
            StringAssert.Contains("SP-HL-9999", Assert.Throws<ShotException>(() => ShotPlanner.Plan(ShotFile.Parse(json), take.Data)).Message);
        }

        [Test]
        public void ThePlantCanBePinnedEvenThoughItIsNotInTheSimulationTable()
        {
            using var take = new VideoTake();
            var json = @"{""shots"":[{""name"":""x"",""startEvent"":{""kind"":""runStart""},""duration"":2,""cards"":[""SP-PL-0001""],""camera"":{""rig"":""fixed"",""pos"":[0,1,2],""lookAt"":[0,0,0]}}]}";
            Assert.DoesNotThrow(() => ShotPlanner.Plan(ShotFile.Parse(json), take.Data));
        }
    }
}
