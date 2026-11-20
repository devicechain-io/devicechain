// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

using UnityEngine;
using UnityEngine.Rendering;
#if UNITY_6000_0_OR_NEWER
using UnityEngine.Rendering.Universal;
#endif

namespace DeviceChain.Harborpulse.Visuals
{
    public sealed class PlanarWaterReflection
    {
        // Layer 30 is unused in this project's TagManager. Only the harbour surface uses it.
        public const int WaterLayer = 30;
        static bool rendering;
        readonly Camera main, reflection;
        readonly bool laptop;
        RenderTexture texture;
        int frame;
        public float Level { get; set; }
        public PlanarWaterReflection(Transform parent, Camera camera)
        {
            main = camera;
            var pipeline = GraphicsSettings.currentRenderPipeline;
            laptop = pipeline != null && pipeline.name.IndexOf("Laptop", System.StringComparison.OrdinalIgnoreCase) >= 0;
            var t = AssetRig.Node("PlanarReflectionCamera", parent, Vector3.zero);
            reflection = t.gameObject.AddComponent<Camera>(); reflection.enabled = false;
#if UNITY_6000_0_OR_NEWER
            var data = reflection.GetUniversalAdditionalCameraData();
            data.renderPostProcessing = false; data.renderShadows = false;
            data.requiresColorOption = CameraOverrideOption.Off; data.requiresDepthOption = CameraOverrideOption.Off;
#endif
            Shader.SetGlobalFloat("_PlanarReflectionOn", 0);
        }
        public void LateTick()
        {
            if (laptop && (++frame & 1) == 0) return;
            RenderNow();
        }
        /// <summary>Explicitly called after the still camera's target is assigned, before its Render.</summary>
        public void RenderNow()
        {
            if (rendering || main == null || main == reflection || Camera.current == reflection) return;
            Shader.SetGlobalFloat("_PlanarReflectionOn", 0);
            var divisor = laptop ? 4 : 2;
            var width = Mathf.Max(1, main.pixelWidth / divisor); var height = Mathf.Max(1, main.pixelHeight / divisor);
            if (texture == null || texture.width != width || texture.height != height)
            {
                if (texture != null) { texture.Release(); MeshKit.Release(texture); }
                // mips: the water samples a blurred level of the reflection, never the sharp mirror (see HarborWater's SoftReflection)
                texture = new RenderTexture(width, height, 24, RenderTextureFormat.ARGBHalf) { name = "Harbor planar reflection", useMipMap = true, autoGenerateMips = true, filterMode = FilterMode.Trilinear };
                texture.Create();
            }
            reflection.CopyFrom(main); reflection.enabled = false;
            reflection.targetTexture = texture; reflection.cullingMask = main.cullingMask & ~(1 << WaterLayer);
            reflection.clearFlags = CameraClearFlags.SolidColor; reflection.backgroundColor = Color.clear;
            // y' = 2 * level - y, determinant -1: restore culling even if URP throws.
            var mirror = Matrix4x4.identity; mirror.m11 = -1; mirror.m13 = 2 * Level;
            reflection.worldToCameraMatrix = main.worldToCameraMatrix * mirror;
            var p = main.transform.position; p.y = 2 * Level - p.y;
            reflection.transform.position = p;
            var f = main.transform.forward; f.y = -f.y;
            var up = main.transform.up; up.y = -up.y;
            reflection.transform.rotation = Quaternion.LookRotation(f, up);
            var normal = main.transform.position.y >= Level ? Vector3.up : Vector3.down;
            var view = reflection.worldToCameraMatrix;
            var clipPoint = view.MultiplyPoint(new Vector3(0, Level + normal.y * 0.05f, 0));
            var clipNormal = view.MultiplyVector(normal).normalized;
            reflection.projectionMatrix = main.CalculateObliqueMatrix(new Vector4(clipNormal.x, clipNormal.y, clipNormal.z, -Vector3.Dot(clipPoint, clipNormal)));
            var invert = GL.invertCulling;
            try
            {
                rendering = true; GL.invertCulling = !invert;
#if UNITY_6000_0_OR_NEWER
                var request = new UniversalRenderPipeline.SingleCameraRequest { destination = texture };
                if (!RenderPipeline.SupportsRenderRequest(reflection, request)) throw new System.InvalidOperationException("URP single-camera render requests unavailable");
                RenderPipeline.SubmitRenderRequest(reflection, request);
                Shader.SetGlobalTexture("_PlanarReflectionTex", texture);
                Shader.SetGlobalFloat("_PlanarReflectionOn", 1);
#else
                throw new System.PlatformNotSupportedException("Planar reflection requires Unity 6000.5 / URP 17.5");
#endif
            }
            finally { GL.invertCulling = invert; rendering = false; }
        }
        public void Dispose()
        {
            Shader.SetGlobalFloat("_PlanarReflectionOn", 0); Shader.SetGlobalTexture("_PlanarReflectionTex", Texture2D.blackTexture);
            if (texture != null) { texture.Release(); MeshKit.Release(texture); }
            MeshKit.Release(reflection.gameObject);
        }
    }
}
