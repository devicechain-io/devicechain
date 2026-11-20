// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

using System;
using System.Collections.Generic;

namespace DeviceChain.Harborpulse.Model.Director
{
    /// <summary>One frame's inputs to the observer adapter that turns platform and runner records into keyed observations.</summary>
    public sealed class DirectorInputs
    {
        /// <summary>The run's identity: the runner's <c>/status.scenario.startedAt</c>, round-trip formatted.</summary>
        public string Run { get; set; }
        /// <summary>The wall instant that is second 0 of this recording; occurred and observed times are seconds after it.</summary>
        public DateTimeOffset RecordingEpoch { get; set; }
        public DateTimeOffset Now { get; set; }
        /// <summary>Absolute sim hours (26 is the following night's 02:00).</summary>
        public double Sim { get; set; }
        public IHarborSource Source { get; set; }
        public HarborFrame Frame { get; set; }
    }

    /// <summary>
    /// The seam between the director and whatever observes the platform. An implementation emits each keyed observation and each
    /// coverage interval (positive while a source is healthy, a gap across any failure); it never emits a value it did not read.
    /// </summary>
    public interface IDirectorFeed
    {
        void Update(DirectorInputs inputs, Action<Observation> observe, Action<CoverageInterval> cover);
        /// <summary>Loud failures (a failed probe, a malformed record) for the banner and the engineer overlay.</summary>
        IReadOnlyList<string> Problems { get; }
    }
}
