// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

using System;
using System.Collections.Generic;
using UnityEngine;
using UnityEngine.Events;
using UnityEngine.Rendering;
using UnityEngine.Rendering.Universal;
using UnityEngine.UI;
#if DC_INTRO_INPUT_SYSTEM
using UnityEngine.InputSystem;
#endif

namespace DeviceChain.Demos
{
    /// <summary>
    /// The animated DeviceChain logo intro that opens a demo. It plays over whatever the demo's
    /// own cameras draw, then dissolves into it, so the demo needs no changes beyond adding this
    /// component (or calling <see cref="Play(Action)"/>):
    /// <list type="bullet">
    /// <item>the intro draws with its own orthographic camera, far from the scene, into a
    /// render texture shown full screen over the demo; its camera skips post-processing, so at the
    /// lock the mark is the exact brand colours whatever the demo's look;</item>
    /// <item>any key, click, tap or gamepad button skips it (a short fade);</item>
    /// <item>reduced motion shows the still lockup and fades: set <see cref="reducedMotion"/>,
    /// <see cref="PreferReducedMotion"/>, or start the player with <c>-reduced-motion</c>;</item>
    /// <item>a demo disables it with <see cref="Disabled"/> (before the scene loads) or the
    /// <c>-no-intro</c> command-line flag; it then completes at once.</item>
    /// </list>
    /// What the intro shows is a pure function of time (<see cref="IntroTimeline"/>), and
    /// <see cref="Evaluate"/> shows any moment of it, which is how a frame sequence is captured.
    /// </summary>
    [DisallowMultipleComponent]
    public sealed class DemoIntro : MonoBehaviour
    {
        public const string NoIntroFlag = "-no-intro";
        public const string ReducedMotionFlag = "-reduced-motion";
        /// <summary>The prefab <see cref="Play(Action)"/> instantiates, under a Resources folder.</summary>
        public const string ResourceName = "DeviceChainDemoIntro";
        /// <summary>The most one frame advances the intro, in seconds.</summary>
        public const float MaxStep = 0.1f;

        /// <summary>When true, every intro completes at once without drawing. Set it before the
        /// scene with the intro loads (for example from a
        /// <c>RuntimeInitializeOnLoadMethod(BeforeSceneLoad)</c>). The <c>-no-intro</c> flag sets it.</summary>
        public static bool Disabled;
        /// <summary>When true, every intro plays its reduced-motion form. The
        /// <c>-reduced-motion</c> flag sets it.</summary>
        public static bool PreferReducedMotion;

        [Tooltip("The generated mark (devicechain_mark.glb): Frame, Cube and Wordmark nodes.")]
        public GameObject mark;
        [Tooltip("Template for the mark's surfaces (DeviceChain/Intro/Mark).")]
        public Material markMaterial;
        [Tooltip("Template for the rim light, the glow, the particles and the backdrop (DeviceChain/Intro/Glow).")]
        public Material glowMaterial;
        [Tooltip("Play when the scene starts. Off when the intro is started from code.")]
        public bool playOnStart = true;
        [Tooltip("Show the still lockup and a quick fade instead of the animation.")]
        public bool reducedMotion;
        [Tooltip("Any key, click, tap or gamepad button skips the intro.")]
        public bool skippable = true;
        [Tooltip("Sorting order of the overlay canvas the intro is shown on.")]
        public int sortingOrder = 32000;
        [Tooltip("Background colour behind the mark.")]
        public Color background = new Color(0.010f, 0.014f, 0.020f, 1f);
        [Tooltip("Destroy this GameObject when the intro completes.")]
        public bool destroyOnComplete = true;
        [Tooltip("Invoked once when the intro has dissolved (or was disabled).")]
        public UnityEvent completed = new UnityEvent();

        /// <summary>Raised once when the intro has dissolved (or was disabled).</summary>
        public event Action Completed;

        // where the rig is built: far below any scene, out of every other camera's range
        public static readonly Vector3 RigOrigin = new Vector3(0f, -10000f, 0f);

