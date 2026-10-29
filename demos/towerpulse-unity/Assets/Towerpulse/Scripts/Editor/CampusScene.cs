// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

using System.Collections.Generic;
using System.IO;
using DeviceChain.Towerpulse.DevicePlane;
using DeviceChain.Towerpulse.Model;
using DeviceChain.Towerpulse.Story;
using DeviceChain.Towerpulse.Visuals;
using UnityEditor;
using UnityEditor.SceneManagement;
using UnityEngine;
using UnityEngine.Rendering;
using UnityEngine.Rendering.Universal;

namespace DeviceChain.Towerpulse.EditorTools
{
    /// <summary>
    /// Builds the campus scene from the art and the layout file: the buildings, the rooftop units and chillers, the furniture and every person the model could
    /// show (hidden until a zone reports someone there), late-afternoon lighting (the scripted day runs from about 14:00 to 16:30), the camera, and the one GameObject that runs the plane and the story. Nothing in the scene
    /// is hand-placed: rebuild it from the menu after the layout or the models change.
    /// </summary>
    public static class CampusScene
    {
        public const string ScenePath = "Assets/Towerpulse/Scenes/Towerpulse.unity";
        public const string ModelDir = "Assets/Towerpulse/Art/Models/";
        public const string LayoutPath = "Assets/Towerpulse/Data/campus_layout.json";
        public const string ShotsPath = "Assets/Towerpulse/Data/shots.json";
        public const string SkyPath = "Assets/Towerpulse/Scenes/Sky_Afternoon.mat";
        public const string SkyTexturePath = "Assets/Towerpulse/Scenes/Sky_Gradient.png";
        public const string HorizonPath = "Assets/Towerpulse/Scenes/Horizon.mat";
        public const string VolumePath = "Assets/Towerpulse/Scenes/Afternoon_Volume.asset";

        static GameObject Model(string name)
        {
            var go = AssetDatabase.LoadAssetAtPath<GameObject>(ModelDir + name + ".glb");
            if (go == null) throw new FileNotFoundException("the model " + name + ".glb is not imported under " + ModelDir);
            return go;
        }

        static GameObject Place(string model, string name, Transform parent, Vector3 pos, float yaw)
        {
            var inst = (GameObject)PrefabUtility.InstantiatePrefab(Model(model), parent);
            inst.name = name;
            inst.transform.localPosition = pos;
            inst.transform.localRotation = Quaternion.Euler(0, yaw, 0);
            return inst;
        }

        [MenuItem("Towerpulse/Create campus scene")]
        public static string Build()
        {
            var scene = EditorSceneManager.NewScene(NewSceneSetup.EmptyScene, NewSceneMode.Single);
            var layout = AssetDatabase.LoadAssetAtPath<TextAsset>(LayoutPath);
            var shots = AssetDatabase.LoadAssetAtPath<TextAsset>(ShotsPath);
            var plan = CampusPlan.Parse(layout.text);

            var root = new GameObject("Campus").transform;
            Place(plan.GroundModel, "ground", root, Vector3.zero, 0);
            var buildings = new Dictionary<string, Transform>();
            foreach (var b in plan.Buildings)
                buildings[b.Id] = Place(b.Model, b.Id, root, new Vector3((float)b.X, (float)b.Y, (float)b.Z), 0).transform;
            foreach (var u in plan.RooftopUnits) Place(u.Model, u.Id, root, new Vector3((float)u.X, (float)u.Y, (float)u.Z), (float)u.Yaw);
            foreach (var u in plan.Chillers) Place(u.Model, u.Id, root, new Vector3((float)u.X, (float)u.Y, (float)u.Z), (float)u.Yaw);

            var furniture = new GameObject("Furniture").transform;
            furniture.SetParent(root, false);
            foreach (var z in plan.Zones)
            {
                var b = plan.Building(z.Building);
                var origin = new Vector3((float)b.X, (float)(b.Y + z.FloorY), (float)b.Z);
                foreach (var f in z.Furniture)
                    Place(f.Prop, z.Id + "_" + f.Prop, furniture, origin + new Vector3((float)f.X, 0, (float)f.Z), (float)f.Yaw);

                // everyone the zone could show: a seat each, then standing room for a crowded meeting. Hidden until the zone reports them.
                var people = new GameObject("People_" + z.Id).transform;
                people.SetParent(root, false);
                int n = 0;
                foreach (var s in z.Seats)
                {
                    var p = Place("person_seated_" + (n % 2 == 0 ? "a" : "b"), "Slot_" + n.ToString("00"), people, origin + new Vector3((float)s.X, 0, (float)s.Z), (float)s.Yaw);
                    p.SetActive(false);
                    n++;
                }
                if (z.Id == BuildingLayout.HotZone)
                {
                    var stand = CampusPlan.StandingSpots(z, TowerDay.MeetingPeople - z.Seats.Count);
                    int k = 0;
                    foreach (var s in stand)
                    {
                        var p = Place("person_standing_" + (k % 2 == 0 ? "a" : "b"), "Slot_" + n.ToString("00"), people, origin + new Vector3((float)s.X, 0, (float)s.Z), (float)s.Yaw);
                        p.SetActive(false);
                        n++; k++;
                    }
                }
            }

            Lighting(plan, root);
            var cam = Camera(shots);

            var host = new GameObject("Towerpulse");
            host.AddComponent<TowerpulseLiveApp>();
            var story = host.AddComponent<StoryApp>();
            story.Configure(layout, shots);
            var director = host.AddComponent<ShotDirector>();
            director.Configure(shots, cam);

            EditorSceneManager.SaveScene(scene, ScenePath);
            return ScenePath;
        }

