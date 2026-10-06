// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

using System;
using System.Collections.Generic;
using System.Linq;
using System.Threading;
using System.Threading.Tasks;
using DeviceChain.Sitepulse.App;
using DeviceChain.Sitepulse.Platform;
using NUnit.Framework;
using UnityEditor;
using UnityEngine;
using static DeviceChain.Sitepulse.Tests.PlatformTestData;

namespace DeviceChain.Sitepulse.Tests
{
    public sealed class PlatformBinderTests
    {
        static BindResult One(Dev dev, SceneKind kind, string externalId = null)
        {
            var devices = new[] { new SceneDevice(externalId ?? dev.ExternalId, kind) };
            return DeviceBinder.Classify(Contract(), devices, Devices(dev)).Single();
        }

        [Test]
        public void AnEquipmentDeviceBinds()
        {
            var r = One(Hauler("06"), SceneKind.Hauler);
            Assert.AreEqual(BindOutcome.Bound, r.Outcome, r.Detail);
            Assert.AreEqual("sp-hauler-06", r.DeviceToken);
            Assert.AreEqual("sp-hauler", r.TypeToken);
            Assert.AreEqual("sp-equipment-profile", r.ProfileToken);
            Assert.AreEqual(1, r.ActiveVersion);
            Assert.IsEmpty(r.UnmappedAreas);
        }

        [Test]
        public void ThePlantBindsOnItsOwnMetrics()
        {
            var r = One(Plant(), SceneKind.Plant);
            Assert.AreEqual(BindOutcome.Bound, r.Outcome, r.Detail);
        }

        [Test]
        public void ADeviceTheDevicePlatformDoesNotReturnIsMissing()
        {
            var devices = new[] { new SceneDevice("SP-LD-0004", SceneKind.Loader), new SceneDevice("SP-HL-0001", SceneKind.Hauler) };
            var r = DeviceBinder.Classify(Contract(), devices, Devices(Hauler("01")));
            Assert.AreEqual(BindOutcome.Missing, r[0].Outcome);
            Assert.AreEqual(BindOutcome.Bound, r[1].Outcome);
            Assert.AreEqual("SP-LD-0004 · not on the platform · no device with this external ID in tenant sim-sitepulse", r[0].Describe("sim-sitepulse"));
        }

        [Test]
        public void TheSameExternalIdTwiceIsAmbiguous()
        {
            var devices = new[] { new SceneDevice("SP-HL-0001", SceneKind.Hauler) };
            var r = DeviceBinder.Classify(Contract(), devices, Devices(Hauler("01"), new Dev { Token = "sp-hauler-99", ExternalId = "SP-HL-0001", Type = "sp-hauler" })).Single();
            Assert.AreEqual(BindOutcome.Ambiguous, r.Outcome);
            Assert.AreEqual(2, r.Count);
            StringAssert.Contains("2 devices share this external ID", r.Describe("t"));
        }

        [Test]
        public void ADozerThatResolvesToAHaulerIsTheWrongType()
        {
            var r = One(Hauler("01"), SceneKind.Dozer, "SP-HL-0001");
            Assert.AreEqual(BindOutcome.WrongType, r.Outcome);
            StringAssert.Contains("sp-hauler", r.Detail);
            StringAssert.Contains("sp-dozer", r.Detail);
        }

        [Test]
        public void ThePlantOnAMachineTypeIsTheWrongType()
        {
            var machine = Loader("01");
            machine.ExternalId = "SP-PL-0001";
            Assert.AreEqual(BindOutcome.WrongType, One(machine, SceneKind.Plant).Outcome);
        }

        [Test]
        public void ADeviceWithNoTypeIsTheWrongType()
        {
            var devices = new[] { new SceneDevice("SP-HL-0001", SceneKind.Hauler) };
            var r = DeviceBinder.Classify(Contract(), devices,
                "{\"devicesByExternalId\":[{\"token\":\"sp-hauler-01\",\"externalId\":\"SP-HL-0001\",\"deviceType\":null}]}").Single();
            Assert.AreEqual(BindOutcome.WrongType, r.Outcome);
        }

