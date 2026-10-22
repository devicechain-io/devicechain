// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// A declaration-only stand-in for the Unity Test Framework's runtime assembly (UnityEngine.TestRunner):
// the two members the demo tests use. Never shipped, never run.
using System;
using System.Text.RegularExpressions;

namespace UnityEngine.TestTools
{
    [AttributeUsage(AttributeTargets.Method)]
    public sealed class UnityTestAttribute : Attribute { }

    public static class LogAssert
    {
        public static void Expect(LogType type, string message) { }
        public static void Expect(LogType type, Regex message) { }
    }
}
