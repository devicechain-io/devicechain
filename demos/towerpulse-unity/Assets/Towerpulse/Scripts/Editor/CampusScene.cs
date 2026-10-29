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
    /// show (hidden until a zone reports someone there), dusk lighting, the camera, and the one GameObject that runs the plane and the story. Nothing in the scene
    /// is hand-placed: rebuild it from the menu after the layout or the models change.
    /// </summary>
    public static class CampusScene
    {
        public const string ScenePath = "Assets/Towerpulse/Scenes/Towerpulse.unity";
        public const string ModelDir = "Assets/Towerpulse/Art/Models/";
        public const string LayoutPath = "Assets/Towerpulse/Data/campus_layout.json";
        public const string ShotsPath = "Assets/Towerpulse/Data/shots.json";
        public const string SkyPath = "Assets/Towerpulse/Scenes/Sky_Dusk.mat";
        public const string HorizonPath = "Assets/Towerpulse/Scenes/Horizon.mat";
        public const string VolumePath = "Assets/Towerpulse/Scenes/Dusk_Volume.asset";

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

        static void Lighting(CampusPlan plan, Transform root)
        {
            var sunGo = new GameObject("Sun (dusk)");
            sunGo.transform.rotation = Quaternion.Euler(9f, 55f, 0f);
            var sun = sunGo.AddComponent<Light>();
            sun.type = LightType.Directional;
            sun.color = new Color(1.0f, 0.62f, 0.36f);
            sun.intensity = 2.4f;
            sun.shadows = LightShadows.Soft;
            sun.shadowStrength = 0.85f;
            RenderSettings.sun = sun;

            var sky = AssetDatabase.LoadAssetAtPath<Material>(SkyPath);
            if (sky == null)
            {
                sky = new Material(Shader.Find("Skybox/Procedural"));
                AssetDatabase.CreateAsset(sky, SkyPath);
            }
            sky.SetFloat("_SunSize", 0.03f);
            sky.SetFloat("_SunSizeConvergence", 8f);
            sky.SetFloat("_AtmosphereThickness", 0.7f);
            sky.SetColor("_SkyTint", new Color(0.30f, 0.36f, 0.66f));
            sky.SetColor("_GroundColor", new Color(0.12f, 0.12f, 0.15f));
            sky.SetFloat("_Exposure", 0.85f);
            EditorUtility.SetDirty(sky);
            RenderSettings.skybox = sky;
            RenderSettings.ambientMode = AmbientMode.Trilight;
            RenderSettings.ambientSkyColor = new Color(0.30f, 0.37f, 0.58f);
            RenderSettings.ambientEquatorColor = new Color(0.34f, 0.28f, 0.32f);
            RenderSettings.ambientGroundColor = new Color(0.10f, 0.09f, 0.11f);
            RenderSettings.fog = true;
            RenderSettings.fogMode = FogMode.Linear;
            RenderSettings.fogColor = new Color(0.30f, 0.37f, 0.60f);
            RenderSettings.fogStartDistance = 60f;
            RenderSettings.fogEndDistance = 360f;
            Horizon(root);

            // warm interior light, so the rooms read as lived in at dusk
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
                    l.color = new Color(1.0f, 0.78f, 0.52f);
                    l.range = 14f;
                    l.intensity = 14f;
                    l.shadows = LightShadows.None;
                }

            var volumeGo = new GameObject("Dusk volume");
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
            vig.intensity.Override(0.26f);
            if (!profile.TryGet(out ColorAdjustments color)) color = profile.Add<ColorAdjustments>();
            color.postExposure.Override(0.85f);
            color.saturation.Override(10f);
            EditorUtility.SetDirty(profile);
            AssetDatabase.SaveAssets();
            volume.sharedProfile = profile;
        }

        // The ground model is finite; a dark plane far larger than the camera ever sees, fogged into the sky, keeps the campus from sitting on the edge of nothing.
        static void Horizon(Transform root)
        {
            var mat = AssetDatabase.LoadAssetAtPath<Material>(HorizonPath);
            if (mat == null)
            {
                mat = new Material(Shader.Find("Universal Render Pipeline/Lit"));
                AssetDatabase.CreateAsset(mat, HorizonPath);
            }
            mat.SetColor("_BaseColor", new Color(0.10f, 0.11f, 0.15f));
            mat.SetFloat("_Smoothness", 0f);
            EditorUtility.SetDirty(mat);
            var plane = GameObject.CreatePrimitive(PrimitiveType.Plane);
            plane.name = "Horizon";
            plane.transform.SetParent(root, false);
            plane.transform.position = new Vector3(0, -0.12f, 0);
            plane.transform.localScale = new Vector3(300f, 1f, 300f);
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
