// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

using DeviceChain.Harborpulse.Model;
using UnityEngine;
#if UNITY_6000_0_OR_NEWER
using UnityEngine.Rendering;
using UnityEngine.Rendering.Universal;
#endif

namespace DeviceChain.Harborpulse.Visuals
{
    /// <summary>Runtime profile shared by play mode and the edit-mode stills path; never writes an asset.</summary>
    public sealed class HarborGrading
    {
#if UNITY_6000_0_OR_NEWER
        readonly Volume volume;
        readonly VolumeProfile profile;
        readonly ColorAdjustments colour;
        readonly WhiteBalance balance;
        readonly Bloom bloom;
        readonly UniversalAdditionalCameraData cameraData;
        readonly bool oldPost, ownedCameraData, oldHdr;
        readonly Camera camera;
        public HarborGrading(Transform parent, Camera camera)
        {
            this.camera = camera; oldHdr = camera.allowHDR;
            ownedCameraData = !camera.TryGetComponent<UniversalAdditionalCameraData>(out _);
            cameraData = camera.GetUniversalAdditionalCameraData(); oldPost = cameraData.renderPostProcessing;
            cameraData.renderPostProcessing = true;
            camera.allowHDR = true;
            var t = AssetRig.Node("HarborGrading", parent, Vector3.zero);
            volume = t.gameObject.AddComponent<Volume>(); volume.isGlobal = true; volume.priority = 100;
            profile = ScriptableObject.CreateInstance<VolumeProfile>(); profile.name = "Harbor runtime grading";
            volume.sharedProfile = profile;
            profile.Add<Tonemapping>(true).mode.Override(TonemappingMode.ACES);
            colour = profile.Add<ColorAdjustments>(true); balance = profile.Add<WhiteBalance>(true); bloom = profile.Add<Bloom>(true);
            bloom.threshold.Override(1.1f); bloom.scatter.Override(0.6f);
            profile.Add<Vignette>(true).intensity.Override(0.13f);
        }
        public void Apply(SkyState sky)
        {
            var night = (float)sky.Night; var storm = (float)sky.Overcast;
            var warm = (1 - Mathf.Abs(night * 2 - 1)) * (1 - storm);
            colour.postExposure.Override(0.2f + night * 0.75f - storm * 0.3f);
            colour.contrast.Override(5 + night * 5 - storm * 3);
            colour.saturation.Override(3 - night * 19 - storm * 18);
            balance.temperature.Override(warm * 18 - night * 12); balance.tint.Override(warm * 3);
            bloom.intensity.Override(Mathf.Lerp(0.18f, 0.65f, night));
        }
        public void Dispose()
        {
            camera.allowHDR = oldHdr;
            if (ownedCameraData) MeshKit.Release(cameraData);
            else cameraData.renderPostProcessing = oldPost;
            foreach (var component in profile.components) MeshKit.Release(component);
            MeshKit.Release(profile); MeshKit.Release(volume.gameObject);
        }
#else
        // Tier-1's older Unity reference package cannot compile URP. Real Unity builds must select the implementation above.
        public HarborGrading(Transform parent, Camera camera) => throw new System.PlatformNotSupportedException("Harbor grading requires Unity 6000.5 / URP 17.5");
        public void Apply(SkyState sky) { }
        public void Dispose() { }
#endif
    }
}
