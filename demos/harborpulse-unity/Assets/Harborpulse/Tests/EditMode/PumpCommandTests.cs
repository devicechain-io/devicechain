// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

using System;
using System.Linq;
using System.Text.Json;
using DeviceChain.Harborpulse.Model;
using NUnit.Framework;

namespace DeviceChain.Harborpulse.Tests
{
    public class PumpCommandTests
    {
        static DateTimeOffset At(int second) => Kit.T0.AddSeconds(second);
        const string Record = "{\"results\":[{\"token\":\"c\",\"deviceToken\":\"bound-pump\",\"name\":\"start-pump\",\"status\":\"SUCCESSFUL\",\"queuedTime\":\"2026-11-19T12:00:01Z\",\"sentTime\":\"2026-11-19T12:00:02Z\",\"respondedTime\":\"2026-11-19T12:00:03Z\"}]}";
        static HarborCommand[] Parse(string json = Record)
        {
            using var doc = JsonDocument.Parse(json);
            return PumpCommandRecords.Parse(doc.RootElement, "pump", "bound-pump", At(20)).ToArray();
        }
        static HarborCommand Success(int? response, string name = "start-pump", string status = "SUCCESSFUL", string device = "pump") =>
            new HarborCommand(device, "c", name, status, At(20), At(1), At(2), response.HasValue ? At(response.Value) : (DateTimeOffset?)null, "bound-pump");

        [Test]
        public void FastTerminalCommandRetainsOnlyRecordedTimesAndObserverRows()
        {
            var history = new CommandHistory(); history.Observe(Parse()); var old = history.Snapshot();
            history.Observe(Parse()); history.Observe(new[] { new HarborCommand("pump", "c", "start-pump", "HELD", At(2)) });
            Assert.That(old.Select(c => c.Status), Is.EqualTo(new[] { "QUEUED", "SENT", "SUCCESSFUL" }));
            Assert.That(old.Select(c => c.PlatformAt), Is.EqualTo(new DateTimeOffset?[] { At(1), At(2), At(3) }));
            Assert.That(history.Snapshot().Count, Is.EqualTo(4)); Assert.That(old.Count, Is.EqualTo(3));
            Assert.That(old.All(c => c.RecordLabel.StartsWith("platform record ")), Is.True);
            Assert.That(history.Snapshot().Single(c => c.Status == "HELD").PlatformAt, Is.Null);
        }
        [Test]
        public void AnObservedStatusThePlatformRecordedIsShownOnceAsTheRecord()
        {
            var history = new CommandHistory();
            history.Observe(new[] { new HarborCommand("pump", "c", "start-pump", "SENT", At(2)), new HarborCommand("pump", "c", "start-pump", "SUCCESSFUL", At(4)) });
            history.Observe(Parse());
            history.Observe(new[] { new HarborCommand("pump", "other", "flush-well", "SUCCESSFUL", At(5)) });
            var rows = history.Snapshot();
            Assert.That(rows.Where(c => c.Token == "c").Select(c => c.Status), Is.EqualTo(new[] { "QUEUED", "SENT", "SUCCESSFUL" }));
            Assert.That(rows.Where(c => c.Token == "c").All(c => c.PlatformAt.HasValue), Is.True);
            Assert.That(history.For("c").Count, Is.EqualTo(3));
            // Another command's observed row is not hidden by this command's records.
            Assert.That(rows.Single(c => c.Token == "other").PlatformAt, Is.Null);
        }
        [TestCase("queuedTime")] [TestCase("sentTime")] [TestCase("respondedTime")]
        public void NullAndAbsentTimestampsStayNullAndAreNeverDrawn(string field)
        {
            using var doc = JsonDocument.Parse(Record);
            var value = doc.RootElement.GetProperty("results")[0].GetProperty(field).GetRawText();
            var nullJson = Record.Replace("\"" + field + "\":" + value, "\"" + field + "\":null");
            var missingJson = nullJson.Replace(",\"" + field + "\":null", "");
            foreach (var json in new[] { nullJson, missingJson })
            {
                var history = new CommandHistory(); history.Observe(Parse(json));
                Assert.That(history.Snapshot().Count, Is.EqualTo(2));
                Assert.That(history.Snapshot().All(c => c.PlatformAt.HasValue), Is.True);
            }
            Assert.That(Parse(Record.Replace("\"2026-11-19T12:00:03Z\"", "null"))[0].RespondedTime, Is.Null);
        }
        [TestCase("token", "4")] [TestCase("deviceToken", "false")] [TestCase("name", "null")]
        [TestCase("status", "\"MADE_UP\"")] [TestCase("queuedTime", "0")]
        [TestCase("sentTime", "true")] [TestCase("respondedTime", "\"bad time\"")]
        public void WrongFieldTypesAndInvalidValuesFailLoudly(string field, string replacement)
        {
            using var doc = JsonDocument.Parse(Record);
            var value = doc.RootElement.GetProperty("results")[0].GetProperty(field).GetRawText();
            Assert.Throws<FormatException>(() => Parse(Record.Replace("\"" + field + "\":" + value, "\"" + field + "\":" + replacement)));
        }
        [TestCase("{}")] [TestCase("{\"results\":null}")] [TestCase("{\"results\":[null]}")]
        public void MissingOrMalformedPagesFailLoudly(string json) => Assert.Throws<FormatException>(() => Parse(json));
        [Test]
        public void RecordsForOtherDevicesAreRefused() => Assert.Throws<FormatException>(() => Parse(Record.Replace("bound-pump", "other")));

