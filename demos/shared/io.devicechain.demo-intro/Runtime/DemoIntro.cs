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
    /// own cameras draw, then opens onto it, so the demo needs no changes beyond adding this
    /// component (or calling <see cref="Play(Action)"/>):
    /// <list type="bullet">
    /// <item>the intro draws with its own orthographic camera, far from the scene, into a
    /// high-dynamic-range image, blooms it with its own pass, and shows the result full screen
    /// over the demo. Its camera skips post-processing, so a demo's look never reaches it and at
    /// the lock the mark is the exact brand colours;</item>
    /// <item>the image carries alpha (premultiplied): the exit is the frame's inner hexagon
    /// opening out, and where it is open the demo shows through;</item>
    /// <item>any key, click, tap or gamepad button skips it (a short fade);</item>
    /// <item>reduced motion shows the still lockup and fades: set <see cref="reducedMotion"/>,
    /// <see cref="PreferReducedMotion"/>, or start the player with <c>-reduced-motion</c>;</item>
    /// <item>a demo disables it with <see cref="Disabled"/> (before the scene loads) or the
    /// <c>-no-intro</c> command-line flag; it then completes at once.</item>
    /// </list>
    /// What the intro shows is a pure function of time (<see cref="IntroTimeline"/>), and
    /// <see cref="Evaluate"/> with <see cref="RenderInto"/> shows any moment of it, which is how a
    /// frame sequence is captured.
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
        [Tooltip("Template for the rim light, the edge light, the pulse, the packets and the backdrop (DeviceChain/Intro/Glow).")]
        public Material glowMaterial;
        [Tooltip("The exit's opening (DeviceChain/Intro/Hole).")]
        public Material holeMaterial;
        [Tooltip("The intro's bloom (Hidden/DeviceChain/Intro/Bloom).")]
        public Material bloomMaterial;
        [Tooltip("Shows the intro on the overlay canvas (Hidden/DeviceChain/Intro/Overlay).")]
        public Material overlayMaterial;
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
        [Tooltip("Invoked once when the intro has opened onto the demo (or was disabled).")]
        public UnityEvent completed = new UnityEvent();

        /// <summary>Raised once when the intro has opened onto the demo (or was disabled).</summary>
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
        /// <summary>The wordmark's rise as it fades in, in pixels at 1080 lines.</summary>
        public const float WordmarkRisePixels = 12f;

        float time;
        float skippedAt = -1f;
        bool playing, finished;
        bool reducedNow;
        float bloomNow;
        float captureAspect = 16f / 9f;

        // the rig
        GameObject rig;
        Transform frame, cube, wordmark, pulse, hole;
        LineRenderer pulseLine;
        Camera cam;
        RenderTexture hdr, target;
        Canvas canvas;
        RawImage image;
        Material matFrame, matCube, matWord, matTraceCore, matTraceHalo, matCubeCore, matPulse, matPackets, matBackdrop, matHole, matBloom;
        Mesh packetMesh;
        Packet[] packets;
        Vector3[] packetVerts;
        Color[] packetColours;
        Vector3 wordmarkRest;
        readonly List<UnityEngine.Object> owned = new List<UnityEngine.Object>();

        struct Packet
        {
            public Vector2 dir, side;
            public float r0, lateral, start, arrive, width, brightness;
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
        /// when it has opened onto the demo, or at once when intros are disabled.</summary>
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
            if (mark == null || markMaterial == null || glowMaterial == null || holeMaterial == null || bloomMaterial == null || overlayMaterial == null)
            {
                Debug.LogError("[DemoIntro] the mark or one of its materials is not set; skipping the intro");
                Finish();
                return;
            }
            reducedNow = reducedMotion || PreferReducedMotion;
            time = 0f;
            skippedAt = -1f;
            playing = true;
            EnsureRig(true);
            Apply(PoseAt(0f));
            RenderInto(target);
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
            RenderInto(target);
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

        /// <summary>Poses the intro at <paramref name="t"/> seconds without playing it, framed for an
        /// image of <paramref name="aspect"/> (width over height). The rig is built if needed,
        /// without the overlay canvas, so the caller renders it with <see cref="RenderInto"/>;
        /// <see cref="Teardown"/> removes it.</summary>
        public void Evaluate(float t, bool reduced = false, float aspect = 16f / 9f)
        {
            reducedNow = reduced;
            time = t;
            captureAspect = aspect;
            EnsureRig(false);
            Apply(PoseAt(t));
        }

        /// <summary>Renders the intro as posed into <paramref name="destination"/>: the camera's
        /// high-dynamic-range image, bloomed, with premultiplied alpha (clear where the exit has
        /// opened).</summary>
        public void RenderInto(RenderTexture destination)
        {
            if (cam == null || destination == null) return;
            if (hdr == null || hdr.width != destination.width || hdr.height != destination.height)
            {
                if (hdr != null)
                {
                    cam.targetTexture = null;
                    DestroyObj(hdr);
                }
                hdr = new RenderTexture(destination.width, destination.height, 24, RenderTextureFormat.ARGBHalf, RenderTextureReadWrite.Linear)
                {
                    antiAliasing = 8,
                    name = "DemoIntro HDR",
                    hideFlags = HideFlags.DontSave,
                };
            }
            cam.targetTexture = hdr;
            cam.Render();
            Bloom(hdr, destination, bloomNow);
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

        // ---------------------------------------------------------------------------- the bloom

        const int BloomLevels = 6;
        const float BloomThreshold = 1f, BloomScatter = 0.7f;
        static readonly int MainTexId = Shader.PropertyToID("_MainTex"), HighTexId = Shader.PropertyToID("_HighTex"),
            BloomTexId = Shader.PropertyToID("_BloomTex"), IntensityId = Shader.PropertyToID("_Intensity"),
            ThresholdId = Shader.PropertyToID("_Threshold"), ScatterId = Shader.PropertyToID("_Scatter");

        /// <summary>The intro's bloom, after URP's: threshold and halve, halve again down a short
        /// chain, then upsample back mixing each level by the scatter. With no bloom the image is
        /// copied as it is, so the locked mark's colours are untouched.</summary>
        void Bloom(RenderTexture source, RenderTexture destination, float intensity)
        {
            matBloom.SetFloat(IntensityId, intensity);
            if (intensity <= 0f)
            {
                matBloom.SetTexture(BloomTexId, Texture2D.blackTexture);
                Graphics.Blit(source, destination, matBloom, 3);
                return;
            }
            matBloom.SetFloat(ThresholdId, BloomThreshold);
            matBloom.SetFloat(ScatterId, Mathf.Lerp(0.05f, 0.95f, BloomScatter));
            var levels = new List<RenderTexture>();
            int w = Mathf.Max(source.width / 2, 1), h = Mathf.Max(source.height / 2, 1);
            var down = RenderTexture.GetTemporary(w, h, 0, RenderTextureFormat.ARGBHalf, RenderTextureReadWrite.Linear);
            Graphics.Blit(source, down, matBloom, 0);
            levels.Add(down);
            for (int i = 1; i < BloomLevels && Mathf.Min(w, h) > 8; i++)
            {
                w = Mathf.Max(w / 2, 1);
                h = Mathf.Max(h / 2, 1);
                var next = RenderTexture.GetTemporary(w, h, 0, RenderTextureFormat.ARGBHalf, RenderTextureReadWrite.Linear);
                Graphics.Blit(down, next, matBloom, 1);
                levels.Add(next);
                down = next;
            }
            var up = levels[levels.Count - 1];
            var made = new List<RenderTexture>();
            for (int i = levels.Count - 2; i >= 0; i--)
            {
                var into = RenderTexture.GetTemporary(levels[i].width, levels[i].height, 0, RenderTextureFormat.ARGBHalf, RenderTextureReadWrite.Linear);
                matBloom.SetTexture(HighTexId, levels[i]);
                Graphics.Blit(up, into, matBloom, 2);
                made.Add(into);
                up = into;
            }
            matBloom.SetTexture(BloomTexId, up);
            Graphics.Blit(source, destination, matBloom, 3);
            matBloom.SetTexture(BloomTexId, Texture2D.blackTexture);
            matBloom.SetTexture(HighTexId, Texture2D.blackTexture);
            foreach (var rt in levels) RenderTexture.ReleaseTemporary(rt);
            foreach (var rt in made) RenderTexture.ReleaseTemporary(rt);
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

        /// <summary>A brand colour, linear, for the glow's HDR colour, which reaches the shader as
        /// set (a non-HDR colour property is set as written, in sRGB, and Unity linearises it).</summary>
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
            // the cube sits within the frame and grows with it as the exit opens
            cube.SetParent(frame, true);
            wordmarkRest = wordmark.localPosition;

            matFrame = Own(new Material(markMaterial) { name = markMaterial.name + " (frame)" });
            matCube = Own(new Material(markMaterial) { name = markMaterial.name + " (cube)" });
            matWord = Own(new Material(markMaterial) { name = markMaterial.name + " (wordmark)" });
            matWord.SetFloat("_Flat", 1f);
            matWord.SetFloat("_Chamfer", 0f);
            frame.GetComponent<Renderer>().sharedMaterial = matFrame;
            cube.GetComponent<Renderer>().sharedMaterial = matCube;
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
            matCubeCore = Glow(CubeCore, 3f, light);
            matPulse = Glow(0.6f, 2f, soft);
            matPackets = Glow(1f, 3f, light, 0f, 2);
            matBackdrop = Glow(BackdropGlow, 1.7f, Hex("#1F425E"), 0f, 1);
            matBackdrop.SetColor("_Base", background);
            matBackdrop.SetFloat("_DstBlend", (float)BlendMode.Zero);
            matBackdrop.renderQueue = (int)RenderQueue.Transparent - 50;     // behind the mark

            // the rim light: the frame's outer edge from the top, clockwise, and its inner edge
            // from the top the other way, so the two meet at the bottom. The bloom spreads it; the
            // halo lines only soften it.
            var outer = Hexagon(OuterRadius * 1.003f, -0.002f, false);
            var inner = Hexagon(InnerRadius * 0.996f, -Bevel - 0.002f, true);
            Line("Rim Outer", frame, outer, true, 0.0075f, matTraceCore);
            Line("Rim Outer Glow", frame, outer, true, 0.045f, matTraceHalo);
            Line("Rim Inner", frame, inner, true, 0.006f, matTraceCore);
            Line("Rim Inner Glow", frame, inner, true, 0.03f, matTraceHalo);

            // the light on the cube's edges: thin lines, no caps (a cap pokes out past a corner)
            var c = CubeCorners(1.004f);
            for (int a = 0; a < 8; a++)
                for (int bit = 1; bit < 8; bit <<= 1)
                    if ((a & bit) == 0)
                        Line("Cube Edge", cube, new[] { c[a], c[a | bit] }, false, 0.004f, matCubeCore, 0);

            // the lock pulse: the frame's outline, which grows, thins and fades
            pulse = new GameObject("Pulse").transform;
            pulse.SetParent(rig.transform, false);
            pulseLine = Line("Pulse Line", pulse, Hexagon(OuterRadius, -0.003f, false), true, 0.01f, matPulse);

            // the exit's opening: the inner hexagon, a hair larger so the frame overlaps its edge
            matHole = Own(new Material(holeMaterial) { name = holeMaterial.name + " (intro)" });
            var holeMesh = Own(new Mesh { name = "DemoIntro Opening", hideFlags = HideFlags.DontSave });
            var hv = new Vector3[7];
            var hexagon = Hexagon(InnerRadius * 1.01f, 0f, false);
            for (int k = 0; k < 6; k++) hv[k + 1] = hexagon[k];
            var ht = new int[18];
            for (int k = 0; k < 6; k++)
            {
                ht[k * 3] = 0;
                ht[k * 3 + 1] = k + 1;
                ht[k * 3 + 2] = (k + 1) % 6 + 1;
            }
            holeMesh.vertices = hv;
            holeMesh.triangles = ht;
            hole = new GameObject("Opening").transform;
            hole.SetParent(rig.transform, false);
            hole.localPosition = new Vector3(0f, 0f, 0.3f);
            hole.gameObject.AddComponent<MeshFilter>().sharedMesh = holeMesh;
            var hr = hole.gameObject.AddComponent<MeshRenderer>();
            hr.sharedMaterial = matHole;
            hr.shadowCastingMode = ShadowCastingMode.Off;
            hr.receiveShadows = false;

            matBloom = Own(new Material(bloomMaterial) { name = bloomMaterial.name + " (intro)" });

            BuildPackets();
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
            cam.allowHDR = true;
            cam.useOcclusionCulling = false;
            cam.enabled = false;                      // renders only when the intro asks it to
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
                if (target != null) DestroyObj(target);
                target = new RenderTexture(w, h, 0, RenderTextureFormat.ARGB32, RenderTextureReadWrite.sRGB)
                {
                    name = "DemoIntro",
                    hideFlags = HideFlags.DontSave,
                };
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
                image.material = overlayMaterial;
                image.raycastTarget = true;           // the intro holds the pointer while it covers
                var rt = image.rectTransform;
                rt.anchorMin = Vector2.zero;
                rt.anchorMax = Vector2.one;
                rt.offsetMin = rt.offsetMax = Vector2.zero;
            }
        }

        /// <summary>Removes the rig, its camera, overlay and images.</summary>
        public void Teardown()
        {
            if (cam != null) cam.targetTexture = null;
            DestroyObj(rig);
            DestroyObj(target);
            DestroyObj(hdr);
            foreach (var o in owned) DestroyObj(o);
            owned.Clear();
            rig = null;
            target = null;
            hdr = null;
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

        LineRenderer Line(string name, Transform parent, Vector3[] pts, bool loop, float width, Material mat, int caps = 3)
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
            lr.numCapVertices = caps;
            lr.shadowCastingMode = ShadowCastingMode.Off;
            lr.receiveShadows = false;
            lr.lightProbeUsage = LightProbeUsage.Off;
            lr.reflectionProbeUsage = ReflectionProbeUsage.Off;
            lr.sharedMaterial = mat;
            return lr;
        }

        const float TraceCore = 3.5f, TraceHalo = 0.6f, CubeCore = 1.3f, BackdropGlow = 0.42f;

        // ---------------------------------------------------------------------------- the packets

        /// <summary>How many data packets travel in toward the hexagon's six corners.</summary>
        public const int PacketCount = 48;
        /// <summary>The packets' motion blur: how long the virtual shutter is open, in seconds.</summary>
        const float Shutter = 1f / 30f;

        void BuildPackets()
        {
            var rnd = new System.Random(20261005);
            float R(float a, float b) => a + (float)rnd.NextDouble() * (b - a);
            packets = new Packet[PacketCount];
            for (int i = 0; i < PacketCount; i++)
            {
                // round the six corners in turn, each arriving at its own moment before the lock
                float ang = Mathf.Deg2Rad * 60f * (i % 6);
                var dir = new Vector2(Mathf.Sin(ang), Mathf.Cos(ang));
                float arrive = R(0.95f, IntroTimeline.Lock - 0.06f);
                packets[i] = new Packet
                {
                    dir = dir,
                    side = new Vector2(-dir.y, dir.x),
                    r0 = R(1.5f, 3.2f),
                    lateral = R(-0.14f, 0.14f),
                    arrive = arrive,
                    start = arrive - R(0.55f, 0.85f),
                    width = R(2f, 4f),
                    brightness = R(1.3f, 2.2f),
                };
            }
            packetVerts = new Vector3[PacketCount * 4];
            packetColours = new Color[PacketCount * 4];
            var uv = new Vector2[PacketCount * 4];
            var tris = new int[PacketCount * 6];
            for (int i = 0; i < PacketCount; i++)
            {
                uv[i * 4] = new Vector2(0, 0);
                uv[i * 4 + 1] = new Vector2(0, 1);
                uv[i * 4 + 2] = new Vector2(1, 1);
                uv[i * 4 + 3] = new Vector2(1, 0);
                int v = i * 4, k = i * 6;
                tris[k] = v; tris[k + 1] = v + 1; tris[k + 2] = v + 2;
                tris[k + 3] = v; tris[k + 4] = v + 2; tris[k + 5] = v + 3;
            }
            packetMesh = Own(new Mesh { name = "DemoIntro Packets", hideFlags = HideFlags.DontSave });
            packetMesh.MarkDynamic();
            packetMesh.vertices = packetVerts;
            packetMesh.uv = uv;
            packetMesh.colors = packetColours;
            packetMesh.triangles = tris;
            packetMesh.bounds = new Bounds(Vector3.zero, new Vector3(10f, 10f, 10f));
            var go = new GameObject("Packets");
            go.transform.SetParent(rig.transform, false);
            go.AddComponent<MeshFilter>().sharedMesh = packetMesh;
            var mr = go.AddComponent<MeshRenderer>();
            mr.sharedMaterial = matPackets;
            mr.shadowCastingMode = ShadowCastingMode.Off;
            mr.receiveShadows = false;
        }

        /// <summary>Where packet <paramref name="i"/> is at <paramref name="t"/> (in the rig's
        /// plane), and its opacity; a packet accelerates in and is absorbed at its corner.</summary>
        public Vector2 PacketPosition(int i, float t, out float alpha)
        {
            var p = packets[i];
            float u = (t - p.start) / (p.arrive - p.start);
            alpha = u <= 0f || u >= 1f ? 0f : Mathf.Clamp01(u / 0.2f) * Mathf.Clamp01((1f - u) / 0.08f);
            u = Mathf.Clamp01(u);
            float e = u * u;
            float r = Mathf.Lerp(p.r0, OuterRadius, e);
            return p.dir * r + p.side * (p.lateral * (1f - e));
        }

        void UpdatePackets(float t, float shown, float pixel)
        {
            for (int i = 0; i < PacketCount; i++)
            {
                var head = PacketPosition(i, t, out float a);
                var tail = PacketPosition(i, t - Shutter, out _);
                var along = head - tail;
                float len = along.magnitude;
                var dir = len > 1e-6f ? along / len : packets[i].dir;
                len = Mathf.Max(len, packets[i].width * pixel);
                tail = head - dir * len;
                var side = new Vector2(-dir.y, dir.x) * (packets[i].width * pixel * 0.5f);
                const float z = -0.15f;
                int v = i * 4;
                packetVerts[v] = new Vector3(tail.x - side.x, tail.y - side.y, z);
                packetVerts[v + 1] = new Vector3(tail.x + side.x, tail.y + side.y, z);
                packetVerts[v + 2] = new Vector3(head.x + side.x, head.y + side.y, z);
                packetVerts[v + 3] = new Vector3(head.x - side.x, head.y - side.y, z);
                var col = new Color(1f, 1f, 1f, a * shown * packets[i].brightness);
                packetColours[v] = packetColours[v + 1] = packetColours[v + 2] = packetColours[v + 3] = col;
            }
            packetMesh.vertices = packetVerts;
            packetMesh.colors = packetColours;
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
            // the backdrop's colour at the centre of its glow, where the mark is (summed in linear)
            var darkNow = ((Color)((Vector4)background.linear + (Vector4)matBackdrop.GetColor("_Color") * (BackdropGlow * p.backdrop))).gamma;
            frame.localRotation = p.frameRotation;
            frame.localScale = Vector3.one * p.holeScale;
            cube.localRotation = Quaternion.Inverse(p.frameRotation) * p.cubeRotation;
            cube.localScale = Vector3.one * p.cubeScale;

            foreach (var m in new[] { matFrame, matCube })
            {
                m.SetFloat("_Flat", p.flat);
                m.SetFloat("_Chamfer", p.chamfer);
                m.SetFloat("_Light", p.light);
                m.SetColor("_Dark", darkNow);
                m.SetFloat("_SweepPos", p.sweep);
                m.SetVector("_Origin", origin);
            }
            matCube.SetFloat("_Alpha", p.cubeAlpha);
            cube.gameObject.SetActive(p.cubeAlpha > 0.001f);

            // the view: the lockup centred, fitted to the image's shape
            float aspect = target != null ? (float)target.width / target.height : captureAspect;
            float half = Mathf.Max(ViewHalfHeight, (LockupHalfWidth + 0.35f) / Mathf.Max(aspect, 0.1f));
            cam.orthographicSize = half;
            cam.transform.localPosition = new Vector3(0f, LockupCentreY, -5f);
            cam.transform.localRotation = Quaternion.identity;
            float pixel = 2f * half / 1080f;                 // one pixel at 1080 lines, in units

            matWord.SetFloat("_Alpha", p.wordmark);
            wordmark.gameObject.SetActive(p.wordmark > 0.001f);
            wordmark.localPosition = wordmarkRest + new Vector3(0f, -WordmarkRisePixels * pixel * p.wordmarkRise, 0f);

            matTraceCore.SetFloat("_Trace", p.trace);
            matTraceHalo.SetFloat("_Trace", p.trace);
            matTraceCore.SetFloat("_Intensity", TraceCore * p.glow);
            matTraceHalo.SetFloat("_Intensity", TraceHalo * p.glow);
            matCubeCore.SetFloat("_Intensity", CubeCore * p.cubeGlow);

            pulse.gameObject.SetActive(p.pulseAlpha > 0f);
            // the outline is resized rather than the transform scaled, so the width stays in pixels
            pulseLine.SetPositions(Hexagon(OuterRadius * p.pulseScale, -0.003f, false));
            pulseLine.widthMultiplier = p.pulseWidth * pixel;
            matPulse.SetFloat("_Intensity", p.pulseAlpha);

            UpdatePackets(time, p.packets, pixel);
            matBackdrop.SetFloat("_Intensity", BackdropGlow * p.backdrop);

            hole.gameObject.SetActive(p.hole > 0f);
            hole.localScale = Vector3.one * p.holeScale;
            matHole.SetFloat("_Open", p.hole);

            bloomNow = p.bloom;
            if (image != null) image.color = new Color(1f, 1f, 1f, p.cover);
        }
    }
}
