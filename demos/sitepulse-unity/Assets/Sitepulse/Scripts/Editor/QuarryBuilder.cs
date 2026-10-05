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
using UnityEditor.Rendering.Universal.ShaderGUI;

namespace DeviceChain.Sitepulse.EditorTools
{
    /// <summary>
    /// Builds the Quarry scene and the assets it uses from the generated art: prop prefabs
    /// (pairing each prop's two glTF levels in one LOD group), the terrain material, the scene
    /// itself with props placed from the terrain's feature file, and the URP look.
    /// Everything here can be re-run; it overwrites what it made before.
    /// </summary>
    public static class QuarryBuilder
    {
        const string Root = "Assets/Sitepulse/";
        const string Models = Root + "Art/Models/";
        const string PropModels = Models + "Props/";
        const string Prefabs = Root + "Art/Prefabs/";
        const string Terrain = Root + "Art/Terrain/";
        const string Materials = Root + "Art/Materials/";
        const string SettingsDir = Root + "Settings/";
        public const string ScenePath = Root + "Scenes/Quarry.unity";

        // terrain tree prototypes (ArtSource/props/build_props.py), named as the feature file names them
        static readonly string[] Vegetation = { "conifer_a", "conifer_b", "conifer_c", "shrub_a", "shrub_b", "rock_a", "rock_b", "rock_c" };

        [MenuItem("Sitepulse/Quarry/Rebuild Everything")]
        public static void BuildAll()
        {
            // the benchmark reads GPU and CPU frame times from the frame timing stats
            PlayerSettings.enableFrameTimingStats = true;
            ConfigureImports();
            BuildPropPrefabs();
            BuildMaterials();
            BuildEffects();
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
            // the site colour maps: the whole terrain at half a metre per texel, the work site at
            // a quarter of a metre
            foreach (var map in new[] { "quarry_color.png", "quarry_color_core.png" })
                ConfigureColorMap(Terrain + map);
        }

        static void ConfigureColorMap(string path)
        {
            var ti = (TextureImporter)AssetImporter.GetAtPath(path);
            if (ti == null) throw new FileNotFoundException(path);
            ti.textureType = TextureImporterType.Default;
            ti.sRGBTexture = true;
            ti.mipmapEnabled = true;
            ti.wrapMode = TextureWrapMode.Clamp;
            ti.filterMode = FilterMode.Trilinear;
            ti.anisoLevel = 8;
            ti.maxTextureSize = 2048;
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
                var go = FleetRig.BuildMergedLod(lod0, lod1, name, out var error, plant ? 0.07f : 0.08f, plant ? 0.008f : 0.01f);
                if (go == null) throw new InvalidOperationException(name + ": " + error);
                if (plant)
                {
                    // vegetation and rocks are coloured by their vertex colours, which the glTF
                    // material does not read: draw them with the vertex-colour shader
                    var vc = name.StartsWith("rock", StringComparison.Ordinal) ? RockMaterial() : VegetationMaterial();
                    foreach (var r in go.GetComponentsInChildren<Renderer>())
                        r.sharedMaterials = Enumerable.Repeat(vc, r.sharedMaterials.Length).ToArray();
                    // the forest is some ten thousand instances: only the near, full-detail level
                    // casts a shadow; past it the shadow would be lost in the cascades anyway
                    var lods = go.GetComponent<LODGroup>().GetLODs();
                    foreach (var r in lods[0].renderers) r.shadowCastingMode = ShadowCastingMode.On;
                    foreach (var r in lods[1].renderers) r.shadowCastingMode = ShadowCastingMode.Off;
                }
                PrefabUtility.SaveAsPrefabAsset(go, Prefabs + name + ".prefab");
                UnityEngine.Object.DestroyImmediate(go);
            }
            AssetDatabase.SaveAssets();
        }

        static Material VegetationMaterial()
        {
            var m = LoadOrCreateMaterial(Materials + "M_Vegetation.mat", "Sitepulse/Vertex Color Lit");
            m.SetColor("_BaseColor", Color.white);
            m.SetFloat("_Smoothness", 0.08f);
            m.enableInstancing = true;
            EditorUtility.SetDirty(m);
            return m;
        }

