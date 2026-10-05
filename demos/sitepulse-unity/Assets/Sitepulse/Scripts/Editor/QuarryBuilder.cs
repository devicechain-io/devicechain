// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

using System;
using System.Collections.Generic;
using System.IO;
using System.Linq;
using DeviceChain.Sitepulse.Visuals;
using UnityEditor;
using UnityEditor.SceneManagement;
using UnityEngine;
using UnityEngine.Rendering;
using UnityEngine.Rendering.Universal;

namespace DeviceChain.Sitepulse.EditorTools
{
    /// <summary>
    /// Builds the Quarry scene and the assets it uses from the generated art: prop prefabs
    /// (pairing each prop's two glTF levels in one LOD group), terrain layers and materials, the
    /// scene itself with props placed from the terrain's feature file, and the URP look.
    /// Everything here can be re-run; it overwrites what it made before.
    /// </summary>
    public static class QuarryBuilder
    {
        const string Root = "Assets/Sitepulse/";
        const string Models = Root + "Art/Models/";
        const string PropModels = Models + "Props/";
        const string Prefabs = Root + "Art/Prefabs/";
        const string Terrain = Root + "Art/Terrain/";
        const string TerrainTextures = Root + "Art/Textures/Terrain/";
        const string Materials = Root + "Art/Materials/";
        const string SettingsDir = Root + "Settings/";
        public const string ScenePath = Root + "Scenes/Quarry.unity";

        static readonly string[] Layers = { "Rock", "Gravel", "Dirt", "Grass" };
        // world metres per texture repeat for each layer
        static readonly float[] LayerTile = { 10f, 6f, 7f, 11f };
        static readonly string[] Vegetation = { "pine", "broadleaf", "shrub", "rock_a", "rock_b" };

        [MenuItem("Sitepulse/Quarry/Rebuild Everything")]
        public static void BuildAll()
        {
            ConfigureImports();
            BuildPropPrefabs();
            BuildMaterials();
            IncludeRuntimeTerrainShaders();
            BuildScene();
        }

        // The terrain is made when the scene loads, so a build sees no TerrainData in the scene and
        // leaves out the shaders the terrain engine needs at run time. Without the normal map
        // generator an instanced terrain draws black in a player while it looks right in the Editor.
        static readonly string[] RuntimeTerrainShaders =
        {
            "Hidden/TerrainEngine/GenerateNormalmap",
            "Hidden/Nature/Terrain/Utilities",
            "Hidden/Universal Render Pipeline/Terrain/Lit (Basemap Gen)",
        };

        [MenuItem("Sitepulse/Quarry/Include Runtime Terrain Shaders")]
        public static void IncludeRuntimeTerrainShaders()
        {
            var gs = AssetDatabase.LoadAssetAtPath<UnityEngine.Object>("ProjectSettings/GraphicsSettings.asset");
            var so = new SerializedObject(gs);
            var list = so.FindProperty("m_AlwaysIncludedShaders");
            foreach (var name in RuntimeTerrainShaders)
            {
                var sh = Shader.Find(name) ?? throw new InvalidOperationException("shader not found: " + name);
                bool present = false;
                for (int i = 0; i < list.arraySize; i++)
                    present |= list.GetArrayElementAtIndex(i).objectReferenceValue == sh;
                if (present) continue;
                list.InsertArrayElementAtIndex(list.arraySize);
                list.GetArrayElementAtIndex(list.arraySize - 1).objectReferenceValue = sh;
            }
            so.ApplyModifiedPropertiesWithoutUndo();
            AssetDatabase.SaveAssets();
        }

