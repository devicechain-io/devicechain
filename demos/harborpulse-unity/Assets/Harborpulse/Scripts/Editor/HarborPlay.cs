// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

using UnityEditor;
using UnityEditor.SceneManagement;

namespace DeviceChain.Harborpulse.EditorTools
{
    /// <summary>
    /// Plays the scene in the Editor from a command line, with the player's own options, so a run in the Editor can be measured
    /// the way a player's is (the Editor is where a maintainer watches a live day, and its frame time is not the player's):
    ///
    ///   Unity -projectPath demos/harborpulse-unity -executeMethod DeviceChain.Harborpulse.EditorTools.HarborPlay.Enter \
    ///     -dc-runner http://localhost:8090 -hp-frame-stats -logFile editor-play.log
    ///
    /// -hp-frame-stats writes the frames' mean, 95th percentile and worst time to the log every ten seconds. Not for -batchmode:
    /// play mode needs the Editor's window.
    /// </summary>
    public static class HarborPlay
    {
        public static void Enter()
        {
            EditorSceneManager.OpenScene(HarborSceneBuilder.ScenePath);
            EditorApplication.EnterPlaymode();
        }
    }
}
