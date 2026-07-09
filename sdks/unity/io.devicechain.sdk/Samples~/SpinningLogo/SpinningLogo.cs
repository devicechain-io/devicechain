// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

using UnityEngine;
using UnityEngine.Rendering;

namespace DeviceChain.Sdk.Unity.Samples
{
    /// <summary>
    /// The slice-4 acceptance smoke test: a rotating cube spawned entirely at runtime — no
    /// pre-authored scene assets, no backend. Attach it to a single empty GameObject in an empty
    /// scene and press Play; a spinning cube proves the package loaded, the component ran, and the
    /// render loop is alive BEFORE any reconcile/subscribe wiring is exercised (sim-subsystem
    /// contract §6, slice 4). "Expand from there": later, drive the pulse from a live event rate.
    /// </summary>
    public sealed class SpinningLogo : MonoBehaviour
    {
        [Tooltip("Degrees per second around the Y axis.")]
        public float DegreesPerSecond = 60f;

        [Tooltip("Amplitude of the idle scale pulse (0 = no pulse).")]
        public float PulseAmplitude = 0.08f;

        [Tooltip("Pulse cycles per second.")]
        public float PulseHz = 0.75f;

        private Transform _logo;
        private float _phase;

        private void Start()
        {
            EnsureCamera();
            EnsureLight();

            var cube = GameObject.CreatePrimitive(PrimitiveType.Cube);
            cube.name = "DeviceChain Logo";
            cube.transform.SetParent(transform, worldPositionStays: false);
            cube.transform.localPosition = Vector3.zero;

            // A plainly-visible tint so the smoke test reads at a glance (no texture asset needed).
            // Resolve a lit shader for the active render pipeline so the cube isn't magenta under
            // URP/HDRP (the default template for most new 2021.3+ projects).
            Renderer renderer = cube.GetComponent<Renderer>();
            if (renderer != null)
            {
                Shader lit = ResolveLitShader();
                if (lit != null)
                {
                    renderer.material = new Material(lit);
                }
                Tint(renderer.material, new Color(0.15f, 0.55f, 0.95f));
            }
            _logo = cube.transform;
        }

        private void Update()
        {
            if (_logo == null)
            {
                return;
            }
            _logo.Rotate(Vector3.up, DegreesPerSecond * Time.deltaTime, Space.World);

            if (PulseAmplitude > 0f)
            {
                _phase += Time.deltaTime * PulseHz * 2f * Mathf.PI;
                float scale = 1f + Mathf.Sin(_phase) * PulseAmplitude;
                _logo.localScale = new Vector3(scale, scale, scale);
            }
        }

        // Spawn a camera framing the logo only if the scene has none (keeps the sample drop-in).
        private void EnsureCamera()
        {
            if (Camera.main != null)
            {
                return;
            }
            var camObject = new GameObject("Main Camera");
            camObject.tag = "MainCamera";
            Camera cam = camObject.AddComponent<Camera>();
            cam.transform.position = new Vector3(0f, 1.2f, -3.5f);
            cam.transform.LookAt(transform.position);
            cam.clearFlags = CameraClearFlags.SolidColor;
            cam.backgroundColor = new Color(0.05f, 0.06f, 0.08f);
        }

        private void EnsureLight()
        {
            if (FindExistingLight() != null)
            {
                return;
            }
            var lightObject = new GameObject("Directional Light");
            Light light = lightObject.AddComponent<Light>();
            light.type = LightType.Directional;
            lightObject.transform.rotation = Quaternion.Euler(50f, -30f, 0f);
        }

        // FindAnyObjectByType arrived in 2022.2; FindObjectOfType is deprecated in Unity 6 but is the
        // only option on the package's 2021.3 floor — version-guard so both compile warning-free.
        private static Light FindExistingLight()
        {
#if UNITY_2022_2_OR_NEWER
            return FindAnyObjectByType<Light>();
#else
            return FindObjectOfType<Light>();
#endif
        }

        // Pick a lit shader for whichever render pipeline is active; fall back to the built-in Standard.
        private static Shader ResolveLitShader()
        {
            if (GraphicsSettings.currentRenderPipeline != null)
            {
                Shader urp = Shader.Find("Universal Render Pipeline/Lit");
                if (urp != null) return urp;
                Shader hdrp = Shader.Find("HDRP/Lit");
                if (hdrp != null) return hdrp;
            }
            return Shader.Find("Standard");
        }

        // Set the tint via whichever colour property the shader exposes (URP/HDRP use _BaseColor).
        private static void Tint(Material material, Color color)
        {
            if (material.HasProperty("_BaseColor")) material.SetColor("_BaseColor", color);
            if (material.HasProperty("_Color")) material.SetColor("_Color", color);
        }
    }
}
