// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

using System;
using System.IO;
using DeviceChain.Towerpulse.Visuals;
using UnityEngine;

namespace DeviceChain.Towerpulse.EditorTools
{
    /// <summary>
    /// Look-check stills of the story's beats, in Play mode of the offline campus scene (set TOWERPULSE_OFFLINE=1 in the Editor's environment first): rebuild the
    /// plane at one hour, run it to another with the scripted preview in the loop, put the camera where the shot file has it at a show time, draw the cards
    /// settled, and render the director's camera to a PNG. One still per call: a render is a GPU job, and the Editor is shared.
    /// </summary>
    public static class Stills
    {
        public static string Shoot(double fromHour, double toHour, double showS, string path, int width = 1600, int height = 900)
        {
            var story = UnityEngine.Object.FindAnyObjectByType<StoryApp>();
            if (story == null) return "no StoryApp: is the campus scene playing?";
            if (story.Problem != null) return "the story could not start: " + story.Problem;
            if (!story.App.Offline) return "the scene is not offline; set TOWERPULSE_OFFLINE=1 before entering Play mode";
            story.Rehearse(fromHour, toHour);
            story.Director.Seek(showS);
            if (story.Director.Error != null) return "the shot file: " + story.Director.Error;
            story.View.RefreshNow();
            return Render(story.Director.Camera, path, width, height, () => story.Ui.Refresh(0.016f, true)) + " | cards: " + story.Ui.Visible() + " | model " + Model.TimeClock.Format(story.App.Plane.Model.TimeS);
        }

        public static string Render(Camera cam, string path, int width, int height, Action beforeRender = null)
        {
            Directory.CreateDirectory(Path.GetDirectoryName(path));
            var rt = new RenderTexture(width, height, 24, RenderTextureFormat.ARGB32) { antiAliasing = 1 };
            var tex = new Texture2D(width, height, TextureFormat.RGB24, false);
            var prevTarget = cam.targetTexture;
            var prevActive = RenderTexture.active;
            try
            {
                cam.targetTexture = rt;
                // the cards are placed against the camera that draws them, so they are laid out only once it points at the frame being shot
                beforeRender?.Invoke();
                Canvas.ForceUpdateCanvases();
                cam.Render();
                RenderTexture.active = rt;
                tex.ReadPixels(new Rect(0, 0, width, height), 0, 0);
                tex.Apply();
                File.WriteAllBytes(path, tex.EncodeToPNG());
            }
            finally
            {
                cam.targetTexture = prevTarget;
                RenderTexture.active = prevActive;
                rt.Release();
                UnityEngine.Object.DestroyImmediate(rt);
                UnityEngine.Object.DestroyImmediate(tex);
            }
            return path;
        }
    }
}
