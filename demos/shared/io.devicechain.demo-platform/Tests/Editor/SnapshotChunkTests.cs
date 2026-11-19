// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

using System;
using System.Collections.Generic;
using System.Text.RegularExpressions;
using System.Threading;
using System.Threading.Tasks;
using DeviceChain.Demos.Platform;
using NUnit.Framework;

namespace DeviceChain.Demos.Tests
{
    // The platform refuses a query with more than ObserverQueries.MaxRootFields root fields ("query Snapshot selects 37 root fields; the
    // maximum is 20"). The per-device reads alias one root field per device, so a fleet past the limit must be read in chunks.
    public class SnapshotChunkTests
    {
        static List<string> Tokens(int n)
        {
            var list = new List<string>();
            for (var i = 0; i < n; i++) list.Add("dev-" + i);
            return list;
        }

        static int RootFields(string query) => Regex.Matches(query, @"\bd\d+: latestMeasurements\(").Count;

        [Test]
        public void ChunksCoverEveryTokenOnceAndNoneIsOverTheLimit()
        {
            var tokens = Tokens(37);
            var seen = new List<string>();
            var sizes = new List<int>();
            foreach (var c in ObserverQueries.Chunks(tokens))
            {
                sizes.Add(c.Count);
                seen.AddRange(c);
            }

            Assert.That(sizes, Is.EqualTo(new[] { 20, 17 }));
            Assert.That(seen, Is.EqualTo(tokens));
            foreach (var c in ObserverQueries.Chunks(tokens))
                Assert.That(RootFields(ObserverQueries.MeasurementsSnapshotQuery(c)), Is.LessThanOrEqualTo(ObserverQueries.MaxRootFields));
        }

        [Test]
        public void TheLastStateOfALargeFleetIsReadInQueriesThePlatformAccepts()
        {
            var tokens = Tokens(37);
            var queries = 0;
            QueryFn platform = (query, vars, ct) =>
            {
                queries++;
                var n = RootFields(query);
                if (n > ObserverQueries.MaxRootFields) throw new InvalidOperationException("query Last selects " + n + " root fields; the maximum is 20");
                var sb = new System.Text.StringBuilder("{");
                for (var i = 0; i < n; i++) sb.Append(i == 0 ? "" : ",").Append("\"d").Append(i).Append("\":[{\"name\":\"level\",\"value\":").Append(i).Append("}]");
                return Task.FromResult(sb.Append('}').ToString());
            };
            var state = LastStateQuery.FetchAsync(platform, tokens, CancellationToken.None).GetAwaiter().GetResult();
            Assert.That(queries, Is.EqualTo(2));
            Assert.That(state.Count, Is.EqualTo(37), "every device's last state, not only the first chunk's");
            Assert.That(state["dev-0"]["level"], Is.EqualTo(0));
            Assert.That(state["dev-20"]["level"], Is.EqualTo(0), "the second chunk's aliases start again at d0 and map to its own tokens");
            Assert.That(state["dev-36"]["level"], Is.EqualTo(16));
        }
    }
}