        /// <summary>The machines' dust: a warm light earth colour, heavy over the lowest metre and a
        /// bit, a light film above.</summary>
        static Material MachineMaterial()
        {
            var m = LoadOrCreateMaterial(Materials + "M_MachineDust.mat", "Sitepulse/Machine Lit");
            m.SetColor("_DustColor", new Color(0.66f, 0.60f, 0.51f));
            m.SetFloat("_DustHeight", 1.4f);
            m.SetFloat("_DustAmount", 0.62f);
            m.SetFloat("_DustBase", 0.08f);
            EditorUtility.SetDirty(m);
            return m;
        }

        static Material RockMaterial()
        {
            // the same rock as ArtSource/sitepulse_kit.py's palette (linear), shaded by vertex colour
            var m = LoadOrCreateMaterial(Materials + "M_Rock.mat", "Sitepulse/Vertex Color Lit");
            m.SetColor("_BaseColor", new Color(0.300f, 0.282f, 0.252f).gamma);
            m.SetFloat("_Smoothness", 0.06f);
            m.enableInstancing = true;
            EditorUtility.SetDirty(m);
            return m;
        }

        // ------------------------------------------------------------------ materials
        const string TerrainMaterialPath = Materials + "Terrain/M_QuarryTerrain.mat";

        [MenuItem("Sitepulse/Quarry/Build Terrain Material")]
        public static void BuildMaterials()
        {
            Directory.CreateDirectory(Materials + "Terrain");
            var m = LoadOrCreateMaterial(TerrainMaterialPath, "Sitepulse/Stylized Terrain");
            var map = AssetDatabase.LoadAssetAtPath<Texture2D>(Terrain + "quarry_color.png")
                ?? throw new FileNotFoundException(Terrain + "quarry_color.png");
            m.SetTexture("_ColorMap", map);
            var core = AssetDatabase.LoadAssetAtPath<Texture2D>(Terrain + "quarry_color_core.png")
                ?? throw new FileNotFoundException(Terrain + "quarry_color_core.png");
            m.SetTexture("_ColorMapCore", core);
            var info = JsonUtility.FromJson<FeatureTerrain>(File.ReadAllText(Terrain + "quarry_features.json")).terrain;
            m.SetVector("_CoreRect", new Vector4(info.core.x0, info.core.z0, info.core.size, 0f));
            // the ground cut into flat facets like the rocks: gentle and broad on worked ground,
            // steeper and smaller on the crushed products
            m.SetVector("_GroundFacet", new Vector4(3.2f, 0.05f, 0.025f, 0f));
            m.SetVector("_PileFacet", new Vector4(1.5f, 0.42f, 0.06f, 0f));
            m.SetFloat("_FacetFar", 190f);
            // the rock: strata of warm light, warm dark and cool grey bands about a bench face's
            // tenth high, broken into flat facets a few metres across; loose rock in smaller ones
            m.SetColor("_RockLight", new Color(0.70f, 0.66f, 0.60f));
            m.SetColor("_RockDark", new Color(0.50f, 0.47f, 0.43f));
            m.SetColor("_RockCool", new Color(0.56f, 0.57f, 0.58f));
            m.SetVector("_RockSlope", new Vector4(44f, 54f, 0f, 0f));
            m.SetFloat("_StrataHeight", 1.4f);
            m.SetFloat("_StrataWarp", 1.1f);
            m.SetFloat("_FacetSize", 3.2f);
            m.SetFloat("_FacetTilt", 0.3f);
            m.SetFloat("_FacetShade", 0.06f);
            m.SetFloat("_RubbleSize", 0.9f);
            m.SetFloat("_RubbleTilt", 0.35f);
            m.SetFloat("_GroundVariation", 0.05f);
            m.SetFloat("_Smoothness", 0.04f);
            m.enableInstancing = true;
            EditorUtility.SetDirty(m);
            AssetDatabase.SaveAssets();
        }

        [Serializable] sealed class FeatureTerrain { public TerrainBlock terrain; }
        [Serializable] sealed class TerrainBlock { public CoreRect core; }
        [Serializable] sealed class CoreRect { public float x0, z0, size; public int res; }

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

