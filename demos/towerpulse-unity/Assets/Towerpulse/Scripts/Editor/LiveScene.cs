// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

using System.IO;
using DeviceChain.Towerpulse.DevicePlane;
using UnityEditor;
using UnityEditor.SceneManagement;
using UnityEngine;

namespace DeviceChain.Towerpulse.EditorTools
{
    /// <summary>
    /// The headless bootstrap scene: an empty scene with the device plane on one GameObject. Build it from the menu or from a script
    /// (<c>LiveScene.Build()</c>); <see cref="Play"/> opens it and enters Play mode. There are no visuals here: those read the same plane later.
    /// </summary>
    public static class LiveScene
    {
        public const string ScenePath = "Assets/Towerpulse/Scenes/TowerpulseLive.unity";

        [MenuItem("Towerpulse/Create live scene")]
        public static string Build()
        {
            Directory.CreateDirectory(Path.GetDirectoryName(ScenePath));
            var scene = EditorSceneManager.NewScene(NewSceneSetup.EmptyScene, NewSceneMode.Single);
            var go = new GameObject("TowerpulseDevicePlane");
            go.AddComponent<TowerpulseLiveApp>();
            EditorSceneManager.SaveScene(scene, ScenePath);
            return ScenePath;
        }

        [MenuItem("Towerpulse/Play live scene")]
        public static void Play()
        {
            if (!File.Exists(ScenePath)) Build();
            EditorSceneManager.OpenScene(ScenePath);
            EditorApplication.EnterPlaymode();
        }
    }
}