        [Test]
        public void ATypeWithNoProfileIsAProfileMismatch()
        {
            var d = Hauler();
            d.HasProfile = false;
            var r = One(d, SceneKind.Hauler);
            Assert.AreEqual(BindOutcome.ProfileMismatch, r.Outcome);
            StringAssert.Contains("no profile", r.Detail);
        }

        [Test]
        public void AMissingMetricIsAProfileMismatchNamingIt()
        {
            var d = Hauler();
            d.Metrics = d.Metrics.Where(m => m != "tyre_pressure_kpa").ToArray();
            var r = One(d, SceneKind.Hauler);
            Assert.AreEqual(BindOutcome.ProfileMismatch, r.Outcome);
            StringAssert.Contains("missing metric tyre_pressure_kpa", r.Detail);
        }

        [Test]
        public void AMissingCommandIsAProfileMismatchNamingIt()
        {
            var d = Dozer();
            d.Commands = new (string, string)[] { ("goto-area", EquipmentSchema) };
            var r = One(d, SceneKind.Dozer);
            Assert.AreEqual(BindOutcome.ProfileMismatch, r.Outcome);
            StringAssert.Contains("missing command goto-refuel", r.Detail);
        }

        [Test]
        public void ANumericMetricDeclaredBooleanIsAProfileMismatch()
        {
            var d = Hauler();
            d.Metrics = d.Metrics.Where(m => m != "fuel_pct").ToArray();
            d.BooleanMetrics["fuel_pct"] = "BOOLEAN";
            var r = One(d, SceneKind.Hauler);
            Assert.AreEqual(BindOutcome.ProfileMismatch, r.Outcome);
            StringAssert.Contains("fuel_pct is BOOLEAN", r.Detail);
        }

        [Test]
        public void ThePlantNeedsThroughputAndARunningFlag()
        {
            var noFlag = Plant();
            noFlag.BooleanMetrics.Clear();
            var r = One(noFlag, SceneKind.Plant);
            Assert.AreEqual(BindOutcome.ProfileMismatch, r.Outcome);
            StringAssert.Contains("plant_running", r.Detail);

            var noThroughput = Plant();
            noThroughput.Metrics = new string[0];
            StringAssert.Contains("throughput_tph", One(noThroughput, SceneKind.Plant).Detail);

            var wrongType = Plant();
            wrongType.BooleanMetrics["plant_running"] = "DOUBLE";
            StringAssert.Contains("plant_running is DOUBLE", One(wrongType, SceneKind.Plant).Detail);
        }

        [Test]
        public void AnAreaTheSceneHasNoGeometryForIsReportedButTheDeviceStillBinds()
        {
            var d = Hauler("03");
            d.Commands = new (string, string)[]
            {
                ("goto-area", "[{\"enum\": [\"sp-zone-cut\", \"sp-zone-quarry\"], \"kind\": \"SCALAR\", \"name\": \"areaToken\", \"dataType\": \"STRING\", \"required\": true}]"),
                ("goto-refuel", null),
            };
            var r = One(d, SceneKind.Hauler);
            Assert.AreEqual(BindOutcome.Bound, r.Outcome);
            CollectionAssert.AreEqual(new[] { "sp-zone-quarry" }, r.UnmappedAreas);

            var board = new ReadinessBoard(new[] { new SceneDevice(d.ExternalId, SceneKind.Hauler) }, "t");
            board.SetBind(r);
            StringAssert.Contains("sp-zone-quarry", board.Notes().Single());
        }

        [Test]
        public void AnUnreadableAreaSchemaIsAProfileMismatch()
        {
            var d = Hauler();
            d.Commands = new (string, string)[] { ("goto-area", "{not json"), ("goto-refuel", null) };
            var r = One(d, SceneKind.Hauler);
            Assert.AreEqual(BindOutcome.ProfileMismatch, r.Outcome);
            StringAssert.Contains("unreadable parameterSchema", r.Detail);
        }