        // ------------------------------------------------------------------ effects
        const string FxTextures = Root + "Art/Textures/Fx/";
        const string FxMaterials = Materials + "Fx/";
        const string EffectsPath = SettingsDir + "QuarryEffects.asset";

        /// <summary>
        /// The effects' textures (drawn here, in code), their particle materials and the
        /// <see cref="QuarryEffects"/> asset that holds them with the rock mesh falling material uses.
        /// </summary>
        [MenuItem("Sitepulse/Quarry/Build Effects")]
        public static QuarryEffects BuildEffects()
        {
            Directory.CreateDirectory(FxTextures);
            Directory.CreateDirectory(FxMaterials);
            var puff = WriteTexture(FxTextures + "T_FxPuff.png", 64, Puff);
            var glow = WriteTexture(FxTextures + "T_FxGlow.png", 64, Glow);

            var dust = ParticleMaterial(FxMaterials + "M_FxDust.mat", "Universal Render Pipeline/Particles/Simple Lit", puff, Color.white, 1f, 0f);
            var exhaust = ParticleMaterial(FxMaterials + "M_FxExhaust.mat", "Universal Render Pipeline/Particles/Simple Lit", puff, Color.white, 1f, 0f);
            var rock = ParticleMaterial(FxMaterials + "M_FxRock.mat", "Universal Render Pipeline/Particles/Simple Lit", null, new Color(0.47f, 0.44f, 0.40f), 0f, 0f);
            var flare = ParticleMaterial(FxMaterials + "M_FxBeacon.mat", "Universal Render Pipeline/Particles/Unlit", glow, new Color(1f, 0.55f, 0.12f), 1f, 2f);

            var fx = AssetDatabase.LoadAssetAtPath<QuarryEffects>(EffectsPath);
            if (fx == null)
            {
                fx = ScriptableObject.CreateInstance<QuarryEffects>();
                AssetDatabase.CreateAsset(fx, EffectsPath);
            }
            fx.dust = dust;
            fx.exhaust = exhaust;
            fx.rock = rock;
            fx.flare = flare;
            var chunk = AssetDatabase.LoadAllAssetsAtPath(PropModels + "rock_c_LOD1.glb").OfType<Mesh>().FirstOrDefault();
            fx.chunk = chunk != null ? chunk : throw new FileNotFoundException(PropModels + "rock_c_LOD1.glb (mesh)");
            EditorUtility.SetDirty(fx);
            AssetDatabase.SaveAssets();
            return fx;
        }

        /// <summary>A URP particle material: surface 0 opaque, 1 transparent; blend 0 alpha, 2 additive.</summary>
        /// <summary>The overlay's dashed geofence line and its pulsing alert ring: unlit, drawn over
        /// the scene's lighting so they read as data, not as things on the site.</summary>
        static (Material line, Material ring) OverlayMaterials()
        {
            Directory.CreateDirectory(FxTextures);
            Directory.CreateDirectory(FxMaterials);
            var dash = WriteTexture(FxTextures + "T_OverlayDash.png", 64, (u, v) => Mathf.SmoothStep(0f, 1f, Mathf.InverseLerp(0.62f, 0.56f, u)) * Mathf.SmoothStep(0f, 1f, Mathf.InverseLerp(0.0f, 0.06f, u)));
            var ti = (TextureImporter)AssetImporter.GetAtPath(FxTextures + "T_OverlayDash.png");
            ti.wrapMode = TextureWrapMode.Repeat;
            ti.SaveAndReimport();
            var ring = WriteTexture(FxTextures + "T_OverlayRing.png", 64, (u, v) => Mathf.Lerp(0.25f, 1f, v * v) * Mathf.SmoothStep(0f, 1f, Mathf.InverseLerp(1f, 0.82f, v)));
            var line = ParticleMaterial(FxMaterials + "M_OverlayLine.mat", "Universal Render Pipeline/Particles/Unlit", dash, new Color(1f, 1f, 1f, 0.95f), 1f, 0f);
            line.SetFloat("_SoftParticlesEnabled", 0f);
            BaseShaderGUI.SetMaterialKeywords(line, null, ParticleGUI.SetMaterialKeywords);
            var ringMat = ParticleMaterial(FxMaterials + "M_OverlayRing.mat", "Universal Render Pipeline/Particles/Unlit", ring, new Color(1f, 0.70f, 0.12f, 0.9f), 1f, 0f);
            ringMat.SetFloat("_SoftParticlesEnabled", 0f);
            BaseShaderGUI.SetMaterialKeywords(ringMat, null, ParticleGUI.SetMaterialKeywords);
            EditorUtility.SetDirty(line);
            EditorUtility.SetDirty(ringMat);
            AssetDatabase.SaveAssets();
            return (line, ringMat);
        }