        // ------------------------------------------------------------------ imports
        [MenuItem("Sitepulse/Quarry/Configure Texture Imports")]
        public static void ConfigureImports()
        {
            foreach (var layer in Layers)
            {
                Configure(TerrainTextures + $"T_{layer}_Albedo.jpg", TextureImporterType.Default, true, 1024);
                Configure(TerrainTextures + $"T_{layer}_Normal.jpg", TextureImporterType.NormalMap, false, 1024);
                Configure(TerrainTextures + $"T_{layer}_Mask.png", TextureImporterType.Default, false, 512);
            }
            foreach (var layer in Layers)
            {
                // the masks are read on the CPU to fill the alphamaps: readable, linear, exact
                var path = Terrain + $"quarry_splat_{layer.ToLowerInvariant()}.png";
                var ti = (TextureImporter)AssetImporter.GetAtPath(path);
                if (ti == null) throw new FileNotFoundException(path);
                ti.textureType = TextureImporterType.SingleChannel;
                ti.textureShape = TextureImporterShape.Texture2D;
                ti.isReadable = true;
                ti.sRGBTexture = false;
                ti.mipmapEnabled = false;
                ti.npotScale = TextureImporterNPOTScale.None;
                ti.textureCompression = TextureImporterCompression.Uncompressed;
                ti.maxTextureSize = 2048;
                var s = ti.GetDefaultPlatformTextureSettings();
                s.format = TextureImporterFormat.R8;
                ti.SetPlatformTextureSettings(s);
                ti.SaveAndReimport();
            }
        }

        static void Configure(string path, TextureImporterType type, bool srgb, int max)
        {
            var ti = (TextureImporter)AssetImporter.GetAtPath(path);
            if (ti == null) throw new FileNotFoundException(path);
            ti.textureType = type;
            ti.sRGBTexture = srgb;
            ti.maxTextureSize = max;
            ti.mipmapEnabled = true;
            ti.anisoLevel = 4;
            ti.textureCompression = TextureImporterCompression.CompressedHQ;
            ti.SaveAndReimport();
        }

        // ------------------------------------------------------------------ prefabs
        [MenuItem("Sitepulse/Quarry/Build Prop Prefabs")]
        public static void BuildPropPrefabs()
        {
            Directory.CreateDirectory(Prefabs);
            foreach (var path in Directory.GetFiles(PropModels, "*.glb"))
            {
                var name = Path.GetFileNameWithoutExtension(path);
                if (name.EndsWith("_LOD1", StringComparison.Ordinal)) continue;
                var lod0 = AssetDatabase.LoadAssetAtPath<GameObject>(PropModels + name + ".glb");
                var lod1 = AssetDatabase.LoadAssetAtPath<GameObject>(PropModels + name + "_LOD1.glb");
                if (lod0 == null || lod1 == null) throw new FileNotFoundException("prop model " + name);
                bool plant = Vegetation.Contains(name);
                // Trees and boulders are many and small on screen: drop to LOD1 sooner and cull later.
                var go = FleetRig.BuildMergedLod(lod0, lod1, name, out var error, plant ? 0.12f : 0.08f, plant ? 0.008f : 0.01f);
                if (go == null) throw new InvalidOperationException(name + ": " + error);
                if (plant)
                    foreach (var r in go.GetComponentsInChildren<Renderer>()) r.shadowCastingMode = ShadowCastingMode.On;
                PrefabUtility.SaveAsPrefabAsset(go, Prefabs + name + ".prefab");
                UnityEngine.Object.DestroyImmediate(go);
            }
            AssetDatabase.SaveAssets();
        }

