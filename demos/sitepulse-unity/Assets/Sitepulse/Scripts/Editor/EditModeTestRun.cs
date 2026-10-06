// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

using System.IO;
using System.Text;
using UnityEditor;
using UnityEditor.TestTools.TestRunner.Api;
using UnityEngine;

namespace DeviceChain.Sitepulse.EditorTools
{
    /// <summary>
    /// Runs the EditMode tests inside an Editor that is already open and writes a plain-text summary
    /// to a file. The command-line test runner refuses a project an Editor holds, so this is how a
    /// script drives the tests without closing the Editor: start the run, then wait for the file to
    /// stop reading <c>RUNNING</c>.
    /// </summary>
    public static class EditModeTestRun
    {
        public const string DefaultResultPath = "EditorScratch/tests.result.txt";

        /// <param name="resultPath">The summary file.</param>
        /// <param name="groupRegex">Only the tests whose full name matches this regular expression (all of them when null).</param>
        public static string Start(string resultPath = DefaultResultPath, string groupRegex = null)
        {
            Directory.CreateDirectory(Path.GetDirectoryName(resultPath));
            File.WriteAllText(resultPath, "RUNNING");
            var api = ScriptableObject.CreateInstance<TestRunnerApi>();
            api.RegisterCallbacks(new Summary(resultPath));
            var filter = new Filter { testMode = TestMode.EditMode };
            if (groupRegex != null) filter.groupNames = new[] { groupRegex };
            api.Execute(new ExecutionSettings(filter));
            return "started";
        }

        sealed class Summary : ICallbacks
        {
            readonly string path;
            public Summary(string path) => this.path = path;
            public void RunStarted(ITestAdaptor tests) { }
            public void TestStarted(ITestAdaptor test) { }
            public void TestFinished(ITestResultAdaptor result) { }

            public void RunFinished(ITestResultAdaptor result)
            {
                var sb = new StringBuilder();
                sb.AppendLine($"pass={result.PassCount} fail={result.FailCount} skip={result.SkipCount} inconclusive={result.InconclusiveCount}");
                Walk(result, sb);
                File.WriteAllText(path, sb.ToString());
            }

            static void Walk(ITestResultAdaptor r, StringBuilder sb)
            {
                if (r.HasChildren) { foreach (var c in r.Children) Walk(c, sb); return; }
                if (r.TestStatus == TestStatus.Failed) sb.AppendLine("FAIL " + r.FullName + " :: " + r.Message);
            }
        }
    }
}