        // the mark's geometry, in its authoring frame (build_mark.py): 1 unit = 100 SVG units,
        // origin at the hexagon's centre, the viewer on -Z
        public const float OuterRadius = 0.51965f, InnerRadius = 0.30565f, Bevel = 0.08f;
        public const float CubeProjectedEdge = 0.2598f;
        /// <summary>The lockup's vertical extent (logo.svg): the mark's top to the wordmark's base.</summary>
        public const float LockupTop = 0.5196f, LockupBottom = -0.9507f;
        public const float LockupHalfWidth = 1.25f;
        /// <summary>Half the view's height at the lockup, in units.</summary>
        public const float ViewHalfHeight = 1.62f;
        public static float LockupCentreY => (LockupTop + LockupBottom) * 0.5f;

        float time;
        float skippedAt = -1f;
        bool playing, finished;
        bool reducedNow;

        // the rig
        GameObject rig;
        Transform frame, cube, wordmark, pulse;
        Camera cam;
        RenderTexture target;
        Canvas canvas;
        RawImage image;
        Material matMark, matWord, matTraceCore, matTraceHalo, matCubeCore, matCubeHalo, matPulse, matParticles, matBackdrop;
        Mesh particleMesh;
        Particle[] particles;
        Vector3[] particleVerts;
        Color[] particleColours;
        Vector3 wordmarkRest;
        readonly List<UnityEngine.Object> owned = new List<UnityEngine.Object>();

        struct Particle
        {
            public Vector3 start, velocity;
            public float size, phase, rate, brightness;
        }

        /// <summary>Seconds since the intro started.</summary>
        public float Time => time;
        public bool IsPlaying => playing;
        public bool IsComplete => finished;
        /// <summary>The intro's camera, once the rig is built (by playing or <see cref="Evaluate"/>).</summary>
        public Camera IntroCamera => cam;
        /// <summary>Whether this play is the reduced-motion form.</summary>
        public bool ReducedMotionActive => reducedNow;

        [RuntimeInitializeOnLoadMethod(RuntimeInitializeLoadType.SubsystemRegistration)]
        static void ReadFlags()
        {
            var args = Environment.GetCommandLineArgs();
            Disabled = Array.IndexOf(args, NoIntroFlag) >= 0;
            PreferReducedMotion = Array.IndexOf(args, ReducedMotionFlag) >= 0;
        }

        /// <summary>Instantiates the packaged intro and plays it; <paramref name="onComplete"/> runs
        /// when it has dissolved, or at once when intros are disabled.</summary>
        public static DemoIntro Play(Action onComplete)
        {
            if (Disabled)
            {
                onComplete?.Invoke();
                return null;
            }
            var prefab = Resources.Load<GameObject>(ResourceName);
            if (prefab == null)
            {
                Debug.LogError($"[DemoIntro] no prefab named {ResourceName} under a Resources folder; skipping the intro");
                onComplete?.Invoke();
                return null;
            }
            var go = Instantiate(prefab);
            go.name = prefab.name;
            var intro = go.GetComponent<DemoIntro>();
            intro.playOnStart = false;
            intro.destroyOnComplete = true;
            if (onComplete != null) intro.Completed += onComplete;
            intro.Play();
            return intro;
        }

        void Start()
        {
            if (playOnStart && !playing && !finished) Play();
        }

        /// <summary>Starts (or restarts) the intro.</summary>
        public void Play()
        {
            finished = false;
            if (Disabled)
            {
                Finish();
                return;
            }
            if (mark == null || markMaterial == null || glowMaterial == null)
            {
                Debug.LogError("[DemoIntro] the mark, mark material or glow material is not set; skipping the intro");
                Finish();
                return;
            }
            reducedNow = reducedMotion || PreferReducedMotion;
            time = 0f;
            skippedAt = -1f;
            playing = true;
            EnsureRig(true);
            Apply(PoseAt(0f));
        }

        /// <summary>Fades the intro out from wherever it is.</summary>
        public void Skip()
        {
            if (playing && skippedAt < 0f) skippedAt = time;
        }

