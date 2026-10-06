// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

using System.Threading.Tasks;
using DeviceChain.Sitepulse.Tasks;
using NUnit.Framework;

namespace DeviceChain.Sitepulse.Tests
{
    /// <summary>
    /// Reads a command's answer in a test. A request's completion runs its continuations
    /// asynchronously, so reading <c>.Result</c> of one nobody answered blocks the Editor's main
    /// thread for good: an unanswered command, which is exactly what several tests guard against,
    /// would hang the whole test run instead of failing one test. This fails it instead.
    /// </summary>
    static class AnswerOf
    {
        public static TaskResult Answer(this Task<TaskResult> completion)
        {
            Assert.IsTrue(completion.IsCompleted, "the command was never answered");
            return completion.Result;
        }
    }
}