        static Material HoseMaterial()
        {
            var m = LoadOrCreateMaterial(Materials + "M_FuelHose.mat", "Universal Render Pipeline/Lit");
            m.SetColor("_BaseColor", new Color(0.05f, 0.05f, 0.055f));
            m.SetFloat("_Smoothness", 0.35f);
            EditorUtility.SetDirty(m);
            return m;
        }

        static Material ParticleMaterial(string path, string shader, Texture2D tex, Color color, float surface, float blend)
        {
            var m = LoadOrCreateMaterial(path, shader);
            m.SetTexture("_BaseMap", tex);
            m.SetColor("_BaseColor", color);
            m.SetFloat("_Surface", surface);
            m.SetFloat("_Blend", blend);
            m.SetFloat("_ReceiveShadows", 0f);
            m.SetFloat("_SoftParticlesEnabled", surface > 0f && blend == 0f ? 1f : 0f);
            m.SetVector("_SoftParticleFadeParams", new Vector4(0f, 1f / 1.5f, 0f, 0f));
            m.SetFloat("_SoftParticlesNearFadeDistance", 0f);
            m.SetFloat("_SoftParticlesFarFadeDistance", 1.5f);
            m.SetFloat("_Smoothness", 0f);
            BaseShaderGUI.SetMaterialKeywords(m, null, ParticleGUI.SetMaterialKeywords);
            m.enableInstancing = true;
            EditorUtility.SetDirty(m);
            return m;
        }

        static Texture2D WriteTexture(string path, int n, Func<float, float, float> alpha)
        {
            var tex = new Texture2D(n, n, TextureFormat.RGBA32, false);
            var px = new Color32[n * n];
            for (int y = 0; y < n; y++)
                for (int x = 0; x < n; x++)
                {
                    float a = Mathf.Clamp01(alpha((x + 0.5f) / n, (y + 0.5f) / n));
                    px[y * n + x] = new Color32(255, 255, 255, (byte)Mathf.RoundToInt(a * 255f));
                }
            tex.SetPixels32(px);
            File.WriteAllBytes(path, tex.EncodeToPNG());
            UnityEngine.Object.DestroyImmediate(tex);
            AssetDatabase.ImportAsset(path, ImportAssetOptions.ForceSynchronousImport);
            var ti = (TextureImporter)AssetImporter.GetAtPath(path);
            ti.textureType = TextureImporterType.Default;
            ti.sRGBTexture = true;
            ti.alphaIsTransparency = true;
            ti.wrapMode = TextureWrapMode.Clamp;
            ti.mipmapEnabled = true;
            ti.textureCompression = TextureImporterCompression.Uncompressed;
            ti.SaveAndReimport();
            return AssetDatabase.LoadAssetAtPath<Texture2D>(path);
        }

        // a puff: a few overlapping soft lumps, so it is not a perfect disc
        static float Puff(float u, float v)
        {
            float a = 0f;
            foreach (var (x, y, r) in new[] { (0.5f, 0.5f, 0.36f), (0.36f, 0.56f, 0.22f), (0.64f, 0.58f, 0.2f), (0.5f, 0.35f, 0.22f), (0.58f, 0.68f, 0.17f) })
            {
                float d = Mathf.Sqrt((u - x) * (u - x) + (v - y) * (v - y)) / r;
                a = Mathf.Max(a, Mathf.SmoothStep(1f, 0f, d) * 0.9f);
            }
            float edge = Mathf.Sqrt((u - 0.5f) * (u - 0.5f) + (v - 0.5f) * (v - 0.5f));
            return a * Mathf.SmoothStep(0.5f, 0.38f, edge);
        }

