// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

using System.IO;
using UnityEditor;
using UnityEngine;

namespace DeviceChain.Demos.EditorTools
{
    /// <summary>
    /// Writes the package's materials and the intro prefab from the generated model and the two
    /// shaders. Run it after regenerating the model (ArtSource~/build_mark.py); it overwrites what
    /// it made before and keeps the assets' GUIDs.
    /// </summary>
    public static class DemoIntroBuilder
    {
        public const string Root = "Packages/io.devicechain.demo-intro/Runtime/";
        public const string ModelPath = Root + "Models/devicechain_mark.glb";
        public const string MarkMaterialPath = Root + "Materials/IntroMark.mat";
        public const string GlowMaterialPath = Root + "Materials/IntroGlow.mat";
        public const string PrefabPath = Root + "Resources/" + DemoIntro.ResourceName + ".prefab";

        [MenuItem("Tools/DeviceChain/Rebuild Demo Intro Assets")]
        public static void Build()
        {
            var mark = AssetDatabase.LoadAssetAtPath<GameObject>(ModelPath);
            if (mark == null) throw new FileNotFoundException(ModelPath);
            var markMat = Material(MarkMaterialPath, "DeviceChain/Intro/Mark");
            var glowMat = Material(GlowMaterialPath, "DeviceChain/Intro/Glow");

            var go = new GameObject(DemoIntro.ResourceName);
            try
            {
                var intro = go.AddComponent<DemoIntro>();
                intro.mark = mark;
                intro.markMaterial = markMat;
                intro.glowMaterial = glowMat;
                Directory.CreateDirectory(Path.GetDirectoryName(Path.GetFullPath(PrefabPath)));
                PrefabUtility.SaveAsPrefabAsset(go, PrefabPath);
            }
            finally
            {
                Object.DestroyImmediate(go);
            }
            AssetDatabase.SaveAssets();
        }

        static Material Material(string path, string shaderName)
        {
            var shader = Shader.Find(shaderName);
            if (shader == null) throw new FileNotFoundException("shader " + shaderName);
            var m = AssetDatabase.LoadAssetAtPath<Material>(path);
            if (m == null)
            {
                Directory.CreateDirectory(Path.GetDirectoryName(Path.GetFullPath(path)));
                m = new Material(shader);
                AssetDatabase.CreateAsset(m, path);
            }
            else
            {
                m.shader = shader;
            }
            EditorUtility.SetDirty(m);
            return m;
        }
    }
}