        void Update()
        {
            if (!playing) return;
            if (skippable && AnyInput()) Skip();
            // a long frame (the scene loading, a hitch) must not swallow the opening beats, but
            // a slow machine still plays the intro in real time
            time += Mathf.Min(UnityEngine.Time.unscaledDeltaTime, MaxStep);
            if (IsDone(time))
            {
                Finish();
                return;
            }
            EnsureOutput();
            Apply(PoseAt(time));
        }

        /// <summary>The pose at <paramref name="t"/> for this play: the full or reduced form, and a
        /// skip's fade.</summary>
        public IntroPose PoseAt(float t)
        {
            var p = reducedNow ? IntroTimeline.EvaluateReduced(t) : IntroTimeline.Evaluate(t);
            if (skippedAt >= 0f) p.cover *= 1f - Mathf.Clamp01((t - skippedAt) / IntroTimeline.SkipFade);
            return p;
        }

        public bool IsDone(float t)
        {
            if (skippedAt >= 0f && t >= skippedAt + IntroTimeline.SkipFade) return true;
            return t >= (reducedNow ? IntroTimeline.ReducedEnd : IntroTimeline.End);
        }

        /// <summary>Shows the intro at <paramref name="t"/> seconds without playing it. The rig is
        /// built if needed (without the overlay canvas, so the caller renders
        /// <see cref="IntroCamera"/> itself); <see cref="Teardown"/> removes it.</summary>
        public void Evaluate(float t, bool reduced = false)
        {
            reducedNow = reduced;
            time = t;
            EnsureRig(false);
            Apply(PoseAt(t));
        }

        void Finish()
        {
            playing = false;
            finished = true;
            Teardown();
            Completed?.Invoke();
            Completed = null;
            completed?.Invoke();
            if (destroyOnComplete && Application.isPlaying) Destroy(gameObject);
        }

        static bool AnyInput()
        {
#if DC_INTRO_INPUT_SYSTEM
            if (Keyboard.current != null && Keyboard.current.anyKey.wasPressedThisFrame) return true;
            if (Mouse.current != null && (Mouse.current.leftButton.wasPressedThisFrame || Mouse.current.rightButton.wasPressedThisFrame)) return true;
            if (Touchscreen.current != null && Touchscreen.current.primaryTouch.press.wasPressedThisFrame) return true;
            if (Gamepad.current != null)
                foreach (var c in Gamepad.current.allControls)
                    if (c is UnityEngine.InputSystem.Controls.ButtonControl b && b.wasPressedThisFrame) return true;
#endif
#if ENABLE_LEGACY_INPUT_MANAGER
            if (Input.anyKeyDown) return true;
#endif
            return false;
        }

        // ---------------------------------------------------------------------------- the rig

        void EnsureRig(bool withOverlay)
        {
            if (rig == null) BuildRig();
            if (withOverlay) EnsureOutput();
        }

        T Own<T>(T o) where T : UnityEngine.Object
        {
            owned.Add(o);
            return o;
        }

        Material Glow(float intensity, float softness, Color colour, float head = 0f, int mode = 0)
        {
            var m = Own(new Material(glowMaterial) { name = glowMaterial.name + " (intro)" });
            m.SetFloat("_Intensity", intensity);
            m.SetFloat("_Softness", softness);
            m.SetColor("_Color", colour);
            m.SetFloat("_Head", head);
            m.SetFloat("_Mode", mode);
            return m;
        }

        static Color Hex(string hex)
        {
            ColorUtility.TryParseHtmlString(hex, out var c);
            return c.linear;
        }