        // ------------------------------------------------------------------ materials
        [MenuItem("Sitepulse/Quarry/Build Terrain Layers and Materials")]
        public static void BuildMaterials()
        {
            Directory.CreateDirectory(Materials + "Terrain");
            for (int k = 0; k < Layers.Length; k++)
            {
                var layer = Layers[k];
                var path = Materials + $"Terrain/TL_{layer}.terrainlayer";
                var tl = AssetDatabase.LoadAssetAtPath<TerrainLayer>(path);
                if (tl == null)
                {
                    tl = new TerrainLayer();
                    AssetDatabase.CreateAsset(tl, path);
                }
                tl.diffuseTexture = Tex($"T_{layer}_Albedo.jpg");
                tl.normalMapTexture = Tex($"T_{layer}_Normal.jpg");
                tl.maskMapTexture = Tex($"T_{layer}_Mask.png");
                tl.tileSize = new Vector2(LayerTile[k], LayerTile[k]);
                tl.normalScale = 1f;
                tl.metallic = 0f;
                tl.smoothness = 0f;
                // mask map: G = occlusion, B = height for height-based blending, A = smoothness
                tl.maskMapRemapMin = new Vector4(0f, 0f, 0f, 0f);
                tl.maskMapRemapMax = new Vector4(0f, 1f, 1f, 0.45f);
                // dry grass rather than straw; worked earth a little less saturated than the photo
                tl.diffuseRemapMax = layer == "Grass" ? new Vector4(0.9f, 1f, 0.82f, 1f)
                    : layer == "Dirt" ? new Vector4(0.95f, 0.92f, 0.87f, 1f) : Vector4.one;
                EditorUtility.SetDirty(tl);
            }

            var terrainMat = LoadOrCreateMaterial(Materials + "Terrain/M_QuarryTerrain.mat", "Universal Render Pipeline/Terrain/Lit");
            terrainMat.SetFloat("_EnableHeightBlend", 1f);
            terrainMat.SetFloat("_HeightTransition", 0.3f);
            terrainMat.EnableKeyword("_TERRAIN_BLEND_HEIGHT");
            terrainMat.SetFloat("_EnableInstancedPerPixelNormal", 1f);
            terrainMat.EnableKeyword("_TERRAIN_INSTANCED_PERPIXEL_NORMAL");
            terrainMat.enableInstancing = true;
            EditorUtility.SetDirty(terrainMat);

            var cliff = LoadOrCreateMaterial(Materials + "Terrain/M_CliffRock.mat", "Universal Render Pipeline/Lit");
            cliff.SetTexture("_BaseMap", Tex("T_Rock_Albedo.jpg"));
            cliff.SetTexture("_BumpMap", Tex("T_Rock_Normal.jpg"));
            cliff.EnableKeyword("_NORMALMAP");
            cliff.SetFloat("_BumpScale", 1.4f);
            cliff.SetTexture("_OcclusionMap", null);
            cliff.SetFloat("_Smoothness", 0.12f);
            cliff.SetFloat("_Metallic", 0f);
            cliff.SetColor("_BaseColor", new Color(1f, 0.98f, 0.95f));
            cliff.enableInstancing = true;
            EditorUtility.SetDirty(cliff);
            AssetDatabase.SaveAssets();
        }

        static Texture2D Tex(string file)
        {
            var t = AssetDatabase.LoadAssetAtPath<Texture2D>(TerrainTextures + file);
            if (t == null) throw new FileNotFoundException(TerrainTextures + file);
            return t;
        }

        static Material LoadOrCreateMaterial(string path, string shader)
        {
            var m = AssetDatabase.LoadAssetAtPath<Material>(path);
            var sh = Shader.Find(shader) ?? throw new InvalidOperationException("shader not found: " + shader);
            if (m == null)
            {
                m = new Material(sh);
                AssetDatabase.CreateAsset(m, path);
            }
            else
            {
                m.shader = sh;
            }
            return m;
        }

        // ------------------------------------------------------------------ scene
        [Serializable]
        sealed class PropList
        {
            public Prop[] props;
        }

        [Serializable]
        sealed class Prop
        {
            public string p;
            public float x, z, heading;
        }

