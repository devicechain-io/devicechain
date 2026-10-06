// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0
//
// Run in the open Editor by `phase-a-acceptance.sh --build editor` (copied to EditorScratch/phaseA_build.cs and
// evaluated by $UNITY_EVAL). Not part of the Unity project: it lives outside Assets.

var outFile = "EditorScratch/phaseA_build.result.txt";
System.IO.File.WriteAllText(outFile, "RUNNING");
{
  string res;
  try { res = "OK " + DeviceChain.Sitepulse.EditorTools.BuildPlayer.Run(); }
  catch (System.Exception e) { res = "FAILED " + e.Message; }
  System.IO.File.WriteAllText(outFile, res);
}
return "built";