        // a glow: a bright core with a soft halo
        static float Glow(float u, float v)
        {
            float d = Mathf.Sqrt((u - 0.5f) * (u - 0.5f) + (v - 0.5f) * (v - 0.5f)) * 2f;
            return Mathf.Pow(Mathf.Clamp01(1f - d), 3f) + 0.6f * Mathf.Pow(Mathf.Clamp01(1f - d * 3f), 2f);
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
            sunGo.transform.rotation = SunRotation;
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
            qt.terrainMaterial = AssetDatabase.LoadAssetAtPath<Material>(TerrainMaterialPath);
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
                // the plant's flywheels and screen box move: everything else is static
                foreach (var t in go.GetComponentsInChildren<Transform>(true))
                {
                    bool moving = false;
                    for (var u = t; u != null && u != go.transform && !moving; u = u.parent)
                        moving = PlantEffects.MovingNodes.Contains(u.name);
                    t.gameObject.isStatic = !moving;
                }
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
            fleet.effects = AssetDatabase.LoadAssetAtPath<QuarryEffects>(EffectsPath);
            fleet.machineMaterial = MachineMaterial();
            fleet.time = 30f;
            fleetGo.SetActive(true);
            fleet.Seek(fleet.time);

            // the plant's moving belts, falling streams and dust
            var plantGo = props.transform.Cast<Transform>().FirstOrDefault(t => t.name.StartsWith("crusher_plant", StringComparison.Ordinal));
            if (plantGo != null)
            {
                var pfxGo = new GameObject("Plant Effects");
                pfxGo.SetActive(false);
                var pfx = pfxGo.AddComponent<PlantEffects>();
                pfx.plant = plantGo;
                pfx.terrain = qt;
                pfx.effects = fleet.effects;
                pfxGo.SetActive(true);
            }

            // the refuel bay at work: the attendant and the hose while a truck is in the bay
            var tank = props.transform.Cast<Transform>().FirstOrDefault(t => t.name.StartsWith("fuel_tank", StringComparison.Ordinal));
            if (tank != null)
            {
                var rvGo = new GameObject("Refuel Bay");
                rvGo.SetActive(false);
                var rv = rvGo.AddComponent<RefuelVignette>();
                rv.fleet = fleet;
                rv.terrain = qt;
                rv.fuelTank = tank;
                rv.worker = AssetDatabase.LoadAssetAtPath<GameObject>(Prefabs + "worker.prefab");
                rv.hoseMaterial = HoseMaterial();
                rvGo.SetActive(true);
            }

            // the data layer over the site: tags, the geofence, the load point, alerts
            var ovGo = new GameObject("IoT Overlay");
            ovGo.SetActive(false);
            var ov = ovGo.AddComponent<IotOverlay>();
            ov.fleet = fleet;
            ov.terrain = qt;
            ov.features = qt.features;
            ov.plant = plantGo;
            (ov.lineMaterial, ov.ringMaterial) = OverlayMaterials();
            ovGo.SetActive(true);

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
            var pc = AssetDatabase.LoadAssetAtPath<UniversalRenderPipelineAsset>(PcPipeline)
                ?? throw new FileNotFoundException(PcPipeline);
            // far enough that the wide shots keep every machine's shadow under it
            pc.shadowDistance = on ? PcShadowDistance : 50f;
            pc.shadowCascadeCount = on ? 3 : 4;
            pc.cascade3Split = new Vector2(0.06f, 0.22f);
            pc.cascade4Split = new Vector3(0.067f, 0.2f, 0.467f);
            pc.msaaSampleCount = on ? PcMsaa : 1;
            // nothing samples the opaque colour; the depth copy is for soft particles and the AO
            pc.supportsCameraOpaqueTexture = !on;
            pc.supportsCameraDepthTexture = true;
            SetSoftShadowQuality(pc, on ? 2 : 3);
            EditorUtility.SetDirty(pc);
            SetSsao(pc, on);
            if (on) ConfigureLaptop(pc);

            var sun = RenderSettings.sun != null ? RenderSettings.sun : UnityEngine.Object.FindAnyObjectByType<Light>();
            if (sun != null)
            {
                sun.color = on ? new Color(1f, 0.86f, 0.70f) : new Color(1f, 0.957f, 0.839f);
                sun.intensity = on ? 1.85f : 1f;
                sun.shadowStrength = on ? 0.88f : 1f;
                if (on) sun.transform.rotation = SunRotation;
            }

            // warm key, cool fill: a blue sky over a neutral horizon and a dark earthy ground
            RenderSettings.ambientMode = on ? AmbientMode.Trilight : AmbientMode.Skybox;
            RenderSettings.ambientSkyColor = new Color(0.40f, 0.50f, 0.66f);
            RenderSettings.ambientEquatorColor = new Color(0.46f, 0.46f, 0.45f);
            RenderSettings.ambientGroundColor = new Color(0.22f, 0.20f, 0.17f);
            RenderSettings.ambientIntensity = 1f;
            // aerial perspective: haze that only builds up towards the horizon, matched by the
            // sky's horizon and ground colour so the terrain's edge melts into it
            RenderSettings.fog = on;
            RenderSettings.fogMode = FogMode.ExponentialSquared;
            RenderSettings.fogDensity = 0.0012f;
            RenderSettings.fogColor = new Color(0.71f, 0.76f, 0.82f);
            RenderSettings.skybox = on ? Sky() : AssetDatabase.GetBuiltinExtraResource<Material>("Default-Skybox.mat");

            // the volume may be inactive (look off), which GameObject.Find would not see
            var volGo = UnityEngine.Object.FindObjectsByType<Volume>(FindObjectsInactive.Include)
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

        /// <summary>The sun's elevation above the horizon (degrees). Low enough that the bench faces
        /// carry shadow lines, high enough that the pit floor is still in the sun.</summary>
        public static float SunElevation = 30f;

        /// <summary>The compass bearing the sun shines from (degrees clockwise from north): the
        /// south-east, so its light rakes across the pit's north wall from the side.</summary>
        public static float SunAzimuth = 140f;

        static Quaternion SunRotation => Quaternion.Euler(SunElevation, SunAzimuth + 180f, 0f);

        const string PcPipeline = "Assets/Settings/PC_RPAsset.asset";
        const string LaptopPipeline = "Assets/Settings/Laptop_RPAsset.asset";
        const string LaptopRenderer = "Assets/Settings/Laptop_Renderer.asset";

        /// <summary>The PC look's shadow reach (m) and MSAA samples.</summary>
        public static float PcShadowDistance = 240f;
        public static int PcMsaa = 2;

        // 1 low, 2 medium, 3 high (URP's SoftShadowQuality)
        static void SetSoftShadowQuality(UniversalRenderPipelineAsset a, int quality)
        {
            var so = new SerializedObject(a);
            so.FindProperty("m_SoftShadowQuality").intValue = quality;
            so.ApplyModifiedPropertiesWithoutUndo();
        }

        /// <summary>
        /// The Laptop quality level: the PC pipeline with its costs cut for integrated or
        /// entry graphics. No MSAA (the camera's SMAA still smooths edges), no screen-space
        /// ambient occlusion, half the shadow reach in two cascades at a smaller map, a coarser
        /// terrain mesh and a shorter tree distance; the effects halve their emission
        /// (<see cref="QuarryEffects.LaptopQuality"/>). It is added to the quality levels after PC.
        /// </summary>
        [MenuItem("Sitepulse/Quarry/Configure Laptop Quality")]
        public static void ConfigureLaptop() => ConfigureLaptop(AssetDatabase.LoadAssetAtPath<UniversalRenderPipelineAsset>(PcPipeline));

        static void ConfigureLaptop(UniversalRenderPipelineAsset pc)
        {
            var pcRenderer = pc.rendererDataList[0] ?? throw new InvalidOperationException("PC pipeline has no renderer");
            if (AssetDatabase.LoadAssetAtPath<ScriptableRendererData>(LaptopRenderer) == null)
                AssetDatabase.CopyAsset(AssetDatabase.GetAssetPath(pcRenderer), LaptopRenderer);
            if (AssetDatabase.LoadAssetAtPath<UniversalRenderPipelineAsset>(LaptopPipeline) == null)
                AssetDatabase.CopyAsset(PcPipeline, LaptopPipeline);
            var renderer = AssetDatabase.LoadAssetAtPath<ScriptableRendererData>(LaptopRenderer);
            foreach (var f in renderer.rendererFeatures)
                if (f != null && f.GetType().Name == "ScreenSpaceAmbientOcclusion") f.SetActive(false);
            EditorUtility.SetDirty(renderer);
            var laptop = AssetDatabase.LoadAssetAtPath<UniversalRenderPipelineAsset>(LaptopPipeline);
            var so = new SerializedObject(laptop);
            var list = so.FindProperty("m_RendererDataList");
            list.arraySize = 1;
            list.GetArrayElementAtIndex(0).objectReferenceValue = renderer;
            so.FindProperty("m_DefaultRendererIndex").intValue = 0;
            so.ApplyModifiedPropertiesWithoutUndo();
            laptop.msaaSampleCount = 1;
            laptop.supportsCameraOpaqueTexture = false;
            laptop.shadowDistance = 140f;
            laptop.shadowCascadeCount = 2;
            laptop.cascade2Split = 0.25f;
            laptop.mainLightShadowmapResolution = 2048;
            SetSoftShadowQuality(laptop, 1);
            EditorUtility.SetDirty(laptop);

            var qs = AssetDatabase.LoadAllAssetsAtPath("ProjectSettings/QualitySettings.asset")[0];
            var q = new SerializedObject(qs);
            var levels = q.FindProperty("m_QualitySettings");
            int pcIndex = -1, laptopIndex = -1;
            for (int i = 0; i < levels.arraySize; i++)
            {
                var n = levels.GetArrayElementAtIndex(i).FindPropertyRelative("name").stringValue;
                if (n == "PC") pcIndex = i;
                if (n == QuarryEffects.LaptopQuality) laptopIndex = i;
            }
            if (pcIndex < 0) throw new InvalidOperationException("no PC quality level");
            if (laptopIndex < 0)
            {
                levels.InsertArrayElementAtIndex(pcIndex);             // a copy of PC, after it
                laptopIndex = pcIndex + 1;
            }
            var el = levels.GetArrayElementAtIndex(laptopIndex);
            el.FindPropertyRelative("name").stringValue = QuarryEffects.LaptopQuality;
            el.FindPropertyRelative("customRenderPipeline").objectReferenceValue = laptop;
            el.FindPropertyRelative("lodBias").floatValue = 1f;
            el.FindPropertyRelative("antiAliasing").intValue = 0;
            // terrain overrides: pixel error (1) and tree distance (16)
            el.FindPropertyRelative("terrainQualityOverrides").intValue = 1 | 16;
            el.FindPropertyRelative("terrainPixelError").floatValue = 8f;
            el.FindPropertyRelative("terrainTreeDistance").floatValue = 700f;
            var pcEl = levels.GetArrayElementAtIndex(pcIndex);
            pcEl.FindPropertyRelative("name").stringValue = "PC";
            pcEl.FindPropertyRelative("customRenderPipeline").objectReferenceValue = pc;
            q.ApplyModifiedPropertiesWithoutUndo();
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
                    so.FindProperty("m_Settings.Downsample").boolValue = on;
                    // from the depth copy after the opaques, not a depth-normals prepass that would
                    // draw the whole site (terrain, thousands of trees) a second time
                    so.FindProperty("m_Settings.Source").enumValueIndex = on ? 0 : 1;
                    so.FindProperty("m_Settings.AfterOpaque").boolValue = on;
                    so.FindProperty("m_Settings.Samples").enumValueIndex = 1;
                    so.FindProperty("m_Settings.BlurQuality").enumValueIndex = on ? 1 : 0;
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
            ca.postExposure.Override(Tonemap == TonemappingMode.Neutral ? 0.05f : 0.25f);
            ca.contrast.Override(14f);
            ca.saturation.Override(Tonemap == TonemappingMode.Neutral ? -4f : 0f);
            // a light grade: cooler shadows, warmer highlights
            var smh = Get<ShadowsMidtonesHighlights>(p);
            smh.shadows.Override(new Vector4(0.94f, 0.98f, 1.08f, 0f));
            smh.midtones.Override(new Vector4(1f, 1f, 1f, 0f));
            smh.highlights.Override(new Vector4(1.04f, 1.01f, 0.95f, 0f));
            var vig = Get<Vignette>(p);
            vig.intensity.Override(0.18f);
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