        [Test]
        public void ARefuelCommandWithNoSchemaIsFine()
        {
            Assert.AreEqual(BindOutcome.Bound, One(Hauler(), SceneKind.Hauler).Outcome);
        }

        [Test]
        public void AResponseWithoutTheListIsAnError()
        {
            Assert.Throws<FormatException>(() => DeviceBinder.Classify(Contract(), new[] { new SceneDevice("a", SceneKind.Hauler) }, "{}"));
        }

        // ---- the scene's own vocabulary

        [Test]
        public void TheSceneListsThe19DevicesTheDesignNames()
        {
            var text = AssetDatabase.LoadAssetAtPath<TextAsset>("Assets/Sitepulse/Data/quarry_fleet.json").text;
            var devices = SitepulseScene.Devices(text);
            Assert.AreEqual(19, devices.Count);
            var ids = devices.Select(d => d.ExternalId).ToList();
            for (var i = 1; i <= 6; i++)
            {
                CollectionAssert.Contains(ids, $"SP-HL-{i:0000}");
                CollectionAssert.Contains(ids, $"SP-LD-{i:0000}");
                CollectionAssert.Contains(ids, $"SP-DZ-{i:0000}");
            }

            Assert.AreEqual(SceneKind.Plant, devices.Single(d => d.ExternalId == "SP-PL-0001").Kind);
            Assert.AreEqual(6, devices.Count(d => d.Kind == SceneKind.Hauler));
        }

        [Test]
        public void TheZonesTheContractCheckAreTheOnesTheFeatureFileHas()
        {
            var text = AssetDatabase.LoadAssetAtPath<TextAsset>("Assets/Sitepulse/Art/Terrain/quarry_features.json").text;
            CollectionAssert.AreEquivalent(Zones, SitepulseScene.Zones(text));
        }

        [Test]
        public void TheContractReadsItsKeysFromTheSceneVocabulary()
        {
            var c = Contract();
            CollectionAssert.AreEqual(Domain.MeasurementKeys.Equipment, c.EquipmentMetrics);
            CollectionAssert.AreEqual(Domain.CommandKeys.Equipment, c.EquipmentCommands);
            CollectionAssert.Contains(c.PlantFlags, "plant_running");
        }

        // ---- the whole bind, over a fake platform

        static QueryFn Fake(Func<string, string> answer, List<string> seen = null) =>
            (query, vars, ct) =>
            {
                seen?.Add(query);
                try { return Task.FromResult(answer(query)); }
                catch (Exception e) { return Task.FromException<string>(e); }
            };

        static readonly SceneDevice[] Three =
        {
            new SceneDevice("SP-HL-0001", SceneKind.Hauler),
            new SceneDevice("SP-LD-0004", SceneKind.Loader),
            new SceneDevice("SP-PL-0001", SceneKind.Plant),
        };

        [Test]
        public void BindingCountsEveryDeviceExactlyAndNeverRoundsUp()
        {
            var plantNoCred = Plant();
            var seen = new List<string>();
            var query = Fake(q =>
            {
                if (q == DeviceBinder.ResolveQuery) return Devices(Hauler("01"), plantNoCred); // the loader is missing
                if (q == CredentialResolver.BatchQuery) return CredBatch(Cred("sp-hauler-01-cred", "sp-hauler-01")); // the plant has no -cred row
                if (q == CredentialResolver.FallbackQuery) return CredSearch(); // and no fallback either
                throw new InvalidOperationException(q);
            }, seen);

            var board = new ReadinessBoard(Three, "sim-sitepulse");
            var creds = new DeviceCredentials();
            new DeviceBinder(query, Contract()).BindAsync(Three, board, creds, CancellationToken.None).GetAwaiter().GetResult();

            Assert.AreEqual("1/3 credentialed · 2 failed", board.Summary());
            Assert.AreEqual(1, creds.Count);
            Assert.IsTrue(creds.TryGet("sp-hauler-01", out _));
            Assert.AreEqual(DeviceStage.Credentialed, board["SP-HL-0001"].Stage);
            Assert.AreEqual(DeviceStage.Unbound, board["SP-LD-0004"].Stage);
            StringAssert.Contains("not on the platform", board["SP-LD-0004"].FailReason);
            Assert.AreEqual(DeviceStage.Resolved, board["SP-PL-0001"].Stage);
            StringAssert.Contains("no credential", board["SP-PL-0001"].FailReason);
            // one resolve, one batch, and a fallback only for the device the batch did not answer
            CollectionAssert.AreEqual(new[] { DeviceBinder.ResolveQuery, CredentialResolver.BatchQuery, CredentialResolver.FallbackQuery }, seen);
            Assert.AreEqual(LineKind.Failed, board.Lines()[1].Kind);
            Assert.AreEqual(LineKind.Ok, board.Lines()[0].Kind);
        }

