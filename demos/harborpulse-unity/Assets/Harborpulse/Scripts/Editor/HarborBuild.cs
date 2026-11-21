// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

using System;
using System.IO;
using UnityEditor;
using UnityEditor.Build.Reporting;
using UnityEngine;

namespace DeviceChain.Harborpulse.EditorTools
{
    public static class HarborBuild
    {
        public static void BuildWindowsPlayer()
        {
            try
            {
                var args=Environment.GetCommandLineArgs();var at=Array.IndexOf(args,"-hp-build-dir");
                if(at<0||at+1>=args.Length)throw new ArgumentException("-hp-build-dir is required");
                var dir=Path.GetFullPath(args[at+1]);Directory.CreateDirectory(dir);
                HarborSceneBuilder.Build();
                var report=BuildPipeline.BuildPlayer(new BuildPlayerOptions { scenes=new[]{HarborSceneBuilder.ScenePath}, locationPathName=Path.Combine(dir,"Harborpulse.exe"), target=BuildTarget.StandaloneWindows64, options=BuildOptions.None });
                Debug.Log("[harborpulse] Windows player: "+report.summary.result+" ("+report.summary.totalErrors+" errors)");
                EditorApplication.Exit(report.summary.result==BuildResult.Succeeded?0:1);
            }
            catch(Exception e){Debug.LogError("[harborpulse] player build: "+e);EditorApplication.Exit(1);}
        }

        /// <summary>
        /// A browser build of the scene, for checking that the shaders draw under WebGL 2 and not only compile:
        ///
        ///   Unity -batchmode -projectPath demos/harborpulse-unity -executeMethod DeviceChain.Harborpulse.EditorTools.HarborBuild.BuildWebGLPlayer -hp-build-dir OUT
        ///
        /// Serve OUT over http and open index.html?hp-offline&amp;hp-start-hour=17.3 (the page address carries the options: see WebOptions).
        /// The build switches the Editor's active target to WebGL and leaves it there.
        /// </summary>
        public static void BuildWebGLPlayer()
        {
            try
            {
                var args=Environment.GetCommandLineArgs();var at=Array.IndexOf(args,"-hp-build-dir");
                if(at<0||at+1>=args.Length)throw new ArgumentException("-hp-build-dir is required");
                var dir=Path.GetFullPath(args[at+1]);Directory.CreateDirectory(dir);
                HarborSceneBuilder.Build();
                var report=BuildPipeline.BuildPlayer(new BuildPlayerOptions { scenes=new[]{HarborSceneBuilder.ScenePath}, locationPathName=dir, target=BuildTarget.WebGL, options=BuildOptions.None });
                Debug.Log("[harborpulse] WebGL player: "+report.summary.result+" ("+report.summary.totalErrors+" errors)");
                EditorApplication.Exit(report.summary.result==BuildResult.Succeeded?0:1);
            }
            catch(Exception e){Debug.LogError("[harborpulse] WebGL build: "+e);EditorApplication.Exit(1);}
        }
    }
}
