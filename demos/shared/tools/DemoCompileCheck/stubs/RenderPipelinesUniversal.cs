// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// A declaration-only stand-in for the Universal Render Pipeline (Unity.RenderPipelines.Universal.Runtime). It declares ONLY the members the demo code touches, so a
// demo that starts using another member fails the compile check with a missing-member error, and
// the stub is extended in the same change. Never shipped, never run: it exists so the sources can be
// compiled against real UnityEngine reference assemblies with no Editor or licence, and it proves
// nothing about behaviour.
using System.Collections.Generic;
using UnityEngine;

namespace UnityEngine.Rendering.Universal
{
    public enum AntialiasingMode { None, FastApproximateAntialiasing, SubpixelMorphologicalAntiAliasing, TemporalAntiAliasing }
    public enum CameraOverrideOption { Off, On, UsePipelineSettings }

    public class UniversalAdditionalCameraData : MonoBehaviour
    {
        public bool renderPostProcessing { get; set; }
        public AntialiasingMode antialiasing { get; set; }
        public bool renderShadows { get; set; }
        public CameraOverrideOption requiresColorOption { get; set; }
        public CameraOverrideOption requiresDepthOption { get; set; }
    }

    public class ScriptableRendererFeature : ScriptableObject
    {
        public void SetActive(bool active) { }
    }

    public class ScriptableRendererData : ScriptableObject
    {
        public List<ScriptableRendererFeature> rendererFeatures { get; }
    }

    public abstract class UniversalRenderPipelineAsset : RenderPipelineAsset
    {
        public int msaaSampleCount { get; set; }
        public float shadowDistance { get; set; }
        public ScriptableRendererData[] rendererDataList { get; }
    }
}