        void BuildRig()
        {
            rig = new GameObject("DemoIntro Rig") { hideFlags = HideFlags.DontSave };
            rig.transform.position = RigOrigin;
            var model = Instantiate(mark, rig.transform, false);
            model.hideFlags = HideFlags.DontSave;
            frame = Find(model.transform, "Frame");
            cube = Find(model.transform, "Cube");
            wordmark = Find(model.transform, "Wordmark");
            if (frame == null || cube == null || wordmark == null)
                throw new InvalidOperationException("[DemoIntro] the mark needs Frame, Cube and Wordmark nodes");
            // the cube spins within the frame
            cube.SetParent(frame, true);
            wordmarkRest = wordmark.localPosition;

            matMark = Own(new Material(markMaterial) { name = markMaterial.name + " (intro)" });
            matWord = Own(new Material(markMaterial) { name = markMaterial.name + " (wordmark)" });
            matWord.SetFloat("_Flat", 1f);
            frame.GetComponent<Renderer>().sharedMaterial = matMark;
            cube.GetComponent<Renderer>().sharedMaterial = matMark;
            wordmark.GetComponent<Renderer>().sharedMaterial = matWord;
            foreach (var r in model.GetComponentsInChildren<Renderer>(true))
            {
                r.shadowCastingMode = ShadowCastingMode.Off;
                r.receiveShadows = false;
                r.lightProbeUsage = LightProbeUsage.Off;
                r.reflectionProbeUsage = ReflectionProbeUsage.Off;
            }

            // brand colours only: the lightest facet for the rim light, the mid blue for its glow
            var light = Hex("#9ACEEC");
            var mid = Hex("#208CB7");
            var soft = Hex("#7AB7D9");
            matTraceCore = Glow(TraceCore, 3f, light, 2.5f);
            matTraceHalo = Glow(TraceHalo, 4.5f, mid, 1.5f);
            matCubeCore = Glow(0.9f, 3f, light);
            matCubeHalo = Glow(0.35f, 4.5f, mid);
            matPulse = Glow(1.2f, 4f, light);
            matParticles = Glow(0.8f, 9f, soft, 0f, 1);
            matBackdrop = Glow(0.42f, 1.7f, Hex("#1F425E"), 0f, 1);
            matBackdrop.renderQueue = (int)RenderQueue.Transparent - 50;     // behind the mark

            // the rim light: the frame's outer edge from the top, clockwise, and its inner edge
            // from the top the other way, so the two meet at the bottom
            var outer = Hexagon(OuterRadius * 1.003f, -0.002f, false);
            var inner = Hexagon(InnerRadius * 0.996f, -Bevel - 0.002f, true);
            Line("Rim Outer", frame, outer, true, 0.0075f, matTraceCore);
            Line("Rim Outer Glow", frame, outer, true, 0.09f, matTraceHalo);
            Line("Rim Inner", frame, inner, true, 0.006f, matTraceCore);
            Line("Rim Inner Glow", frame, inner, true, 0.06f, matTraceHalo);

            // the cube's edges
            var c = CubeCorners(1.004f);
            for (int a = 0; a < 8; a++)
                for (int bit = 1; bit < 8; bit <<= 1)
                    if ((a & bit) == 0)
                    {
                        var seg = new[] { c[a], c[a | bit] };
                        Line("Cube Edge", cube, seg, false, 0.004f, matCubeCore);
                        Line("Cube Edge Glow", cube, seg, false, 0.05f, matCubeHalo);
                    }

            // the lock pulse: a hexagon that grows out of the frame and fades
            pulse = new GameObject("Pulse").transform;
            pulse.SetParent(rig.transform, false);
            Line("Pulse Line", pulse, Hexagon(OuterRadius, -0.003f, false), true, 0.05f, matPulse);

            BuildParticles();
            BuildBackdrop();

            var camGo = new GameObject("DemoIntro Camera") { hideFlags = HideFlags.DontSave };
            camGo.transform.SetParent(rig.transform, false);
            cam = camGo.AddComponent<Camera>();
            cam.orthographic = true;
            cam.nearClipPlane = 0.1f;
            cam.farClipPlane = 20f;
            cam.clearFlags = CameraClearFlags.SolidColor;
            cam.backgroundColor = background;
            cam.allowMSAA = true;
            cam.allowHDR = false;
            cam.useOcclusionCulling = false;
            cam.enabled = false;                      // renders only when it has a target
            var data = camGo.AddComponent<UniversalAdditionalCameraData>();
            data.renderPostProcessing = false;
            data.antialiasing = AntialiasingMode.None;
            data.renderShadows = false;
            data.requiresColorOption = CameraOverrideOption.Off;
            data.requiresDepthOption = CameraOverrideOption.Off;
            SetLayer(rig.transform, gameObject.layer);
        }

