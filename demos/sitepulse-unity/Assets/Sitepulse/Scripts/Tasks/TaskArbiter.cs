// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

using System;

namespace DeviceChain.Sitepulse.Tasks
{
    /// <summary>What the arbiter decided about an incoming command.</summary>
    public sealed class ArbiterDecision
    {
        ArbiterDecision(bool accepted, TaskRequest superseded, string reason)
        {
            Accepted = accepted;
            Superseded = superseded;
            Reason = reason;
        }

        /// <summary>The incoming command runs.</summary>
        public bool Accepted { get; }

        /// <summary>The command that was running and is now over (accepted only; may be null).</summary>
        public TaskRequest Superseded { get; }

        /// <summary>Why the incoming command was refused (not accepted only).</summary>
        public string Reason { get; }

        public static ArbiterDecision Start(TaskRequest superseded) => new ArbiterDecision(true, superseded, null);
        public static ArbiterDecision Refuse(string reason) => new ArbiterDecision(false, null, reason);
    }

    /// <summary>
    /// One machine's movement tasks: the newer command wins. Newer means later in the order the commands
    /// arrived at the session (<see cref="TaskRequest.Sequence"/>), never the order their handlers
    /// happened to reach the main thread, because the SDK starts concurrent handlers on pool threads and
    /// two commands delivered back to back can arrive here in either order.
    ///
    /// A command that arrives later than a newer one the machine has already seen is refused at once,
    /// even if that newer one has since finished: running the older one now would act on an intent the
    /// operator has already replaced. The arbiter only decides; the caller ends the superseded task and
    /// releases what it held.
    /// </summary>
    public sealed class TaskArbiter
    {
        TaskRequest running;
        TaskRequest newest;

        public TaskRequest Running => running;

        public ArbiterDecision Offer(TaskRequest incoming)
        {
            if (incoming == null) throw new ArgumentNullException(nameof(incoming));
            if (newest != null && incoming.Sequence <= newest.Sequence)
                return ArbiterDecision.Refuse(TaskReasons.SupersededBy(running != null ? running.Token : newest.Token));

            var previous = running;
            running = incoming;
            newest = incoming;
            return ArbiterDecision.Start(previous);
        }

        /// <summary>The task ended (any way). Frees the machine for the next command; the newest sequence is remembered.</summary>
        public void Finished(TaskRequest request)
        {
            if (ReferenceEquals(running, request)) running = null;
        }
    }
}
