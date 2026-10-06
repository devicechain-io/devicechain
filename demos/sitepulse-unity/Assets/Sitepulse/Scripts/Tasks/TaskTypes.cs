// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

using System;
using System.Threading.Tasks;

namespace DeviceChain.Sitepulse.Tasks
{
    /// <summary>How a task ended: carried out, or not, and always why.</summary>
    public sealed class TaskResult
    {
        TaskResult(bool succeeded, string reason)
        {
            Succeeded = succeeded;
            Reason = reason;
        }

        public bool Succeeded { get; }

        /// <summary>What happened, for the timeline and for the platform's failure reason. Never empty on a failure.</summary>
        public string Reason { get; }

        public static TaskResult Ok(string note = null) => new TaskResult(true, note);

        public static TaskResult Fail(string reason) => new TaskResult(false, string.IsNullOrWhiteSpace(reason) ? "failed without a reason" : reason);

        public override string ToString() => Succeeded ? "succeeded" + (Reason != null ? " · " + Reason : "") : "failed · " + Reason;
    }

    /// <summary>
    /// The words a failure uses, in one place. A reset and a supersede are the two ways a task ends
    /// that nobody asked for, and the platform's row carries these words.
    /// </summary>
    public static class TaskReasons
    {
        public const string Reset = "simulation reset before completion";
        public const string NoExecutor = "this device has no task executor in this run";

        public static string SupersededBy(string token) => "superseded by " + token;
    }

    /// <summary>
    /// One validated command, handed from an SDK thread to the main thread: what to do, which command it
    /// is, where it came in the device's arrival order, and the one place its answer goes. It is plain
    /// data plus a completion source with asynchronous continuations, so completing it from the main
    /// thread never runs the SDK's continuation inline, and nothing ever blocks on it.
    /// </summary>
    public sealed class TaskRequest
    {
        readonly TaskCompletionSource<TaskResult> completion = new TaskCompletionSource<TaskResult>(TaskCreationOptions.RunContinuationsAsynchronously);

        public TaskRequest(string token, string key, string area, long sequence, int generation)
        {
            Token = token ?? throw new ArgumentNullException(nameof(token));
            Key = key ?? throw new ArgumentNullException(nameof(key));
            Area = area;
            Sequence = sequence;
            Generation = generation;
        }

        public string Token { get; }

        /// <summary>The command key: goto-area or goto-refuel.</summary>
        public string Key { get; }

        /// <summary>goto-area's destination zone token; null for the others.</summary>
        public string Area { get; }

        /// <summary>The order the command arrived in on its session (newer is greater).</summary>
        public long Sequence { get; }

        public int Generation { get; }

        public Task<TaskResult> Completion => completion.Task;

        public bool IsComplete => completion.Task.IsCompleted;

        /// <summary>Answers the command. The first answer stands; a second is ignored and reports false.</summary>
        public bool Complete(TaskResult result) => completion.TrySetResult(result);
    }

    /// <summary>Where validated commands go on the main thread. The device plane only knows this.</summary>
    public interface ITaskSink
    {
        void Submit(string externalId, TaskRequest request);

        /// <summary>A command was turned away before it was a task (unknown command, bad payload, no such place): for the timeline.</summary>
        void Refused(string externalId, string text);
    }
}
