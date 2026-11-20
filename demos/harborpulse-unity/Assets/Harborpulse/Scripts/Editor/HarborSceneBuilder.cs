// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

using System;
using DeviceChain.Harborpulse.Visuals;
using UnityEditor;
using UnityEditor.SceneManagement;
using UnityEngine;
using UnityEngine.Rendering;
using UnityEngine.Rendering.Universal;

namespace DeviceChain.Harborpulse.EditorTools
{
    /// <summary>
    /// Writes the scene file: a camera with URP post-processing, runtime grading, and one GameObject carrying <see cref="HarborpulseApp"/>, which builds everything else in code when the scene plays. The scene
    /// holds no geometry, so regenerating it is safe: Harborpulse > Build Scene, or in batch mode
    /// <c>-executeMethod DeviceChain.Harborpulse.EditorTools.HarborSceneBuilder.BuildFromCommandLine</c>.
    /// </summary>
    public static class HarborSceneBuilder
    {
        public const string ScenePath = "Assets/Harborpulse/Scenes/Harborpulse.unity";

        [MenuItem("Harborpulse/Build Scene")]
        public static void Build()
        {
            var scene = EditorSceneManager.NewScene(NewSceneSetup.EmptyScene, NewSceneMode.Single);

            var camGo = new GameObject("Main Camera") { tag = "MainCamera" };
            var cam = camGo.AddComponent<Camera>();
            cam.allowHDR = true;
            cam.farClipPlane = 20000;
            cam.nearClipPlane = 0.3f;
            camGo.AddComponent<AudioListener>();
            var data = camGo.AddComponent<UniversalAdditionalCameraData>();
            data.renderPostProcessing = true;
            data.antialiasing = AntialiasingMode.SubpixelMorphologicalAntiAliasing;

            var appGo = new GameObject("Harborpulse");
            var app = appGo.AddComponent<HarborpulseApp>();
            app.sceneCamera = cam;
            app.litShader = Shader.Find("Universal Render Pipeline/Lit");
            app.waterShader = AssetDatabase.LoadAssetAtPath<Shader>("Assets/Harborpulse/Shaders/HarborWater.shader");
            app.wetWellShader = AssetDatabase.LoadAssetAtPath<Shader>("Assets/Harborpulse/Shaders/WetWellWater.shader");
            app.skyShader = AssetDatabase.LoadAssetAtPath<Shader>("Assets/Harborpulse/Shaders/HarborSky.shader");
            if (app.litShader == null || app.waterShader == null || app.skyShader == null || app.wetWellShader == null)
                throw new InvalidOperationException("a shader is missing: URP Lit " + (app.litShader != null) + ", water " + (app.waterShader != null) + ", sky " + (app.skyShader != null));

            System.IO.Directory.CreateDirectory("Assets/Harborpulse/Scenes");
            if (!EditorSceneManager.SaveScene(scene, ScenePath)) throw new InvalidOperationException("could not save " + ScenePath);
            EditorBuildSettings.scenes = new[] { new EditorBuildSettingsScene(ScenePath, true) };
            AssetDatabase.SaveAssets();
            Debug.Log("[harborpulse] wrote " + ScenePath);
        }

        /// <summary>Batch mode: build the scene and quit with 0, or 1 on any failure.</summary>
        public static void BuildFromCommandLine()
        {
            try
            {
                Build();
                EditorApplication.Exit(0);
            }
            catch (Exception e)
            {
                Debug.LogError("[harborpulse] scene build failed: " + e);
                EditorApplication.Exit(1);
            }
        }
    }
}