        [MenuItem("Sitepulse/Quarry/Build Scene")]
        public static void BuildScene()
        {
            var scene = EditorSceneManager.NewScene(NewSceneSetup.EmptyScene, NewSceneMode.Single);

            var sunGo = new GameObject("Sun");
            var sun = sunGo.AddComponent<Light>();
            sun.type = LightType.Directional;
            sun.shadows = LightShadows.Soft;
            // late-morning sun from the south-south-east: it lights the pit's north wall, the faces
            // the scene is about, and rakes across the benches instead of flattening them
            sunGo.transform.rotation = Quaternion.Euler(36f, 335f, 0f);
            RenderSettings.sun = sun;

            var camGo = new GameObject("Main Camera") { tag = "MainCamera" };
            var cam = camGo.AddComponent<Camera>();
            cam.nearClipPlane = 0.3f;
            cam.farClipPlane = 2000f;
            cam.fieldOfView = 50f;
            var camData = camGo.AddComponent<UniversalAdditionalCameraData>();
            camData.renderPostProcessing = true;
            camGo.AddComponent<AudioListener>();
            camGo.transform.position = new Vector3(-150f, 70f, -150f);
            camGo.transform.LookAt(new Vector3(0f, 0f, 0f));

            var terrainGo = new GameObject("Quarry Terrain");
            terrainGo.SetActive(false);
            terrainGo.AddComponent<Terrain>();
            terrainGo.AddComponent<TerrainCollider>();
            var qt = terrainGo.AddComponent<QuarryTerrain>();
            qt.heightmap = AssetDatabase.LoadAssetAtPath<TextAsset>(Terrain + "quarry_height.bytes");
            qt.features = AssetDatabase.LoadAssetAtPath<TextAsset>(Terrain + "quarry_features.json");
            qt.masks = Layers.Select(l => AssetDatabase.LoadAssetAtPath<Texture2D>(Terrain + $"quarry_splat_{l.ToLowerInvariant()}.png")).ToArray();
            qt.layers = Layers.Select(l => AssetDatabase.LoadAssetAtPath<TerrainLayer>(Materials + $"Terrain/TL_{l}.terrainlayer")).ToArray();
            qt.terrainMaterial = AssetDatabase.LoadAssetAtPath<Material>(Materials + "Terrain/M_QuarryTerrain.mat");
            qt.cliffMaterial = AssetDatabase.LoadAssetAtPath<Material>(Materials + "Terrain/M_CliffRock.mat");
            qt.treePrefabs = Vegetation.Select(v => AssetDatabase.LoadAssetAtPath<GameObject>(Prefabs + v + ".prefab")).ToArray();
            terrainGo.isStatic = true;
            terrainGo.SetActive(true);                      // builds the terrain
            if (!qt.Built) throw new InvalidOperationException("terrain did not build");

            var props = new GameObject("Props") { isStatic = true };
            var list = JsonUtility.FromJson<PropList>(qt.features.text);
            foreach (var p in list.props)
            {
                var prefab = AssetDatabase.LoadAssetAtPath<GameObject>(Prefabs + p.p + ".prefab");
                if (prefab == null) throw new FileNotFoundException("prop prefab " + p.p);
                var go = (GameObject)PrefabUtility.InstantiatePrefab(prefab, props.transform);
                var rot = Quaternion.Euler(0f, p.heading, 0f);
                go.transform.SetPositionAndRotation(new Vector3(p.x, GroundUnder(qt, go, p.x, p.z, rot), p.z), rot);
                go.isStatic = true;
            }

            var fleetGo = new GameObject("Fleet Preview");
            fleetGo.SetActive(false);
            var fleet = fleetGo.AddComponent<QuarryFleetPreview>();
            fleet.choreography = AssetDatabase.LoadAssetAtPath<TextAsset>(Root + "Data/quarry_fleet.json");
            fleet.terrain = qt;
            fleet.dozer = Model("dozer");
            fleet.dozerLod1 = Model("dozer_LOD1");
            fleet.loader = Model("loader");
            fleet.loaderLod1 = Model("loader_LOD1");
            fleet.hauler = Model("hauler");
            fleet.haulerLod1 = Model("hauler_LOD1");
            fleet.time = 30f;
            fleetGo.SetActive(true);
            fleet.Seek(fleet.time);

            new GameObject("Benchmark").AddComponent<FrameTimeBenchmark>();

            ApplyLook(true);
            Directory.CreateDirectory(Path.GetDirectoryName(ScenePath));
            EditorSceneManager.SaveScene(scene, ScenePath);
            var scenes = EditorBuildSettings.scenes.Where(s => s.path != ScenePath && File.Exists(s.path)).ToList();
            scenes.Insert(0, new EditorBuildSettingsScene(ScenePath, true));
            EditorBuildSettings.scenes = scenes.ToArray();
        }

        static GameObject Model(string name)
        {
            var go = AssetDatabase.LoadAssetAtPath<GameObject>(Models + name + ".glb");
            return go != null ? go : throw new FileNotFoundException(Models + name + ".glb");
        }

