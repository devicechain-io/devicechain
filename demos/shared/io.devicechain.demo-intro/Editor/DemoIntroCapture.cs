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

        /// <summary>The intro at <paramref name="t"/> seconds as the overlay shows it, before the
        /// overlay's cover (a skip's or reduced motion's fade), written as a PNG with
        /// premultiplied alpha: where the exit has opened, the demo shows through.</summary>
        public static string RenderFrame(float t, string path, int width, int height, bool reduced = false) =>
            Render(path, width, height, intro => intro.Evaluate(t, reduced, (float)width / height), null);

        /// <summary>The locked mark alone, orthographic, over symbol.svg's view box on black.</summary>
        public static string RenderLockPose(string path, int size) =>
            Render(path, size, size, intro => intro.Evaluate(IntroTimeline.DissolveStart, false, 1f), cam =>
            {
                cam.transform.localPosition = new Vector3(0f, 0f, -5f);
                cam.orthographicSize = SymbolHalfExtent;
                cam.backgroundColor = Color.black;
                // the mark alone: no backdrop, packets, pulse, opening or lines
                foreach (var t in cam.transform.parent.GetComponentsInChildren<Transform>(true))
                    if (t.name == "Backdrop" || t.name == "Packets" || t.name == "Pulse" || t.name == "Opening" || t.GetComponent<LineRenderer>() != null)
                        t.gameObject.SetActive(false);
            });

        static string Render(string path, int width, int height, System.Action<DemoIntro> pose, System.Action<Camera> frame)
        {
            var prefab = AssetDatabase.LoadAssetAtPath<GameObject>(DemoIntroBuilder.PrefabPath);
            if (prefab == null) throw new FileNotFoundException(DemoIntroBuilder.PrefabPath);
            var go = Object.Instantiate(prefab);
            go.hideFlags = HideFlags.DontSave;
            var intro = go.GetComponent<DemoIntro>();
            var rt = new RenderTexture(width, height, 0, RenderTextureFormat.ARGB32, RenderTextureReadWrite.sRGB);
            try
            {
                pose(intro);
                frame?.Invoke(intro.IntroCamera);
                intro.RenderInto(rt);
                var prev = RenderTexture.active;
                RenderTexture.active = rt;
                var tex = new Texture2D(width, height, TextureFormat.RGBA32, false);
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
