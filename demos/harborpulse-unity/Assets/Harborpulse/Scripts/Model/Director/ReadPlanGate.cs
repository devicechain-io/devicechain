// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

using System;

namespace DeviceChain.Harborpulse.Model.Director
{
    /// <summary>
    /// When the director's platform reads are planned and dispatched. Planning walks every device and every alarm known so far and
    /// writes each read's variables; dispatch walks every read. Done every frame, both cost more with every alarm the day raises,
    /// which is a frame time that creeps up as the day plays out. Nothing they depend on changes between frames, though: a plan is
    /// a function of the runner's status (a new one about once a second) and of the reads' own answers (one every few seconds
    /// each), and a read is due only when its own clock says so or another has just come back.
    ///
    /// So the plan is rebuilt when the status or an answer changed, or a second has passed (which covers anything not counted
    /// here, such as a device that binds late); and reads are dispatched when the plan changed, an answer came back, or the
    /// earliest idle read falls due. Per frame, with nothing new, the work is two comparisons.
    /// </summary>
    public sealed class ReadPlanGate
    {
        /// <summary>The longest a plan stands without being rebuilt.</summary>
        public static readonly TimeSpan Refresh = TimeSpan.FromSeconds(1);

        object status;
        long answers = -1, seenAnswers = -1;
        DateTimeOffset plannedAt = DateTimeOffset.MinValue, wake = DateTimeOffset.MinValue;
        bool dispatch = true;

        /// <summary>How many times the plan was rebuilt, and how many dispatch passes ran: what a test counts.</summary>
        public long Plans { get; private set; }
        public long Dispatches { get; private set; }

        /// <summary>A read's answer came back (or failed): the plan may have new alarms to ask about, and a slot is free.</summary>
        public void Answered() { answers++; dispatch = true; }

        /// <summary>Whether to rebuild the plan now. True at most once per change, and once a <see cref="Refresh"/> otherwise.</summary>
        public bool ShouldPlan(object statusNow, DateTimeOffset now)
        {
            if (ReferenceEquals(statusNow, status) && seenAnswers == answers && now - plannedAt < Refresh && now >= plannedAt) return false;
            status = statusNow; seenAnswers = answers; plannedAt = now; Plans++; dispatch = true;
            return true;
        }

        /// <summary>Whether to walk the reads for ones to start now.</summary>
        public bool ShouldDispatch(DateTimeOffset now) => dispatch || now >= wake;

        /// <summary>A dispatch pass ran; <paramref name="earliestIdleDue"/> is when the next idle read falls due (null: none is waiting on its clock).</summary>
        public void Dispatched(DateTimeOffset now, DateTimeOffset? earliestIdleDue)
        {
            Dispatches++; dispatch = false;
            wake = earliestIdleDue.HasValue && earliestIdleDue.Value > now ? earliestIdleDue.Value : now + Refresh;
        }
    }
}
