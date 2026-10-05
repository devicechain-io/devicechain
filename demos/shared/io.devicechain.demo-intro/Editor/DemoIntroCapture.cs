// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

using System.IO;
using UnityEditor;
using UnityEngine;

namespace DeviceChain.Demos.EditorTools
{
    /// <summary>
    /// Renders the intro without playing it: any moment of it at a fixed size (for a frame
    /// sequence at a fixed timestep, one call per frame), and the lock pose framed for the lock
    /// check (ArtSource~/lock_check.py).
    /// </summary>
    public static class DemoIntroCapture
    {
        public const string LockPosePath = "Temp/demo-intro-lock.png";
        /// <summary>Half the side of symbol.svg's view box, in the model's units.</summary>
        public const float SymbolHalfExtent = 0.51965f;

        [MenuItem("Tools/DeviceChain/Render Demo Intro Lock Pose")]
        static void RenderLockPoseMenu() => Debug.Log("[DemoIntro] lock pose written to " + RenderLockPose(LockPosePath, 1024));

        /// <summary>The intro at <paramref name="t"/> seconds as the viewer sees it (before the
        /// overlay's fade, which shows the demo through it), written as a PNG.</summary>
        public static string RenderFrame(float t, string path, int width, int height, bool reduced = false) =>
            Render(path, width, height, intro => intro.Evaluate(t, reduced), null);

        /// <summary>The locked mark alone, orthographic, over symbol.svg's view box on black.</summary>
        public static string RenderLockPose(string path, int size) =>
            Render(path, size, size, intro => intro.Evaluate(IntroTimeline.DissolveStart), cam =>
            {
                cam.transform.localPosition = new Vector3(0f, 0f, -5f);
                cam.orthographicSize = SymbolHalfExtent;
                cam.backgroundColor = Color.black;
                // the mark alone: no backdrop, particles, pulse or lines
                foreach (var t in cam.transform.parent.GetComponentsInChildren<Transform>(true))
                    if (t.name == "Backdrop" || t.name == "Particles" || t.name == "Pulse" || t.GetComponent<LineRenderer>() != null)
                        t.gameObject.SetActive(false);
            });

        static string Render(string path, int width, int height, System.Action<DemoIntro> pose, System.Action<Camera> frame)
        {
            var prefab = AssetDatabase.LoadAssetAtPath<GameObject>(DemoIntroBuilder.PrefabPath);
            if (prefab == null) throw new FileNotFoundException(DemoIntroBuilder.PrefabPath);
            var go = Object.Instantiate(prefab);
            go.hideFlags = HideFlags.DontSave;
            var intro = go.GetComponent<DemoIntro>();
            var rt = new RenderTexture(width, height, 24, RenderTextureFormat.ARGB32, RenderTextureReadWrite.sRGB) { antiAliasing = 8 };
            try
            {
                pose(intro);
                var cam = intro.IntroCamera;
                cam.targetTexture = rt;
                pose(intro);                          // again, framed for the target's aspect
                frame?.Invoke(cam);
                cam.Render();
                var prev = RenderTexture.active;
                RenderTexture.active = rt;
                var tex = new Texture2D(width, height, TextureFormat.RGB24, false);
                tex.ReadPixels(new Rect(0, 0, width, height), 0, 0);
                tex.Apply();
                RenderTexture.active = prev;
                var full = Path.GetFullPath(path);
                Directory.CreateDirectory(Path.GetDirectoryName(full));
                File.WriteAllBytes(full, tex.EncodeToPNG());
                Object.DestroyImmediate(tex);
                return full;
            }
            finally
            {
                intro.Teardown();
                Object.DestroyImmediate(go);
                rt.Release();
                Object.DestroyImmediate(rt);
            }
        }
    }
}