        static Camera Camera(TextAsset shots)
        {
            var go = new GameObject("Main Camera") { tag = "MainCamera" };
            var cam = go.AddComponent<Camera>();
            cam.clearFlags = CameraClearFlags.Skybox;
            cam.nearClipPlane = 0.3f;
            cam.farClipPlane = 2500f;
            cam.fieldOfView = 38f;
            cam.allowHDR = true;
            var data = go.AddComponent<UniversalAdditionalCameraData>();
            data.renderPostProcessing = true;
            data.antialiasing = AntialiasingMode.SubpixelMorphologicalAntiAliasing;
            var res = ShotFile.Parse(shots.text);
            if (res.Ok)
            {
                res.List.TryPose(0, out var p, out _);
                go.transform.position = new Vector3((float)p.Px, (float)p.Py, (float)p.Pz);
                go.transform.rotation = Quaternion.LookRotation(new Vector3((float)p.Lx, (float)p.Ly, (float)p.Lz) - go.transform.position, Vector3.up);
                cam.fieldOfView = (float)p.Fov;
            }
            return cam;
        }

        static readonly Color Haze = new Color(0.66f, 0.75f, 0.87f), Zenith = new Color(0.28f, 0.46f, 0.84f);

        static Material Sky()
        {
            // 8 x 512, zenith at the top, haze at and below the horizon (the skybox's lower half is what a camera looking down at the horizon plane's far edge sees)
            var tex = new Texture2D(8, 512, TextureFormat.RGB24, false);
            for (int y = 0; y < 512; y++)
            {
                float v = y / 511f;                       // 0 = nadir, 1 = zenith
                float up = Mathf.Clamp01((v - 0.5f) * 2f);  // 0 at the horizon, 1 at the zenith
                var c = Color.Lerp(Haze, Zenith, Mathf.Pow(up, 0.55f));
                for (int x = 0; x < 8; x++) tex.SetPixel(x, y, c);
            }
            tex.Apply();
            System.IO.File.WriteAllBytes(SkyTexturePath, tex.EncodeToPNG());
            Object.DestroyImmediate(tex);
            AssetDatabase.ImportAsset(SkyTexturePath, ImportAssetOptions.ForceUpdate);
            var imp = (TextureImporter)AssetImporter.GetAtPath(SkyTexturePath);
            imp.sRGBTexture = true; imp.mipmapEnabled = false; imp.wrapMode = TextureWrapMode.Clamp; imp.textureCompression = TextureImporterCompression.Uncompressed;
            imp.SaveAndReimport();
            var sky = AssetDatabase.LoadAssetAtPath<Material>(SkyPath);
            if (sky == null)
            {
                sky = new Material(Shader.Find("Skybox/Panoramic"));
                AssetDatabase.CreateAsset(sky, SkyPath);
            }
            sky.shader = Shader.Find("Skybox/Panoramic");
            sky.SetTexture("_MainTex", AssetDatabase.LoadAssetAtPath<Texture2D>(SkyTexturePath));
            sky.SetFloat("_Mapping", 1f);
            sky.SetFloat("_ImageType", 0f);
            sky.EnableKeyword("_MAPPING_LATITUDE_LONGITUDE_LAYOUT");
            sky.SetFloat("_Exposure", 1f);
            EditorUtility.SetDirty(sky);
            return sky;
        }