        /// <summary>The lowest terrain height under a prop's footprint, so nothing floats on a slope.</summary>
        static float GroundUnder(QuarryTerrain qt, GameObject go, float x, float z, Quaternion rot)
        {
            var b = new Bounds(go.transform.position, Vector3.zero);
            foreach (var r in go.GetComponentsInChildren<Renderer>()) b.Encapsulate(r.bounds);
            var ext = b.extents - new Vector3(0.2f, 0f, 0.2f);
            float y = qt.HeightAt(x, z);
            foreach (var (sx, sz) in new[] { (-1, -1), (-1, 1), (1, -1), (1, 1) })
            {
                var c = rot * new Vector3(sx * ext.x, 0f, sz * ext.z);
                y = Mathf.Min(y, qt.HeightAt(x + c.x, z + c.z));
            }
            return y;
        }

        // ------------------------------------------------------------------ look
        /// <summary>
        /// The URP look: tonemapping, a little bloom and grading, SSAO, a three-colour ambient,
        /// a warm sun with long cascaded shadows, and distance fog. <c>on=false</c> puts back the
        /// project template's settings, for before/after comparisons.
        /// </summary>
        public static void ApplyLook(bool on)
        {
            var rp = (UniversalRenderPipelineAsset)GraphicsSettings.defaultRenderPipeline;
            var rpQuality = QualitySettings.renderPipeline as UniversalRenderPipelineAsset;
            foreach (var a in new[] { rp, rpQuality }.Where(a => a != null).Distinct())
            {
                // far enough that the wide shots keep every machine's shadow under it
                a.shadowDistance = on ? 320f : 50f;
                a.shadowCascadeCount = 4;
                a.cascade4Split = on ? new Vector3(0.035f, 0.1f, 0.32f) : new Vector3(0.067f, 0.2f, 0.467f);
                a.msaaSampleCount = on ? 4 : 1;
                EditorUtility.SetDirty(a);
                SetSsao(a, on);
            }

            var sun = RenderSettings.sun != null ? RenderSettings.sun : UnityEngine.Object.FindAnyObjectByType<Light>();
            if (sun != null)
            {
                sun.color = on ? new Color(1f, 0.9f, 0.78f) : new Color(1f, 0.957f, 0.839f);
                sun.intensity = on ? 2.4f : 1f;
                sun.shadowStrength = on ? 0.92f : 1f;
            }

            RenderSettings.ambientMode = on ? AmbientMode.Trilight : AmbientMode.Skybox;
            RenderSettings.ambientSkyColor = new Color(0.50f, 0.60f, 0.74f);
            RenderSettings.ambientEquatorColor = new Color(0.52f, 0.48f, 0.42f);
            RenderSettings.ambientGroundColor = new Color(0.20f, 0.17f, 0.14f);
            RenderSettings.ambientIntensity = 1f;
            // aerial perspective: a light haze that greys the far hills, matched by the sky's
            // horizon and ground colour so the terrain's edge melts into it
            RenderSettings.fog = on;
            RenderSettings.fogMode = FogMode.ExponentialSquared;
            RenderSettings.fogDensity = 0.0021f;
            RenderSettings.fogColor = new Color(0.70f, 0.75f, 0.81f);
            RenderSettings.skybox = on ? Sky() : AssetDatabase.GetBuiltinExtraResource<Material>("Default-Skybox.mat");

            // the volume may be inactive (look off), which GameObject.Find would not see
            var volGo = UnityEngine.Object.FindObjectsByType<Volume>(FindObjectsInactive.Include, FindObjectsSortMode.None)
                .Select(v => v.gameObject).FirstOrDefault(g => g.name == "Look");
            if (volGo == null)
            {
                volGo = new GameObject("Look");
                var v = volGo.AddComponent<Volume>();
                v.isGlobal = true;
                v.priority = 10f;
            }
            volGo.GetComponent<Volume>().sharedProfile = LookProfile();
            volGo.SetActive(on);
            var cam = Camera.main;
            if (cam != null)
            {
                var data = cam.GetComponent<UniversalAdditionalCameraData>();
                if (data != null)
                {
                    data.antialiasing = on ? AntialiasingMode.SubpixelMorphologicalAntiAliasing : AntialiasingMode.None;
                    data.antialiasingQuality = AntialiasingQuality.High;
                }
            }
            AssetDatabase.SaveAssets();
        }

