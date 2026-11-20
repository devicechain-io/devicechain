// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

using DeviceChain.Harborpulse.Visuals;
using UnityEditor;
using UnityEditor.Build;
using UnityEditor.Build.Reporting;
using UnityEngine;
using UnityEngine.SceneManagement;

namespace DeviceChain.Harborpulse.EditorTools
{
    public sealed class HarborShaderBuild : IProcessSceneWithReport
    {
        public int callbackOrder => 0;
        public void OnProcessScene(Scene scene, BuildReport report)
        {
            foreach (var root in scene.GetRootGameObjects()) foreach (var app in root.GetComponentsInChildren<HarborpulseApp>(true))
            {
                app.wetWellShader = AssetDatabase.LoadAssetAtPath<Shader>("Assets/Harborpulse/Shaders/WetWellWater.shader");
                if (app.wetWellShader == null) throw new BuildFailedException("WetWellWater shader missing");
            }
        }
    }
}
