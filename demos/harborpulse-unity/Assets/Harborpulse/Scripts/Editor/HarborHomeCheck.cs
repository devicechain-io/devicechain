// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

using System;
using System.Collections.Generic;
using System.Globalization;
using System.IO;
using System.Linq;
using DeviceChain.Harborpulse.Model.Director;
using DeviceChain.Harborpulse.Visuals;
using UnityEditor;
using UnityEditor.SceneManagement;
using UnityEngine;
using UnityEngine.Rendering.Universal;

namespace DeviceChain.Harborpulse.EditorTools
{
    public static partial class HarborStills
    {
        sealed class Pixels
        {
            public long Count;public int MinY=int.MaxValue,MaxY=-1,MinX=int.MaxValue,MaxX=-1;
            public int Height=>Count==0?0:MaxY-MinY+1;
            public void Add(int x,int y){Count++;MinY=Math.Min(MinY,y);MaxY=Math.Max(MaxY,y);MinX=Math.Min(MinX,x);MaxX=Math.Max(MaxX,x);}
        }
        sealed class Check
        {
            public int Width,Height; public Dictionary<string,Pixels> Pixels;
            public double Water=> (Pixels["water"].Count+Pixels["mouth"].Count)/(double)(Width*Height);
            public double Sky=>Pixels["sky"].Count/(double)(Width*Height);
            public int Devices=>Pixels.Count(p=>p.Key.StartsWith("device:",StringComparison.Ordinal)&&p.Value.Height>=8);
            public bool Passed=>Water<=.45&&Sky<=.25&&Devices>=30&&new[]{"hp-north-quay","hp-east-quay","hp-basin","mouth","ridge"}.All(k=>Pixels[k].Count>0);
        }
        static HarborpulseApp PreviewApp()
        {
            EditorSceneManager.OpenScene(HarborSceneBuilder.ScenePath,OpenSceneMode.Single);
            var app=UnityEngine.Object.FindAnyObjectByType<HarborpulseApp>();
            if(app==null)throw new InvalidOperationException("Build Scene first");
            app.offlinePreview=true;app.Initialize();
            if(app.Problem!=null){app.Teardown();throw new InvalidOperationException(app.Problem);}
            return app;
        }
        static string OutputDirectory()
        {
            var args=Environment.GetCommandLineArgs();var at=Array.IndexOf(args,"-hp-stills-dir");
            if(at<0||at+1>=args.Length)throw new ArgumentException("-hp-stills-dir is required");
            var dir=Path.GetFullPath(args[at+1]);Directory.CreateDirectory(dir);return dir;
        }
        public static void HomeCheckFromCommandLine()
        {
            HarborpulseApp app=null;
            try
            {
                var dir=OutputDirectory();app=PreviewApp();app.WaterTimeOverride=37.5f;app.HoldPreviewAt(12.5);
                // the scene's size, for the performance budget: renderers, vertices, and how many of each cast shadows
                // the far level only: the near-detail sets are built hidden, drawn only close to a camera, and counted on their own line below
                var filters=app.World.Root.GetComponentsInChildren<MeshFilter>(true).Where(f=>f.GetComponentInParent<NearDetailRoot>(true)==null).ToArray();
                long vertices=filters.Sum(f=>(long)(f.sharedMesh!=null?f.sharedMesh.vertexCount:0));
                long casting=filters.Where(f=>f.GetComponent<MeshRenderer>()!=null&&f.GetComponent<MeshRenderer>().shadowCastingMode!=UnityEngine.Rendering.ShadowCastingMode.Off).Sum(f=>(long)(f.sharedMesh!=null?f.sharedMesh.vertexCount:0));
                Debug.Log("[harborpulse] world: "+filters.Length+" renderers, "+vertices+" vertices, "+casting+" of them casting shadows");
                app.World.Detail.Stats(out var built,out var shown,out var nearVertices,out var shownVertices);
                Debug.Log("[harborpulse] near detail at HOME: "+app.World.Detail.Count+" sets, "+built+" built hidden ("+nearVertices+" vertices), "+shown+" shown ("+shownVertices+" vertices)");
                // (the camera is wherever the director left it here, so nothing is asserted yet: HOME is asserted once the HOME pose is set below)
                var checks=new List<Check>();
                foreach(var vertical in new[]{false,true})
                {
                    var width=vertical?1080:1920;var height=vertical?1920:1080;
                    app.sceneCamera.aspect=width/(float)height;
                    app.DirectorCamera.ForcePose(vertical?"pose:home:portrait":"pose:home");
                    // Re-project the camera-space HUD at each aspect, then freeze camera ownership.
                    app.RefreshHud(); Canvas.ForceUpdateCanvases();
                    // a forced decision from scratch for the HOME camera: no set may be near, whatever the camera showed before the cut
                    app.World.Detail.Refresh(app.sceneCamera.transform.position,true);
                    app.World.Detail.Stats(out _,out var nearShown,out _,out _);
                    if(nearShown>0)throw new InvalidOperationException("HOME must draw the far level only, but "+nearShown+" near-detail sets are shown at "+(vertical?"portrait":"landscape"));
                    var name=vertical?"home-portrait":"home-landscape";
                    // The camera-space Canvas does not render reliably outside Play mode (its dynamic font atlas is not built), so the
                    // HOME beauty frame is the scene alone with the burnt-in stamp; HUD-bearing frames come from Play mode or the player.
                    var hudShown=app.Hud?.Visible??false;if(app.Hud!=null)app.Hud.Visible=false;
                    Render(app.sceneCamera,Path.Combine(dir,name+"-beauty.png"),width,height,"OFFLINE PREVIEW - HOME 12:30",app);
                    if(app.Hud!=null)app.Hud.Visible=hudShown;
                    var check=Mask(app,Path.Combine(dir,name+"-masks.png"),width,height);checks.Add(check);
                    Debug.Log("[harborpulse] "+name+" water="+check.Water.ToString("P2",CultureInfo.InvariantCulture)+" sky="+check.Sky.ToString("P2",CultureInfo.InvariantCulture)+" devices="+check.Devices+"/"+app.World.Rigs.Count+" "+(check.Passed?"PASS":"FAIL"));
                }
                File.WriteAllText(Path.Combine(dir,"home-check.json"),DirectorWire.Json(w=>
                {
                    w.WriteStartObject();w.WriteString("mode","OFFLINE PREVIEW");w.WriteString("simTime","12:30");w.WriteStartArray("aspects");
                    foreach(var c in checks)
                    {
                        w.WriteStartObject();w.WriteNumber("width",c.Width);w.WriteNumber("height",c.Height);w.WriteNumber("waterFraction",c.Water);w.WriteNumber("skyFraction",c.Sky);w.WriteNumber("devicesVisible",c.Devices);w.WriteBoolean("passed",c.Passed);
                        w.WriteStartObject("visible");foreach(var k in new[]{"hp-north-quay","hp-east-quay","hp-basin","mouth","ridge"})w.WriteBoolean(k,c.Pixels[k].Count>0);w.WriteEndObject();
                        w.WriteStartObject("devices");foreach(var p in c.Pixels.Where(p=>p.Key.StartsWith("device:",StringComparison.Ordinal)))
                        {w.WriteStartObject(p.Key.Substring(7));w.WriteNumber("visiblePixels",p.Value.Count);w.WriteNumber("bboxHeightPx",p.Value.Height);w.WriteNumber("minX",p.Value.Count==0?-1:p.Value.MinX);w.WriteNumber("maxX",p.Value.MaxX);w.WriteNumber("minY",p.Value.Count==0?-1:p.Value.MinY);w.WriteNumber("maxY",p.Value.MaxY);w.WriteEndObject();}w.WriteEndObject();w.WriteEndObject();
                    }
                    w.WriteEndArray();w.WriteEndObject();
                }));
                app.Teardown();app=null;EditorApplication.Exit(checks.All(c=>c.Passed)?0:1);
            }
            catch(Exception e){Debug.LogError("[harborpulse] HOME check: "+e);app?.Teardown();EditorApplication.Exit(1);}
        }
        static Check Mask(HarborpulseApp app,string path,int width,int height)
        {
            var names=new List<string>{"sky","water","land","ridge","hp-north-quay","hp-east-quay","hp-basin","mouth"};
            names.AddRange(app.World.Rigs.Select(r=>"device:"+r.Placement.Device.Token));
            var palette=new List<Color32>{new Color32(0,0,0,255)};
            for(var i=1;i<names.Count;i++)palette.Add(new Color32((byte)(32+(i%4)*64),(byte)(32+((i/4)%4)*64),(byte)(32+((i/16)%4)*64),255));
            var materials=palette.Select((c,i)=>{var m=new Material(Shader.Find("Universal Render Pipeline/Unlit")){name="HOME mask "+names[i]};m.SetColor("_BaseColor",c);m.SetFloat("_Cull",0);return m;}).ToArray();
            var renderers=app.GetComponentsInChildren<Renderer>(true);
            var original=renderers.ToDictionary(r=>r,r=>r.sharedMaterials);
            var classes=new Dictionary<Renderer,string>();
            foreach(var category in app.World.Masks)foreach(var renderer in category.Value)classes[renderer]=category.Key;
            foreach(var rig in app.World.Rigs)
                foreach(var renderer in rig.Root.GetComponentsInChildren<Renderer>(true))classes[renderer]="device:"+rig.Placement.Device.Token;
            var cam=app.sceneCamera;var sky=RenderSettings.skybox;var fog=RenderSettings.fog;var clear=cam.clearFlags;var background=cam.backgroundColor;var target=cam.targetTexture;var aspect=cam.aspect;var active=RenderTexture.active;
            var urp=cam.GetUniversalAdditionalCameraData();var post=urp.renderPostProcessing;var aa=urp.antialiasing;
            var hud=app.Hud?.Visible??false;
            // A mask-only water patch partitions the mapped mouth from the same flat water plane.
            // The measured water fraction includes both colours. It is absent from the beauty pass.
            var mouth=app.Map.Mouth;var marker=new MeshKit().Slab((float)mouth.Min(p=>p.X),(float)mouth.Max(p=>p.X),(float)mouth.Min(p=>p.Z),(float)mouth.Max(p=>p.Z),.001f,.015f).Spawn("Mouth mask",app.transform,materials[names.IndexOf("mouth")],false);
            var rt=new RenderTexture(width,height,24,RenderTextureFormat.ARGB32,RenderTextureReadWrite.Linear){antiAliasing=1};
            var tex=new Texture2D(width,height,TextureFormat.RGB24,false,true);
            try
            {
                if(app.Hud!=null)app.Hud.Visible=false;
                RenderSettings.skybox=null;RenderSettings.fog=false;cam.clearFlags=CameraClearFlags.SolidColor;cam.backgroundColor=Color.black;urp.renderPostProcessing=false;urp.antialiasing=AntialiasingMode.None;
                foreach(var r in renderers)
                {
                    var category=classes.TryGetValue(r,out var c)?c:r.gameObject.layer==PlanarWaterReflection.WaterLayer?"water":"land";
                    var index=names.IndexOf(category);if(index<0)index=names.IndexOf("land");
                    r.sharedMaterials=Enumerable.Repeat(materials[index],Math.Max(1,original[r].Length)).ToArray();
                }
                cam.aspect=width/(float)height;cam.targetTexture=rt;Canvas.ForceUpdateCanvases();cam.Render();RenderTexture.active=rt;
                tex.ReadPixels(new Rect(0,0,width,height),0,0);tex.Apply();File.WriteAllBytes(path,tex.EncodeToPNG());
                var result=new Check{Width=width,Height=height,Pixels=names.ToDictionary(n=>n,n=>new Pixels())};
                var pixels=tex.GetPixels32();
                // URP converts an sRGB _BaseColor to linear before writing a linear target, so compare against the converted palette.
                byte Linear(byte v)=>QualitySettings.activeColorSpace==ColorSpace.Linear?(byte)Mathf.Clamp(Mathf.RoundToInt(Mathf.GammaToLinearSpace(v/255f)*255),0,255):v;
                var expected=palette.Select(p=>new Color32(Linear(p.r),Linear(p.g),Linear(p.b),255)).ToList();
                for(var i=0;i<pixels.Length;i++)
                {
                    var pixel=pixels[i];var nearest=0;var best=int.MaxValue;
                    for(var j=0;j<expected.Count;j++)
                    {var p=expected[j];var distance=(pixel.r-p.r)*(pixel.r-p.r)+(pixel.g-p.g)*(pixel.g-p.g)+(pixel.b-p.b)*(pixel.b-p.b);if(distance<best){best=distance;nearest=j;}}
                    // Flat unlit, linear RT, no post or AA: interior mask pixels are exact.
                    // The small tolerance excludes edge blending instead of inventing visibility.
                    if(best<=27)result.Pixels[names[nearest]].Add(i%width,i/width);
                }
                return result;
            }
            finally
            {
                foreach(var p in original)p.Key.sharedMaterials=p.Value;
                RenderSettings.skybox=sky;RenderSettings.fog=fog;cam.clearFlags=clear;cam.backgroundColor=background;cam.targetTexture=target;cam.aspect=aspect;RenderTexture.active=active;urp.renderPostProcessing=post;urp.antialiasing=aa;if(app.Hud!=null)app.Hud.Visible=hud;
                rt.Release();UnityEngine.Object.DestroyImmediate(rt);UnityEngine.Object.DestroyImmediate(tex);MeshKit.Release(marker.GetComponent<MeshFilter>().sharedMesh);UnityEngine.Object.DestroyImmediate(marker);foreach(var m in materials)UnityEngine.Object.DestroyImmediate(m);
            }
        }
        public static void DirectorContactSheetFromCommandLine()
        {
            HarborpulseApp app=null;
            try
            {
                var dir=OutputDirectory();app=PreviewApp();
                var args=Environment.GetCommandLineArgs();var at=Array.IndexOf(args,"-hp-director-hours");
                var hours=(at>=0&&at+1<args.Length?args[at+1]:"6.1,8.6,11.1,12.6,14.2,17.6,17.8,19.4,20.55,21.4,26.5").Split(',').Select(s=>double.Parse(s,CultureInfo.InvariantCulture)).OrderBy(h=>h).ToArray();
                app.BeginDirectorPreview(6);
                var images=new List<Texture2D>();var current=6.0;
                foreach(var hour in hours)
                {
                    while(current<hour){var step=Math.Min(1.0/60,hour-current);app.AdvanceDirectorPreview((float)(step*app.PreviewSecondsPerHour));current+=step;}
                    Canvas.ForceUpdateCanvases();var name="director-"+HudModel.Clock(hour).Replace(':','h')+"-page-"+(app.DirectorView?.Page??"ambient")+".png";
                    var file=Path.Combine(dir,name);Render(app.sceneCamera,file,1280,720,"OFFLINE PREVIEW - DIRECTOR",app);
                    var image=new Texture2D(2,2);image.LoadImage(File.ReadAllBytes(file));images.Add(image);
                }
                var columns=3;var rows=(images.Count+columns-1)/columns;var sheet=new Texture2D(columns*1280,rows*720,TextureFormat.RGB24,false);
                for(var i=0;i<images.Count;i++)sheet.SetPixels((i%columns)*1280,(rows-1-i/columns)*720,1280,720,images[i].GetPixels());
                sheet.Apply();File.WriteAllBytes(Path.Combine(dir,"director-contact-sheet.png"),sheet.EncodeToPNG());
                foreach(var image in images)UnityEngine.Object.DestroyImmediate(image);UnityEngine.Object.DestroyImmediate(sheet);
                app.Teardown();app=null;EditorApplication.Exit(0);
            }
            catch(Exception e){Debug.LogError("[harborpulse] director stills: "+e);app?.Teardown();EditorApplication.Exit(1);}
        }
    }
}
