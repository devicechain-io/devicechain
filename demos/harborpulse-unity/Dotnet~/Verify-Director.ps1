# Copyright The DeviceChain Authors
# SPDX-License-Identifier: Apache-2.0

param([string]$Filter = '', [switch]$CompileCheck, [switch]$PrepareOnly)
$ErrorActionPreference = 'Stop'
$repo = (Resolve-Path "$PSScriptRoot/../../..").Path.Replace('\', '/')
$build = "$($PSScriptRoot.Replace('\', '/'))/.build/u2b"
New-Item -ItemType Directory -Force $build | Out-Null
$sdk = "$HOME/.dotnet/dotnet.exe"
if (!(Test-Path $sdk)) { $sdk = (Get-Command dotnet).Source }
$env:PATH = "$(Split-Path $sdk);$env:PATH"
$env:DOTNET_CLI_HOME = "$build/home"
$env:APPDATA = "$build/roaming"
$env:LOCALAPPDATA = "$build/local"
if (!$env:NUGET_PACKAGES) { $env:NUGET_PACKAGES = "$HOME/.nuget/packages" }
$env:NUGET_HTTP_CACHE_PATH = "$build/cache"
$env:DOTNET_CLI_TELEMETRY_OPTOUT = '1'
$env:DOTNET_SKIP_FIRST_TIME_EXPERIENCE = '1'
$env:DOTNET_CLI_USE_MSBUILD_SERVER = '0'
$env:MSBUILDDISABLENODEREUSE = '1'
@"
<Project>
  <Import Project="$repo/demos/harborpulse-unity/Dotnet~/Directory.Build.props" Condition="'`$(MSBuildProjectName)' == 'DeviceChain.Harborpulse.Model' Or '`$(MSBuildProjectName)' == 'DeviceChain.Harborpulse.Tests'" />
  <Import Project="$repo/demos/shared/io.devicechain.demo-platform/Dotnet~/Directory.Build.props" Condition="'`$(MSBuildProjectName)' == 'DeviceChain.Demos.Core'" />
  <Import Project="$repo/demos/shared/io.devicechain.demo-hud/Dotnet~/Directory.Build.props" Condition="'`$(MSBuildProjectName)' == 'DeviceChain.Demos.Hud.Model'" />
  <Import Project="$repo/sdks/csharp/Directory.Build.props" Condition="'`$(MSBuildProjectName)' == 'DeviceChain.Sdk'" />
  <PropertyGroup>
    <BaseIntermediateOutputPath>$build/`$(MSBuildProjectName)/obj/</BaseIntermediateOutputPath>
    <MSBuildProjectExtensionsPath>$build/`$(MSBuildProjectName)/obj/</MSBuildProjectExtensionsPath>
    <BaseOutputPath>$build/`$(MSBuildProjectName)/bin/</BaseOutputPath>
    <TargetPlatformSdkPath>$build/sdk/</TargetPlatformSdkPath>
    <TargetPlatformDisplayName>Portable</TargetPlatformDisplayName>
    <NuGetAudit>false</NuGetAudit>
    <DefaultItemExcludes>`$(DefaultItemExcludes);**/obj/**;**/bin/**;**/.build/**</DefaultItemExcludes>
  </PropertyGroup>
</Project>
"@ | Set-Content "$build/Directory.Build.props"
'<configuration><packageSources><clear /></packageSources></configuration>' | Set-Content "$build/nuget.config"
$properties = @("-p:UseSharedCompilation=false", "-nodeReuse:false", "-p:DirectoryBuildPropsPath=$build/Directory.Build.props", "-p:RestoreConfigFile=$build/nuget.config")
if ($PrepareOnly) { return }
Push-Location $PSScriptRoot
try {
    if ($CompileCheck) {
        Set-Location $repo
        & bash sdks/unity/tools/UnityCompileCheck/compile-check.sh @properties
        if ($LASTEXITCODE) { throw "Unity compile check failed: $LASTEXITCODE" }
    } else {
        & $sdk build -c Release @properties
        if ($LASTEXITCODE) { throw "Model build failed: $LASTEXITCODE" }
        $selection = @()
        if ($Filter) { $selection = @('--filter', "FullyQualifiedName~$Filter") }
        & $sdk test --no-build -c Release @properties @selection --logger 'trx;LogFileName=director.trx' --results-directory "$build/results"
        if ($LASTEXITCODE) { throw "Model tests failed: $LASTEXITCODE" }
        if (!$Filter) {
            & python -m unittest test_check_director_log.py
            if ($LASTEXITCODE) { throw "Python caption checker tests failed: $LASTEXITCODE" }
        }
    }
} finally { Pop-Location }