        static void SetLayer(Transform t, int layer)
        {
            t.gameObject.layer = layer;
            foreach (Transform c in t) SetLayer(c, layer);
        }

        void EnsureOutput()
        {
            int w = Mathf.Max(Screen.width, 16), h = Mathf.Max(Screen.height, 16);
            if (target == null || target.width != w || target.height != h)
            {
                if (target != null)
                {
                    cam.targetTexture = null;
                    DestroyObj(target);
                }
                target = new RenderTexture(w, h, 24, RenderTextureFormat.ARGB32, RenderTextureReadWrite.sRGB)
                {
                    antiAliasing = 8,
                    name = "DemoIntro",
                    hideFlags = HideFlags.DontSave,
                };
                cam.targetTexture = target;
                cam.enabled = true;
                if (image != null) image.texture = target;
            }
            if (canvas == null)
            {
                var go = new GameObject("DemoIntro Overlay") { hideFlags = HideFlags.DontSave };
                go.transform.SetParent(rig.transform, false);
                canvas = go.AddComponent<Canvas>();
                canvas.renderMode = RenderMode.ScreenSpaceOverlay;
                canvas.sortingOrder = sortingOrder;
                var img = new GameObject("Image");
                img.transform.SetParent(go.transform, false);
                image = img.AddComponent<RawImage>();
                image.texture = target;
                image.raycastTarget = true;           // the intro holds the pointer while it covers
                var rt = image.rectTransform;
                rt.anchorMin = Vector2.zero;
                rt.anchorMax = Vector2.one;
                rt.offsetMin = rt.offsetMax = Vector2.zero;
            }
        }

        /// <summary>Removes the rig, its camera, overlay and render texture.</summary>
        public void Teardown()
        {
            if (cam != null) cam.targetTexture = null;
            DestroyObj(rig);
            DestroyObj(target);
            foreach (var o in owned) DestroyObj(o);
            owned.Clear();
            rig = null;
            target = null;
            canvas = null;
            image = null;
            cam = null;
        }

        void OnDestroy() => Teardown();

        static void DestroyObj(UnityEngine.Object o)
        {
            if (o == null) return;
            if (Application.isPlaying) Destroy(o);
            else DestroyImmediate(o);
        }

        static Transform Find(Transform t, string name)
        {
            if (t.name == name) return t;
            foreach (Transform c in t)
            {
                var f = Find(c, name);
                if (f != null) return f;
            }
            return null;
        }

        /// <summary>A pointy-top hexagon from its top corner, clockwise as the viewer sees it
        /// (or counter-clockwise), at depth <paramref name="z"/>.</summary>
        public static Vector3[] Hexagon(float radius, float z, bool counterClockwise)
        {
            var pts = new Vector3[6];
            for (int k = 0; k < 6; k++)
            {
                float a = Mathf.Deg2Rad * 60f * (counterClockwise ? -k : k);
                pts[k] = new Vector3(radius * Mathf.Sin(a), radius * Mathf.Cos(a), z);
            }
            return pts;
        }

        /// <summary>The cube's corners at the lock, indexed by bits (1, 2, 4) = steps along its
        /// three edges from the corner nearest the viewer (build_mark.py builds the same cube).</summary>
        public static Vector3[] CubeCorners(float scale = 1f)
        {
            float p = CubeProjectedEdge;
            float edge = p * Mathf.Sqrt(1.5f);
            float back = Mathf.Sqrt(edge * edge - p * p);
            var e = new[]
            {
                new Vector3(-p * Mathf.Cos(Mathf.PI / 6f), p * 0.5f, back),
                new Vector3(p * Mathf.Cos(Mathf.PI / 6f), p * 0.5f, back),
                new Vector3(0f, -p, back),
            };
            var f = new Vector3(0f, 0f, -1.5f * back);
            var c = new Vector3[8];
            for (int i = 0; i < 8; i++)
                c[i] = (f + ((i & 1) != 0 ? e[0] : Vector3.zero) + ((i & 2) != 0 ? e[1] : Vector3.zero) + ((i & 4) != 0 ? e[2] : Vector3.zero)) * scale;
            return c;
        }

