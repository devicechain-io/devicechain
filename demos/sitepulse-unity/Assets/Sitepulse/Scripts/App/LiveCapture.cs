// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

using System.Collections;
using System.Globalization;
using UnityEngine;

namespace DeviceChain.Sitepulse.App
{
    /// <summary>
    /// <c>-sitepulse-screenshot &lt;path.png&gt;</c> saves one frame of the player, after
    /// <c>-sitepulse-screenshot-after &lt;seconds&gt;</c> (default 60), so an unattended run leaves a
    /// picture of what it showed: the cards, the badge, the readiness panel. It captures the
    /// player's own frame, never the desktop around it, and does nothing without the flag.
    /// </summary>
    public sealed class LiveCapture : MonoBehaviour
    {
        const string PathFlag = "-sitepulse-screenshot";
        const string AfterFlag = "-sitepulse-screenshot-after";

        string path;
        float after = 60f;

        [RuntimeInitializeOnLoadMethod(RuntimeInitializeLoadType.AfterSceneLoad)]
        static void Install()
        {
            var args = System.Environment.GetCommandLineArgs();
            var p = Value(args, PathFlag);
            if (string.IsNullOrEmpty(p)) return;
            var go = new GameObject("Live Capture");
            DontDestroyOnLoad(go);
            var c = go.AddComponent<LiveCapture>();
            c.path = p;
            if (float.TryParse(Value(args, AfterFlag), NumberStyles.Float, CultureInfo.InvariantCulture, out var s) && s > 0) c.after = s;
        }

        static string Value(string[] args, string flag)
        {
            var i = System.Array.IndexOf(args, flag);
            return i >= 0 && i + 1 < args.Length ? args[i + 1] : null;
        }

        IEnumerator Start()
        {
            yield return new WaitForSecondsRealtime(after);
            yield return new WaitForEndOfFrame();
            ScreenCapture.CaptureScreenshot(path);
            Debug.Log($"[sitepulse] screenshot saved to {path}");
        }
    }
}