        static void Lighting(CampusPlan plan, Transform root)
        {
            var sunGo = new GameObject("Sun (late afternoon)");
            sunGo.transform.rotation = Quaternion.Euler(27f, 38f, 0f);
            var sun = sunGo.AddComponent<Light>();
            sun.type = LightType.Directional;
            sun.color = new Color(1.0f, 0.84f, 0.66f);
            sun.intensity = 3.2f;
            sun.shadows = LightShadows.Soft;
            sun.shadowStrength = 0.8f;
            RenderSettings.sun = sun;

            // The sky is a plain vertical gradient from the zenith down to the haze colour the fog and the far ground end in, so there is no horizon
            // line to see: the procedural sky takes its tint from the sun's colour (a warm sun made the whole sky yellow-green).
            RenderSettings.skybox = Sky();
            RenderSettings.ambientMode = AmbientMode.Trilight;
            RenderSettings.ambientSkyColor = new Color(0.50f, 0.60f, 0.76f);
            RenderSettings.ambientEquatorColor = new Color(0.52f, 0.50f, 0.50f);
            RenderSettings.ambientGroundColor = new Color(0.30f, 0.29f, 0.27f);
            RenderSettings.fog = true;
            RenderSettings.fogMode = FogMode.Linear;
            RenderSettings.fogColor = Haze;
            RenderSettings.fogStartDistance = 140f;
            RenderSettings.fogEndDistance = 1100f;
            Horizon(root);

            // warm interior light, so the rooms read as lived in
            var lights = new GameObject("Interior lights").transform;
            lights.SetParent(root, false);
            foreach (var b in plan.Buildings)
                for (int f = 0; f < b.Floors; f++)
                {
                    var go = new GameObject(b.Id + "-f" + (f + 1) + "-light");
                    go.transform.SetParent(lights, false);
                    go.transform.position = new Vector3((float)b.X, (float)(b.Y + f * plan.FloorHeight + 3.0), (float)b.Z);
                    var l = go.AddComponent<Light>();
                    l.type = LightType.Point;
                    l.color = new Color(1.0f, 0.93f, 0.82f);
                    l.range = 14f;
                    l.intensity = 6f;
                    l.shadows = LightShadows.None;
                }

            var volumeGo = new GameObject("Afternoon volume");
            var volume = volumeGo.AddComponent<Volume>();
            volume.isGlobal = true;
            var profile = AssetDatabase.LoadAssetAtPath<VolumeProfile>(VolumePath);
            if (profile == null)
            {
                profile = ScriptableObject.CreateInstance<VolumeProfile>();
                AssetDatabase.CreateAsset(profile, VolumePath);
            }
            // set on every build, so a retune here is never skipped because the asset already exists
            if (!profile.TryGet(out Tonemapping tone)) tone = profile.Add<Tonemapping>();
            tone.mode.Override(TonemappingMode.ACES);
            if (!profile.TryGet(out Bloom bloom)) bloom = profile.Add<Bloom>();
            bloom.threshold.Override(1.0f);
            bloom.intensity.Override(0.9f);
            bloom.scatter.Override(0.6f);
            if (!profile.TryGet(out Vignette vig)) vig = profile.Add<Vignette>();
            vig.intensity.Override(0.2f);
            if (!profile.TryGet(out ColorAdjustments color)) color = profile.Add<ColorAdjustments>();
            color.postExposure.Override(0.25f);
            color.saturation.Override(6f);
            EditorUtility.SetDirty(profile);
            AssetDatabase.SaveAssets();
            volume.sharedProfile = profile;
        }

        // The ground model is finite; a plane far larger than the camera ever sees, fogged into the haze the sky ends in, keeps the campus from sitting on the edge of nothing.
        static void Horizon(Transform root)
        {
            var mat = AssetDatabase.LoadAssetAtPath<Material>(HorizonPath);
            if (mat == null)
            {
                mat = new Material(Shader.Find("Universal Render Pipeline/Lit"));
                AssetDatabase.CreateAsset(mat, HorizonPath);
            }
            mat.SetColor("_BaseColor", new Color(0.40f, 0.48f, 0.36f));
            mat.SetFloat("_Smoothness", 0f);
            EditorUtility.SetDirty(mat);
            var plane = GameObject.CreatePrimitive(PrimitiveType.Plane);
            plane.name = "Horizon";
            plane.transform.SetParent(root, false);
            plane.transform.position = new Vector3(0, -0.12f, 0);
            plane.transform.localScale = new Vector3(900f, 1f, 900f);
            plane.GetComponent<Renderer>().sharedMaterial = mat;
            Object.DestroyImmediate(plane.GetComponent<Collider>());
        }

        [MenuItem("Towerpulse/Play campus scene")]
        public static void Play()
        {
            if (!File.Exists(ScenePath)) Build();
            EditorSceneManager.OpenScene(ScenePath);
            EditorApplication.EnterPlaymode();
        }
    }
}