        LineRenderer Line(string name, Transform parent, Vector3[] pts, bool loop, float width, Material mat)
        {
            var go = new GameObject(name);
            go.transform.SetParent(parent, false);
            var lr = go.AddComponent<LineRenderer>();
            lr.useWorldSpace = false;
            lr.loop = loop;
            lr.positionCount = pts.Length;
            lr.SetPositions(pts);
            lr.widthMultiplier = width;
            lr.alignment = LineAlignment.View;
            lr.textureMode = LineTextureMode.Stretch;
            lr.numCornerVertices = 3;
            lr.numCapVertices = 3;
            lr.shadowCastingMode = ShadowCastingMode.Off;
            lr.receiveShadows = false;
            lr.lightProbeUsage = LightProbeUsage.Off;
            lr.reflectionProbeUsage = ReflectionProbeUsage.Off;
            lr.sharedMaterial = mat;
            return lr;
        }

        const float TraceCore = 3.5f, TraceHalo = 0.9f;
        const int ParticleCount = 18;

        void BuildParticles()
        {
            var rnd = new System.Random(20261005);
            float R(float a, float b) => a + (float)rnd.NextDouble() * (b - a);
            particles = new Particle[ParticleCount];
            for (int i = 0; i < ParticleCount; i++)
            {
                // spread round the mark, none in front of its centre
                Vector3 s;
                do s = new Vector3(R(-2.4f, 2.4f), R(-1.5f, 1.3f), R(-0.8f, 1.2f));
                while (Mathf.Abs(s.x) < 0.7f && Mathf.Abs(s.y) < 0.7f);
                particles[i] = new Particle
                {
                    start = s,
                    velocity = new Vector3(R(-0.05f, 0.05f), R(0.02f, 0.09f), 0f),
                    size = R(0.03f, 0.06f),
                    phase = R(0f, 6.283f),
                    rate = R(1.2f, 3.2f),
                    brightness = R(0.25f, 0.8f),
                };
            }
            particleVerts = new Vector3[ParticleCount * 4];
            particleColours = new Color[ParticleCount * 4];
            var uv = new Vector2[ParticleCount * 4];
            var tris = new int[ParticleCount * 6];
            for (int i = 0; i < ParticleCount; i++)
            {
                uv[i * 4] = new Vector2(0, 0);
                uv[i * 4 + 1] = new Vector2(1, 0);
                uv[i * 4 + 2] = new Vector2(1, 1);
                uv[i * 4 + 3] = new Vector2(0, 1);
                int v = i * 4, k = i * 6;
                tris[k] = v; tris[k + 1] = v + 2; tris[k + 2] = v + 1;
                tris[k + 3] = v; tris[k + 4] = v + 3; tris[k + 5] = v + 2;
            }
            particleMesh = Own(new Mesh { name = "DemoIntro Particles", hideFlags = HideFlags.DontSave });
            particleMesh.MarkDynamic();
            particleMesh.vertices = particleVerts;
            particleMesh.uv = uv;
            particleMesh.colors = particleColours;
            particleMesh.triangles = tris;
            particleMesh.bounds = new Bounds(Vector3.zero, new Vector3(10f, 10f, 10f));
            var go = new GameObject("Particles");
            go.transform.SetParent(rig.transform, false);
            go.AddComponent<MeshFilter>().sharedMesh = particleMesh;
            var mr = go.AddComponent<MeshRenderer>();
            mr.sharedMaterial = matParticles;
            mr.shadowCastingMode = ShadowCastingMode.Off;
            mr.receiveShadows = false;
        }

        void UpdateParticles(float t, float alpha)
        {
            for (int i = 0; i < ParticleCount; i++)
            {
                var p = particles[i];
                var c = p.start + p.velocity * t;
                float h = p.size * 0.5f;
                int v = i * 4;
                particleVerts[v] = c + new Vector3(-h, -h, 0f);
                particleVerts[v + 1] = c + new Vector3(h, -h, 0f);
                particleVerts[v + 2] = c + new Vector3(h, h, 0f);
                particleVerts[v + 3] = c + new Vector3(-h, h, 0f);
                float tw = 0.55f + 0.45f * Mathf.Sin(p.phase + p.rate * t);
                var col = new Color(1f, 1f, 1f, alpha * p.brightness * tw);
                particleColours[v] = particleColours[v + 1] = particleColours[v + 2] = particleColours[v + 3] = col;
            }
            particleMesh.vertices = particleVerts;
            particleMesh.colors = particleColours;
        }