        [Test]
        public void ACredentialIsNeverInAnythingTheBoardOrHolderPrints()
        {
            const string secret = "feedfacefeedfacefeedfacefeedface";
            var one = new[] { new SceneDevice("SP-HL-0001", SceneKind.Hauler) };
            var query = Fake(q => q == DeviceBinder.ResolveQuery
                ? Devices(Hauler("01"))
                : CredBatch(Cred("sp-hauler-01-cred", "sp-hauler-01", id: secret)));
            var board = new ReadinessBoard(one, "t");
            var creds = new DeviceCredentials();
            new DeviceBinder(query, Contract()).BindAsync(one, board, creds, CancellationToken.None).GetAwaiter().GetResult();

            Assert.IsTrue(creds.TryGet("sp-hauler-01", out var held));
            Assert.AreEqual(secret, held);
            StringAssert.DoesNotContain(secret, creds.ToString());
            StringAssert.DoesNotContain(secret, board.Summary());
            foreach (var l in board.Lines()) StringAssert.DoesNotContain(secret, l.Text);
        }

        [Test]
        public void AFailedQueryFailsTheDevicesWithItsReasonInsteadOfReadingAsMissing()
        {
            var board = new ReadinessBoard(Three, "t");
            var query = Fake(_ => throw new InvalidOperationException("connection refused"));
            UnityEngine.TestTools.LogAssert.Expect(LogType.Error, "[sitepulse] resolve query failed: InvalidOperationException: connection refused");
            new DeviceBinder(query, Contract()).BindAsync(Three, board, new DeviceCredentials(), CancellationToken.None).GetAwaiter().GetResult();
            Assert.AreEqual("0/3 credentialed · 3 failed", board.Summary());
            foreach (var d in board.Devices) StringAssert.Contains("connection refused", d.FailReason);
            foreach (var d in board.Devices) StringAssert.DoesNotContain("not on the platform", d.FailReason);
        }

        [Test]
        public void AFailedCredentialQueryFailsOnlyTheCredentialStage()
        {
            var one = new[] { new SceneDevice("SP-HL-0001", SceneKind.Hauler) };
            var query = Fake(q => q == DeviceBinder.ResolveQuery ? Devices(Hauler("01")) : throw new InvalidOperationException("boom"));
            var board = new ReadinessBoard(one, "t");
            UnityEngine.TestTools.LogAssert.Expect(LogType.Error, "[sitepulse] credential query failed: InvalidOperationException: boom");
            new DeviceBinder(query, Contract()).BindAsync(one, board, new DeviceCredentials(), CancellationToken.None).GetAwaiter().GetResult();
            Assert.AreEqual(DeviceStage.Resolved, board["SP-HL-0001"].Stage);
            StringAssert.Contains("credential query failed", board["SP-HL-0001"].FailReason);
        }

        [Test]
        public void AFreshBoardShowsNothingCredentialed()
        {
            var board = new ReadinessBoard(Three, "t");
            Assert.AreEqual("0/3 credentialed · 0 failed", board.Summary());
        }
    }
}
