// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

using System;
using System.Collections.Generic;
using System.Collections;
using System.Linq;

namespace DeviceChain.Harborpulse.Model.Director
{
    public sealed partial class ObservationStore
    {
        // The archive remains lossless for recording/replay. Live predicates read a materialized
        // latest-revision view, seeking directly to their authored sim interval. A frame never
        // groups the archive again. Device/key are stable across revisions of either identity.
        readonly Dictionary<(string, string, string, string, string), Bucket> buckets = new Dictionary<(string, string, string, string, string), Bucket>();
        readonly Dictionary<(string, string, bool), List<CoverageInterval>> spans = new Dictionary<(string, string, bool), List<CoverageInterval>>();
        readonly Dictionary<string, double> eventTimes = new Dictionary<string, double>(StringComparer.Ordinal);
        public long WorkUnits { get; private set; }
        public void ResetWorkUnits() => WorkUnits = 0;
        sealed class Archive : IReadOnlyCollection<Observation>
        {
            readonly ObservationStore owner;
            public Archive(ObservationStore owner) { this.owner = owner; }
            public int Count => owner.records.Count;
            public IEnumerator<Observation> GetEnumerator() {
                foreach (var row in owner.records.Values) { owner.WorkUnits++; yield return row; }
            }
            IEnumerator IEnumerable.GetEnumerator() => GetEnumerator();
        }
        sealed class Entry
        {
            public Observation Row;
            public long Order;
            public double Sim;
        }
        sealed class Bucket
        {
            public readonly List<Observation> Rows = new List<Observation>();
            public View Current, Occurred;
            public readonly View LatestCurrent, LatestOccurred;
            public double MaxObserved = double.NegativeInfinity;
            public Bucket(ObservationStore owner) {
                LatestCurrent = new View(owner, true, double.PositiveInfinity);
                LatestOccurred = new View(owner, false, double.PositiveInfinity);
            }
        }
        sealed class View
        {
            readonly bool current;
            readonly ObservationStore owner;
            readonly Dictionary<string, Entry> latest = new Dictionary<string, Entry>(StringComparer.Ordinal);
            readonly SortedSet<Entry> bySim = new SortedSet<Entry>(Comparer<Entry>.Create((a, b) => {
                var c = a.Sim.CompareTo(b.Sim); return c != 0 ? c : a.Order.CompareTo(b.Order);
            }));
            readonly SortedSet<Entry> pending = new SortedSet<Entry>(Comparer<Entry>.Create((a, b) => {
                var c = a.Row.ObservedAtS.CompareTo(b.Row.ObservedAtS); return c != 0 ? c : a.Order.CompareTo(b.Order);
            }));
            long sequence;
            public double Now { get; private set; }
            public View(ObservationStore owner, bool current, double now)
            { this.owner = owner; this.current = current; Now = now; }
            public void Add(Observation row)
            {
                owner.WorkUnits++;
                var entry = new Entry { Row = row, Order = ++sequence, Sim = row.Sim };
                if (row.ObservedAtS > Now) pending.Add(entry); else Apply(entry);
            }
            void Apply(Entry entry)
            {
                var row = entry.Row; var key = current ? row.SnapshotKey : row.EventKey;
                if (latest.TryGetValue(key, out var old)) {
                    if (old.Row.ObservedAtS > row.ObservedAtS || (old.Row.ObservedAtS == row.ObservedAtS && old.Row.OccurredAtS >= row.OccurredAtS)) return;
                    bySim.Remove(old);
                }
                latest[key] = entry; bySim.Add(entry);
            }
            public IEnumerable<Observation> Query(double now, double from, double to)
            {
                Now = now;
                while (pending.Count > 0 && pending.Min.Row.ObservedAtS <= now) {
                    var next = pending.Min; pending.Remove(next); owner.WorkUnits++; Apply(next);
                }
                return Range(from, to);
            }
            public IEnumerable<Observation> Range(double from, double to)
            {
                if (from > to) yield break;
                foreach (var entry in bySim.GetViewBetween(new Entry { Sim = from, Order = long.MinValue }, new Entry { Sim = to, Order = long.MaxValue })) {
                    owner.WorkUnits++; yield return entry.Row;
                }
            }
        }
        void Index(Observation row)
        {
            if (!eventTimes.TryGetValue(row.EventKey, out var previous) || row.ObservedAtS > previous) eventTimes[row.EventKey] = row.ObservedAtS;
            foreach (var device in new[] { (string)null, row.Device })
                foreach (var key in new[] { (string)null, row.Key }) {
                    var id = (row.Run, row.Kind, row.Source, device, key);
                    if (!buckets.TryGetValue(id, out var bucket)) buckets.Add(id, bucket = new Bucket(this));
                    bucket.Rows.Add(row); bucket.MaxObserved = Math.Max(bucket.MaxObserved, row.ObservedAtS);
                    bucket.LatestCurrent.Add(row); bucket.LatestOccurred.Add(row);
                    bucket.Current?.Add(row); bucket.Occurred?.Add(row);
                }
        }
        internal double? LatestEvidenceTime(IEnumerable<string> keys)
        {
            double? latest = null;
            foreach (var key in keys) { WorkUnits++; if (eventTimes.TryGetValue(key, out var at) && (!latest.HasValue || at > latest.Value)) latest = at; }
            return latest;
        }
        internal IEnumerable<Observation> Query(string run, string kind, string source, bool current, double now, double from, double to, string device = null, string key = null)
        {
            if (!buckets.TryGetValue((run, kind, source, device, key), out var bucket)) return Array.Empty<Observation>();
            // Live input never contains future observations. These views are built as records
            // arrive, including before the first query/page, so opening a page cannot scan history.
            if (now >= bucket.MaxObserved) return (current ? bucket.LatestCurrent : bucket.LatestOccurred).Range(from, to);
            var view = current ? bucket.Current : bucket.Occurred;
            // Standalone evidence/replay callers may seek backwards. Live scheduler clocks never do.
            if (view == null || now < view.Now) {
                view = new View(this, current, now);
                foreach (var row in bucket.Rows) view.Add(row);
                if (current) bucket.Current = view; else bucket.Occurred = view;
            }
            return view.Query(now, from, to);
        }
        internal IEnumerable<Observation> WindowHistory(string run, string device, string key, double now)
        {
            if (!buckets.TryGetValue((run, "window", EvidenceSource.Platform, device, key), out var bucket)) yield break;
            foreach (var row in bucket.Rows) { WorkUnits++; if (row.ObservedAtS <= now) yield return row; }
        }
        public IReadOnlyList<CoverageInterval> Coverage => spans.Values.SelectMany(s => s).ToArray();
        // Each sign is a sorted union. In particular a failed read extending the same gap
        // every frame occupies one span, while any overlapping gap still vetoes proof.
        int EndBefore(List<CoverageInterval> list, double from)
        {
            int lo = 0, hi = list.Count;
            while (lo < hi) { WorkUnits++; int mid = lo + (hi - lo) / 2; if (list[mid].ToSim < from) lo = mid + 1; else hi = mid; }
            return lo;
        }
        void IndexCoverage(CoverageInterval interval)
        {
            var key = (interval.Run, interval.Stream, interval.Gap);
            if (!spans.TryGetValue(key, out var list)) spans.Add(key, list = new List<CoverageInterval>());
            var at = EndBefore(list, interval.FromSim); var end = at;
            var from = interval.FromSim; var to = interval.ToSim;
            while (end < list.Count && list[end].FromSim <= to) {
                WorkUnits++; from = Math.Min(from, list[end].FromSim); to = Math.Max(to, list[end].ToSim); end++;
            }
            if (end == at + 1 && list[at].FromSim == from && list[at].ToSim == to) return;
            if (end > at) list.RemoveRange(at, end - at);
            list.Insert(at, new CoverageInterval(interval.Run, interval.Stream, from, to, interval.Gap));
        }
        bool IndexedCovers(string run, string stream, double from, double to)
        {
            if (to < from) return false;
            if (spans.TryGetValue((run, stream, true), out var gaps)) {
                var at = EndBefore(gaps, from); if (at < gaps.Count && gaps[at].FromSim <= to) return false;
            }
            if (!spans.TryGetValue((run, stream, false), out var positive)) return false;
            // Preserve the original zero-duration query convention: an earlier positive
            // interval suffices at a point, provided no gap overlaps that point.
            if (from == to) return positive.Count > 0 && positive[0].FromSim <= from;
            var index = EndBefore(positive, from);
            return index < positive.Count && positive[index].FromSim <= from && positive[index].ToSim >= to;
        }
    }
}
