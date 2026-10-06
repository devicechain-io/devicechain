// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// The quarry's terrain in the scene's stylized look. The ground's colour comes from the site
// colour maps that ArtSource/terrain/quarry_heightmap.py paints (one flat colour per material with
// crisp edges), not from tiled photographic layers: a quarter-metre map over the work site and a
// half-metre one over the whole terrain. On steep ground the shader draws rock instead:
// horizontal strata whose bands wander a little, and flat facets from a 3D cell pattern in world
// space, so a face is never stretched however steep it is. The colour map's alpha classes the
// ground, and the worked ground, the crushed products and loose rock get facets of their own, so
// the ground is cut into the same flat planes as the rocks and the machines. A faint large-scale
// variation keeps the broad fields from looking printed.
//
// It is a Unity Terrain material: the vertex stage, the shadow, depth and depth-normal passes are
// URP's own Terrain/Lit code, so instancing, per-pixel normals and holes behave as they do there.
Shader "Sitepulse/Stylized Terrain"
{
    Properties
    {
        [NoScaleOffset] _ColorMap("Site colour map", 2D) = "grey" {}
        [NoScaleOffset] _ColorMapCore("Work site colour map", 2D) = "grey" {}
        _CoreRect("Work site map: x0, z0, edge (m)", Vector) = (0, 0, 1, 0)
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
        _GroundFacet("Worked ground facets: size (m), tilt, brightness", Vector) = (2.4, 0.1, 0.04, 0)
        _PileFacet("Product pile facets: size (m), tilt, brightness", Vector) = (1.6, 0.4, 0.06, 0)
        _FacetFar("Ground facets out to (m)", Float) = 220
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
            TEXTURE2D(_ColorMapCore); SAMPLER(sampler_ColorMapCore);
            half4 _RockLight, _RockDark, _RockCool;
            float4 _RockSlope, _CoreRect, _GroundFacet, _PileFacet;
            float _FacetFar;
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

            // The 2D version of CellOf, over the ground plan: half the cells to search, and enough
            // for ground no steeper than a pile's angle of repose
            float3 CellOf2(float2 p)
            {
                float2 i = floor(p), f = frac(p);
                float2 o = step(0.5, f) - 1.0;
                float best = 1e9;
                float3 id = 0;
                [unroll] for (int y = 0; y <= 1; y++)
                [unroll] for (int x = 0; x <= 1; x++)
                {
                    float2 g = o + float2(x, y);
                    float3 h = Hash33(float3(i + g, 7.0));
                    float2 d = g + 0.25 + 0.5 * h.xy - f;
                    float dd = dot(d, d);
                    if (dd < best) { best = dd; id = h; }
                }
                return id;
            }

            // A flat facet: the normal tilted at random, one tilt per cell of the 2D pattern over
            // the ground plan; w is that cell's random brightness, -1..1
            half3 Facet(half3 n, float3 p, float size, float tilt, out float w)
            {
                float3 cell = CellOf2(p.xz / size);
                half3 tangent = normalize(cross(n, half3(0, 0, 1)) + half3(0, 0, 1e-4));
                half3 bitangent = cross(n, tangent);
                float2 t = (cell.xy * 2.0 - 1.0) * tilt;
                w = cell.z * 2.0 - 1.0;
                return normalize(n + tangent * t.x + bitangent * t.y);
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
                // the work site's sharper map, fading into the whole terrain's over its last metres
                float2 cuv = (p.xz - _CoreRect.xy) / _CoreRect.z;
                float2 inside = saturate(min(cuv, 1.0 - cuv) * (_CoreRect.z / 6.0));
                half core = inside.x * inside.y;
                UNITY_BRANCH if (core > 0.0)
                    site = lerp(site, SAMPLE_TEXTURE2D(_ColorMapCore, sampler_ColorMapCore, cuv), core);
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

                // the colour map's alpha classes the ground: 0 natural, 1/3 worked ground, 2/3 crushed
                // product, 1 loose broken rock; each but the natural ground is cut into facets
                half a = site.a;
                half worked = saturate(1.0 - abs(a - 0.3333) * 3.0) * (1.0 - rock);
                half pile = saturate(1.0 - abs(a - 0.6667) * 3.0) * (1.0 - rock);
                half rubble = saturate((a - 0.6667) * 3.0) * (1.0 - rock);
                // facets are lost in the distance: leave them out past it
                float far = saturate((_FacetFar - distance(p, _WorldSpaceCameraPos)) / 40.0);
                worked *= far;
                pile *= far;
                rubble *= far;
                half3 nn = inputData.normalWS;
                UNITY_BRANCH if (worked > 0.01)
                {
                    float w;
                    half3 f = Facet(nn, p, _GroundFacet.x, _GroundFacet.y, w);
                    inputData.normalWS = normalize(lerp(inputData.normalWS, f, worked));
                    albedo *= 1.0 + _GroundFacet.z * worked * w;
                }
                UNITY_BRANCH if (pile > 0.01)
                {
                    float w;
                    half3 f = Facet(nn, p, _PileFacet.x, _PileFacet.y, w);
                    inputData.normalWS = normalize(lerp(inputData.normalWS, f, pile));
                    albedo *= 1.0 + _PileFacet.z * pile * w;
                }
                UNITY_BRANCH if (rubble > 0.01)
                {
                    float w;
                    half3 f = Facet(nn, p, _RubbleSize, _RubbleTilt, w);
                    inputData.normalWS = normalize(lerp(inputData.normalWS, f, rubble));
                    albedo *= 1.0 + 0.1 * rubble * w;
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