        void BuildBackdrop()
        {
            var mesh = Own(new Mesh { name = "DemoIntro Backdrop", hideFlags = HideFlags.DontSave });
            const float s = 7f;
            mesh.vertices = new[] { new Vector3(-s, -s, 0), new Vector3(s, -s, 0), new Vector3(s, s, 0), new Vector3(-s, s, 0) };
            mesh.uv = new[] { new Vector2(0, 0), new Vector2(1, 0), new Vector2(1, 1), new Vector2(0, 1) };
            mesh.colors = new[] { Color.white, Color.white, Color.white, Color.white };
            mesh.triangles = new[] { 0, 2, 1, 0, 3, 2 };
            var go = new GameObject("Backdrop");
            go.transform.SetParent(rig.transform, false);
            go.transform.localPosition = new Vector3(0f, -0.05f, 4f);
            go.AddComponent<MeshFilter>().sharedMesh = mesh;
            var mr = go.AddComponent<MeshRenderer>();
            mr.sharedMaterial = matBackdrop;
            mr.shadowCastingMode = ShadowCastingMode.Off;
        }

        // ---------------------------------------------------------------------------- applying a pose

        void Apply(IntroPose p)
        {
            var origin = rig.transform.position;
            frame.localRotation = p.frameRotation;
            cube.localRotation = p.cubeRotation;
            cube.localScale = Vector3.one * p.cubeScale;

            matMark.SetFloat("_Flat", p.flat);
            matMark.SetFloat("_Light", p.light);
            matMark.SetFloat("_SweepPos", p.sweep);
            matMark.SetVector("_Origin", origin);
            matWord.SetFloat("_Alpha", p.wordmark);
            wordmark.gameObject.SetActive(p.wordmark > 0.001f);
            wordmark.localPosition = wordmarkRest + new Vector3(0f, -0.035f * (1f - p.wordmark), 0f);

            matTraceCore.SetFloat("_Trace", p.trace);
            matTraceHalo.SetFloat("_Trace", p.trace);
            matTraceCore.SetFloat("_Intensity", TraceCore * p.glow);
            matTraceHalo.SetFloat("_Intensity", TraceHalo * p.glow);
            matCubeCore.SetFloat("_Intensity", 0.9f * p.cubeGlow);
            matCubeHalo.SetFloat("_Intensity", 0.35f * p.cubeGlow);

            bool pulsing = p.pulse > 0f && p.pulse < 1f;
            pulse.gameObject.SetActive(pulsing);
            if (pulsing)
            {
                float e = 1f - (1f - p.pulse) * (1f - p.pulse);
                pulse.localScale = Vector3.one * (1f + 0.22f * e);
                float fade = (1f - p.pulse);
                matPulse.SetFloat("_Intensity", 0.9f * fade * fade);
            }

            matParticles.SetFloat("_Intensity", 1f);
            UpdateParticles(time > 0f ? time : 0f, p.particles);
            matBackdrop.SetFloat("_Intensity", 0.42f * p.backdrop);

            // the view: the lockup centred, pushing in toward the cube as the intro dissolves
            float aspect = cam.targetTexture != null ? (float)cam.targetTexture.width / cam.targetTexture.height
                : Screen.height > 0 ? (float)Screen.width / Screen.height : 16f / 9f;
            float half = Mathf.Max(ViewHalfHeight, (LockupHalfWidth + 0.35f) / Mathf.Max(aspect, 0.1f));
            float push = p.push;
            cam.orthographicSize = half * Mathf.Lerp(1f, 0.28f, push);
            cam.transform.localPosition = new Vector3(0f, Mathf.Lerp(LockupCentreY, 0f, push), -5f);
            cam.transform.localRotation = Quaternion.identity;

            if (image != null) image.color = new Color(1f, 1f, 1f, p.cover);
        }
    }
}