        [TestCase(3, PumpStartKind.Platform)] [TestCase(4, PumpStartKind.Platform)]
        [TestCase(5, PumpStartKind.Unconfirmed)] [TestCase(-1, PumpStartKind.Unconfirmed)]
        public void LatePollComparesPlatformResponseToFirstRunningEvent(int response, PumpStartKind expected)
        {
            var history = new CommandHistory(); var evidence = new PumpStartEvidence();
            evidence.Update("pump", new Reading(0, At(0)), null, history, default);
            evidence.Update("pump", new Reading(1, At(4)), null, history, default);
            history.Observe(new[] { Success(response < 0 ? (int?)null : response) });
            evidence.Update("pump", new Reading(1, At(9)), null, history, default);
            Assert.That(evidence.Kind, Is.EqualTo(expected)); Assert.That(evidence.TransitionAt, Is.EqualTo(At(4)));
        }
        [TestCase("stop-pump", "SUCCESSFUL", "pump")]
        [TestCase("start-pump", "FAILED", "pump")]
        [TestCase("start-pump", "SUCCESSFUL", "other")]
        public void OtherCommandRecordsCannotConfirmStart(string name, string status, string device)
        {
            var history = new CommandHistory(); history.Observe(new[] { Success(3, name, status, device) });
            var evidence = new PumpStartEvidence(); evidence.Update("pump", new Reading(0, At(0)), null, history, default);
            evidence.Update("pump", new Reading(1, At(4)), null, history, default);
            Assert.That(evidence.Kind, Is.EqualTo(PumpStartKind.Unconfirmed));
        }
        [Test]
        public void ObservedSuccessWithoutPlatformResponseCannotConfirmStart()
        {
            var history = new CommandHistory(); history.Observe(new[] { new HarborCommand("pump", "c", "start-pump", "SUCCESSFUL", At(3)) });
            var evidence = new PumpStartEvidence(); evidence.Update("pump", new Reading(0, At(0)), null, history, default);
            evidence.Update("pump", new Reading(1, At(4)), null, history, default);
            Assert.That(evidence.Kind, Is.EqualTo(PumpStartKind.Unconfirmed));
        }
        [Test]
        public void JoiningWhileRunningHasNoTransitionEvenWithEarlierSuccess()
        {
            var history = new CommandHistory(); history.Observe(new[] { Success(3) }); var evidence = new PumpStartEvidence();
            evidence.Update("pump", new Reading(1, At(4)), new Reading(3.3, At(4)), history, new CommandCoverage(At(0), At(10)));
            evidence.Update("pump", new Reading(1, At(8)), null, history, default);
            Assert.That(evidence.Kind, Is.EqualTo(PumpStartKind.Unconfirmed)); Assert.That(evidence.TransitionAt, Is.Null);
            Assert.That(evidence.Label, Is.EqualTo("UNCONFIRMED (joined while running)"));
            evidence.Update("pump", new Reading(0, At(9)), null, history, default);
            Assert.That(evidence.Label, Is.EqualTo("UNCONFIRMED"));
        }
        [Test]
        public void LatePlatformRecordUpgradesFloatComparison()
        {
            var history = new CommandHistory(); var evidence = new PumpStartEvidence();
            evidence.Update("pump", new Reading(0, At(0)), null, history, default);
            evidence.Update("pump", new Reading(1, At(4)), new Reading(3.3, At(4)), history, new CommandCoverage(At(0), At(4)));
            Assert.That(evidence.Kind, Is.EqualTo(PumpStartKind.FloatConsistent));
            history.Observe(new[] { Success(3) }); evidence.Update("pump", new Reading(1, At(8)), null, history, default);
            Assert.That(evidence.Kind, Is.EqualTo(PumpStartKind.Platform));
        }
        [TestCase(false)] [TestCase(true)]
        public void FloatNeedsBothCommandPollsAcrossTheTransition(bool historyHealthy)
        {
            var history = new CommandHistory(); var evidence = new PumpStartEvidence();
            var observer = new CommandCoverage(At(0), At(4));
            evidence.Update("pump", new Reading(0, At(0)), null, history, default);
            evidence.Update("pump", new Reading(1, At(4)), new Reading(3.3, At(4)), history, observer.Intersect(historyHealthy ? observer : default));
            Assert.That(evidence.Kind, Is.EqualTo(historyHealthy ? PumpStartKind.FloatConsistent : PumpStartKind.Unconfirmed));
            Assert.That(observer.Intersect(new CommandCoverage(At(1), At(3))).Covers(At(0), At(4)), Is.False);
        }
        [Test]
        public void CommandPollCadenceAndFailuresBreakCoverage()
        {
            var poll = new PumpCommandPoll();
            Assert.That(poll.TryBegin(At(0)), Is.True); Assert.That(poll.TryBegin(At(2)), Is.False);
            poll.Complete(At(0)); Assert.That(poll.TryBegin(At(1)), Is.False); Assert.That(poll.TryBegin(At(2)), Is.True);
            poll.Complete(At(2)); Assert.That(poll.Coverage(At(2)).Covers(At(0), At(2)), Is.True);
            Assert.That(poll.Coverage(At(6)).Covers(At(0), At(2)), Is.False);
            Assert.That(poll.TryBegin(At(6)), Is.True); poll.Complete(At(6));
            Assert.That(poll.Coverage(At(6)).Covers(At(0), At(6)), Is.False);
            poll.Fail(); Assert.That(poll.Coverage(At(6)).Covers(At(6), At(6)), Is.False);
            Assert.That(poll.TryBegin(At(8)), Is.True); poll.Complete(At(8));
            Assert.That(poll.Coverage(At(8)).HealthySince, Is.EqualTo(At(8)));
        }
        [TestCase(1)] [TestCase(2)] [TestCase(3)]
        public void QueryHasOneBoundedAliasPerBoundPumpAndNoStatusFilter(int pumps)
        {
            var query = HarborReadQueries.CommandsFor(pumps); HarborReadQueries.RequireReadOnly(query);
            for (var i = 0; i < pumps; i++) Assert.That(query.Contains("p" + i + ": commands(criteria:{pageNumber:1,pageSize:10,deviceToken:$p" + i + "})"), Is.True);
            Assert.That(query.Contains("p" + pumps + ": commands"), Is.False);
            Assert.That(query.Contains("statuses:"), Is.False);
            foreach (var field in new[] { "token", "deviceToken", "name", "status", "queuedTime", "sentTime", "respondedTime" }) Assert.That(query.Contains(field), Is.True);
        }
        [Test]
        public void PreviewSuppliesPlatformRecordShapeEvenWhenIntermediatePollsWereMissed()
        {
            var layout = Kit.Layout(); var preview = new PreviewSource(layout);
            preview.SetTime(18.1 * 3600, At(20)); var history = new CommandHistory(); history.Observe(preview.Commands);
            Assert.That(preview.Commands.All(c => c.IsPlatformRecord && c.DeviceToken != null), Is.True);
            foreach (var c in preview.Commands) Assert.That(history.For(c.Token).Select(r => r.Status), Is.EqualTo(new[] { "QUEUED", "SENT", "SUCCESSFUL" }));
        }
        [Test]
        public void HeldPreviewKeepsCommandTimesStableAndBackwardSeeksResetThem()
        {
            var preview = new PreviewSource(Kit.Layout()); preview.SetTime(18.1 * 3600, At(20));
            var first = preview.Commands[0]; preview.SetTime(18.1 * 3600, At(25)); var held = preview.Commands[0];
            Assert.That(held.QueuedTime, Is.EqualTo(first.QueuedTime)); Assert.That(held.SentTime, Is.EqualTo(first.SentTime)); Assert.That(held.RespondedTime, Is.EqualTo(first.RespondedTime));
            preview.SetTime(17 * 3600, At(30)); preview.SetTime(18.1 * 3600, At(40));
            Assert.That(preview.Commands[0].RespondedTime, Is.EqualTo(first.RespondedTime.Value.AddSeconds(20)));
        }
    }
}