        const string SkyPath = Materials + "M_Sky.mat";

        /// <summary>A procedural sky whose ground half is the fog colour, so the hazy horizon has no edge.</summary>
        static Material Sky()
        {
            var m = LoadOrCreateMaterial(SkyPath, "Skybox/Procedural");
            m.SetFloat("_SunSize", 0.035f);
            m.SetFloat("_AtmosphereThickness", 0.85f);
            m.SetColor("_SkyTint", new Color(0.52f, 0.56f, 0.62f));
            m.SetColor("_GroundColor", new Color(0.62f, 0.66f, 0.71f));
            m.SetFloat("_Exposure", 1.15f);
            EditorUtility.SetDirty(m);
            return m;
        }

        static void SetSsao(UniversalRenderPipelineAsset a, bool on)
        {
            foreach (var data in a.rendererDataList)
            {
                if (data == null) continue;
                foreach (var f in data.rendererFeatures)
                {
                    if (f == null || f.GetType().Name != "ScreenSpaceAmbientOcclusion") continue;
                    var so = new SerializedObject(f);
                    so.FindProperty("m_Settings.Intensity").floatValue = on ? 0.55f : 0.4f;
                    so.FindProperty("m_Settings.Radius").floatValue = 0.3f;
                    so.FindProperty("m_Settings.DirectLightingStrength").floatValue = on ? 0.2f : 0.25f;
                    so.FindProperty("m_Settings.Falloff").floatValue = on ? 120f : 100f;
                    so.ApplyModifiedPropertiesWithoutUndo();
                    EditorUtility.SetDirty(f);
                }
            }
        }

        const string LookProfilePath = SettingsDir + "QuarryLook.asset";

        /// <summary>Tone mapping for the look. Compared on the same frames, Neutral keeps the
        /// machines' yellow paint yellow and the dry grass green-grey, where ACES pushed the paint
        /// toward orange and the grass toward straw.</summary>
        public static TonemappingMode Tonemap = TonemappingMode.Neutral;

        /// <summary>The look's post-processing profile, created on first use and brought up to
        /// date with the settings here every time.</summary>
        public static VolumeProfile LookProfile()
        {
            var p = AssetDatabase.LoadAssetAtPath<VolumeProfile>(LookProfilePath);
            if (p == null)
            {
                Directory.CreateDirectory(SettingsDir);
                p = ScriptableObject.CreateInstance<VolumeProfile>();
                AssetDatabase.CreateAsset(p, LookProfilePath);
            }
            var tm = Get<Tonemapping>(p);
            tm.mode.Override(Tonemap);
            var bloom = Get<Bloom>(p);
            bloom.threshold.Override(1.1f);
            bloom.intensity.Override(0.25f);
            bloom.scatter.Override(0.6f);
            var ca = Get<ColorAdjustments>(p);
            ca.postExposure.Override(Tonemap == TonemappingMode.Neutral ? 0.1f : 0.25f);
            ca.contrast.Override(10f);
            ca.saturation.Override(Tonemap == TonemappingMode.Neutral ? 0f : 4f);
            var vig = Get<Vignette>(p);
            vig.intensity.Override(0.16f);
            vig.smoothness.Override(0.5f);
            EditorUtility.SetDirty(p);
            AssetDatabase.SaveAssets();
            return p;
        }

        static T Get<T>(VolumeProfile p) where T : VolumeComponent
        {
            if (p.TryGet<T>(out var existing)) return existing;
            var c = p.Add<T>(true);
            c.name = typeof(T).Name;
            c.hideFlags = HideFlags.HideInInspector | HideFlags.HideInHierarchy;
            AssetDatabase.AddObjectToAsset(c, p);
            return c;
        }
    }
}
