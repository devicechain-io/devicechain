// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// The quarry's terrain in the scene's stylized look. The ground's colour comes from one site
// colour map that ArtSource/terrain/quarry_heightmap.py paints (large fields of one colour per
// material), not from tiled photographic layers. On steep ground the shader draws rock instead:
// horizontal strata whose bands wander a little, and flat facets from a 3D cell pattern in world
// space, so a face is never stretched however steep it is. A faint large-scale variation keeps the
// broad fields from looking printed.
//
// It is a Unity Terrain material: the vertex stage, the shadow, depth and depth-normal passes are
// URP's own Terrain/Lit code, so instancing, per-pixel normals and holes behave as they do there.
Shader "Sitepulse/Stylized Terrain"
{
    Properties
    {
        [NoScaleOffset] _ColorMap("Site colour map", 2D) = "grey" {}
        _RockLight("Rock, light bands", Color) = (0.70, 0.66, 0.60, 1)
        _RockDark("Rock, dark bands", Color) = (0.50, 0.47, 0.43, 1)
        _RockCool("Rock, cool bands", Color) = (0.56, 0.57, 0.58, 1)
        _RockSlope("Rock from / full at (degrees)", Vector) = (44, 54, 0, 0)
        _StrataHeight("Strata band height (m)", Float) = 1.4
        _StrataWarp("Strata wander (m)", Float) = 1.1
        _FacetSize("Facet size (m)", Float) = 3.0
        _FacetTilt("Facet tilt", Range(0, 1)) = 0.3
        _FacetShade("Facet brightness variation", Range(0, 0.3)) = 0.06
        _RubbleSize("Rubble facet size (m)", Float) = 0.9
        _RubbleTilt("Rubble facet tilt", Range(0, 1)) = 0.35
        _GroundVariation("Ground variation", Range(0, 0.2)) = 0.05
        _Smoothness("Smoothness", Range(0, 1)) = 0.04

        // set by the terrain engine
        [HideInInspector] _Control("Control (RGBA)", 2D) = "red" {}
        [HideInInspector] _Splat0("Layer 0 (R)", 2D) = "grey" {}
        [HideInInspector] _MainTex("BaseMap (RGB)", 2D) = "grey" {}
        [HideInInspector] _BaseColor("Main Color", Color) = (1,1,1,1)
        [HideInInspector] _TerrainHolesTexture("Holes Map (RGB)", 2D) = "white" {}
        [ToggleUI] _EnableInstancedPerPixelNormal("Enable Instanced per-pixel normal", Float) = 1.0
    }

    HLSLINCLUDE
    #pragma multi_compile_fragment __ _ALPHATEST_ON
    ENDHLSL

    SubShader
    {
        Tags { "Queue" = "Geometry-100" "RenderType" = "Opaque" "RenderPipeline" = "UniversalPipeline" "UniversalMaterialType" = "Lit" "IgnoreProjector" = "False" "TerrainCompatible" = "True" }

        Pass
        {
            Name "ForwardLit"
            Tags { "LightMode" = "UniversalForward" }
            HLSLPROGRAM
            #pragma target 3.5
            #pragma vertex SplatmapVert
            #pragma fragment StylizedFragment

            #pragma multi_compile _ _MAIN_LIGHT_SHADOWS _MAIN_LIGHT_SHADOWS_CASCADE _MAIN_LIGHT_SHADOWS_SCREEN
            #pragma multi_compile _ _ADDITIONAL_LIGHTS_VERTEX _ADDITIONAL_LIGHTS
            #pragma multi_compile _ _CLUSTER_LIGHT_LOOP
            #pragma multi_compile _ EVALUATE_SH_MIXED EVALUATE_SH_VERTEX
            #pragma multi_compile_fragment _ _ADDITIONAL_LIGHT_SHADOWS
            #pragma multi_compile_fragment _ _SHADOWS_SOFT _SHADOWS_SOFT_LOW _SHADOWS_SOFT_MEDIUM _SHADOWS_SOFT_HIGH
            #pragma multi_compile_fragment _ _SCREEN_SPACE_OCCLUSION
            #include_with_pragmas "Packages/com.unity.render-pipelines.universal/ShaderLibrary/Fog.hlsl"
            #pragma multi_compile_instancing
            #pragma instancing_options assumeuniformscaling nomatrices nolightprobe nolightmap

            // per-pixel normals from the terrain's normal map when instanced, as Terrain/Lit does
            #define _TERRAIN_INSTANCED_PERPIXEL_NORMAL 1

            #include "Packages/com.unity.render-pipelines.universal/Shaders/Terrain/TerrainLitInput.hlsl"
            #include "Packages/com.unity.render-pipelines.universal/Shaders/Terrain/TerrainLitPasses.hlsl"

            // Not in UnityPerMaterial: Terrain/Lit owns that buffer's layout, and a terrain is
            // never SRP-batched anyway.
            TEXTURE2D(_ColorMap); SAMPLER(sampler_ColorMap);
            half4 _RockLight, _RockDark, _RockCool;
            float4 _RockSlope;
            float _StrataHeight, _StrataWarp, _FacetSize, _FacetTilt, _FacetShade, _GroundVariation, _Smoothness;
            float _RubbleSize, _RubbleTilt;

            float Hash11(float p)
            {
                p = frac(p * 0.1031);
                p *= p + 33.33;
                p *= p + p;
                return frac(p);
            }

            float3 Hash33(float3 p)
            {
                p = frac(p * float3(0.1031, 0.1030, 0.0973));
                p += dot(p, p.yxz + 33.33);
                return frac((p.xxy + p.yxx) * p.zyx);
            }

            float ValueNoise(float2 p)
            {
                float2 i = floor(p), f = frac(p);
                float2 u = f * f * (3.0 - 2.0 * f);
                float a = Hash11(dot(i, float2(1.0, 157.0)));
                float b = Hash11(dot(i + float2(1, 0), float2(1.0, 157.0)));
                float c = Hash11(dot(i + float2(0, 1), float2(1.0, 157.0)));
                float d = Hash11(dot(i + float2(1, 1), float2(1.0, 157.0)));
                return lerp(lerp(a, b, u.x), lerp(c, d, u.x), u.y);
            }

            // The nearest of a jittered 3D grid of points: the cell a world position falls in, as
            // a random vector per cell. The points are jittered within the middle half of their
            // cells, so only the 2x2x2 cells towards the position need searching, not 3x3x3.
            float3 CellOf(float3 p)
            {
                float3 i = floor(p), f = frac(p);
                float3 o = step(0.5, f) - 1.0;                 // the lower corner of the 2x2x2 block
                float best = 1e9;
                float3 id = 0;
                [unroll] for (int z = 0; z <= 1; z++)
                [unroll] for (int y = 0; y <= 1; y++)
                [unroll] for (int x = 0; x <= 1; x++)
                {
                    float3 g = o + float3(x, y, z);
                    float3 h = Hash33(i + g);
                    float3 d = g + 0.25 + 0.5 * h - f;
                    float dd = dot(d, d);
                    if (dd < best) { best = dd; id = h; }
                }
                return id;
            }

            void StylizedFragment(Varyings IN, out half4 outColor : SV_Target0
            #ifdef _WRITE_RENDERING_LAYERS
                , out uint outRenderingLayers : SV_Target1
            #endif
            )
            {
                UNITY_SETUP_STEREO_EYE_INDEX_POST_VERTEX(IN);
            #ifdef _ALPHATEST_ON
                ClipHoles(IN.uvMainAndLM.xy);
            #endif
                InputData inputData;
                InitializeInputData(IN, half3(0, 0, 1), inputData);

                float3 p = IN.positionWS;
                half3 n = inputData.normalWS;
                half4 site = SAMPLE_TEXTURE2D(_ColorMap, sampler_ColorMap, IN.uvMainAndLM.xy);
                half3 albedo = site.rgb;

                // a faint, large-scale variation over everything
                float v = ValueNoise(p.xz / 23.0) * 0.6 + ValueNoise(p.xz / 7.0) * 0.4;
                albedo *= 1.0 + _GroundVariation * (v * 2.0 - 1.0);

                float slopeDeg = degrees(acos(saturate(n.y)));
                half rock = smoothstep(_RockSlope.x, _RockSlope.y, slopeDeg);
                UNITY_BRANCH if (rock > 0.001)
                {
                    // strata: bands of light, dark and cool rock along the bench, wandering a little
                    float y = p.y + _StrataWarp * (ValueNoise(p.xz / 19.0) * 2.0 - 1.0)
                                  + 0.35 * _StrataWarp * (ValueNoise(p.xz / 5.0) * 2.0 - 1.0);
                    float b = y / _StrataHeight;
                    float k = floor(b);
                    float t = smoothstep(0.82, 1.0, frac(b));
                    float h0 = lerp(Hash11(k * 7.31 + 1.7), Hash11((k + 1.0) * 7.31 + 1.7), t);
                    float h1 = lerp(Hash11(k * 3.17 + 9.2), Hash11((k + 1.0) * 3.17 + 9.2), t);
                    half3 band = lerp(_RockDark.rgb, _RockLight.rgb, smoothstep(0.15, 0.85, h0));
                    band = lerp(band, _RockCool.rgb, step(0.72, h1) * 0.7);

                    // facets: flat planes, one per cell of a 3D pattern, tilted at random
                    float3 cell = CellOf(p / _FacetSize);
                    half3 up = half3(0, 1, 0);
                    half3 tangent = normalize(cross(up, n) + half3(1e-4, 0, 0));
                    half3 bitangent = cross(n, tangent);
                    float2 tilt = (cell.xy * 2.0 - 1.0) * _FacetTilt;
                    half3 faceted = normalize(n + tangent * tilt.x + bitangent * (tilt.y - 0.15 * _FacetTilt));
                    band *= 1.0 + _FacetShade * (cell.z * 2.0 - 1.0);

                    albedo = lerp(albedo, band, rock);
                    inputData.normalWS = normalize(lerp(n, faceted, rock));
                }

                // loose broken rock (the colour map's alpha): small facets, no strata
                half rubble = site.a * (1.0 - rock);
                UNITY_BRANCH if (rubble > 0.01)
                {
                    float3 cell = CellOf(p / _RubbleSize);
                    half3 nn = inputData.normalWS;
                    half3 tangent = normalize(cross(nn, half3(0, 0, 1)) + half3(0, 0, 1e-4));
                    half3 bitangent = cross(nn, tangent);
                    float2 tilt = (cell.xy * 2.0 - 1.0) * _RubbleTilt;
                    half3 faceted = normalize(nn + tangent * tilt.x + bitangent * tilt.y);
                    inputData.normalWS = normalize(lerp(nn, faceted, rubble));
                    albedo *= 1.0 + 0.1 * rubble * (cell.z * 2.0 - 1.0);
                }

                InitializeBakedGIData(IN, inputData);
                half4 color = UniversalFragmentPBR(inputData, albedo, 0.0h, half3(0, 0, 0), _Smoothness, 1.0h, half3(0, 0, 0), 1.0h);
                SplatmapFinalColor(color, inputData.fogCoord);
                outColor = half4(color.rgb, 1.0h);
            #ifdef _WRITE_RENDERING_LAYERS
                outRenderingLayers = EncodeMeshRenderingLayer();
            #endif
            }
            ENDHLSL
        }

        UsePass "Universal Render Pipeline/Terrain/Lit/SHADOWCASTER"
        UsePass "Universal Render Pipeline/Terrain/Lit/DEPTHONLY"
        UsePass "Universal Render Pipeline/Terrain/Lit/DEPTHNORMALS"
        UsePass "Hidden/Nature/Terrain/Utilities/PICKING"
    }
}
