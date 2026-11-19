// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// The harbour water, first pass (U0): Gerstner waves over the gauges' observed tide, depth-based colour and
// refraction from the camera's depth and opaque textures, quay-wall and crest foam, a Fresnel blend to the
// sky and the sun's glint, and procedural rain rings during the storm. Planar reflection of the town is the
// next pass; here the reflection is the sky's own gradient.
//
// It draws after the opaques and composites the refracted scene itself (Blend One Zero), so it needs the
// URP asset's Depth Texture and Opaque Texture, which the project's PC and Laptop assets enable.
Shader "Harborpulse/Water"
{
    Properties
    {
        _ShallowColor ("Shallow tint", Color) = (0.16, 0.48, 0.50, 1)
        _DeepColor ("Deep colour", Color) = (0.015, 0.075, 0.11, 1)
        _FoamColor ("Foam", Color) = (0.90, 0.94, 0.95, 1)
        _DepthFade ("Depth for full colour (m)", Float) = 7
        _FoamDistance ("Wall foam width (m)", Float) = 1.4
        _Displace ("Vertex waves (1 near grid, 0 far plane)", Float) = 1
        _WaveFadeStart ("Waves fade from (m)", Float) = 250
        _WaveFadeEnd ("Waves gone at (m)", Float) = 680
    }

    SubShader
    {
        Tags { "RenderType" = "Transparent" "Queue" = "Transparent-50" "RenderPipeline" = "UniversalPipeline" "IgnoreProjector" = "True" }

        Pass
        {
            Name "HarborWater"
            Tags { "LightMode" = "UniversalForward" }
            Blend One Zero
            ZWrite On
            Cull Off

            HLSLPROGRAM
            #pragma target 3.5
            #pragma vertex Vert
            #pragma fragment Frag
            #pragma multi_compile_fog
            #pragma multi_compile _ _MAIN_LIGHT_SHADOWS _MAIN_LIGHT_SHADOWS_CASCADE _MAIN_LIGHT_SHADOWS_SCREEN
            #pragma multi_compile_fragment _ _SHADOWS_SOFT

            #include "Packages/com.unity.render-pipelines.universal/ShaderLibrary/Core.hlsl"
            #include "Packages/com.unity.render-pipelines.universal/ShaderLibrary/Lighting.hlsl"
            #include "Packages/com.unity.render-pipelines.universal/ShaderLibrary/DeclareDepthTexture.hlsl"
            #include "Packages/com.unity.render-pipelines.universal/ShaderLibrary/DeclareOpaqueTexture.hlsl"
            #include "HarborWaves.hlsl"

            CBUFFER_START(UnityPerMaterial)
                float4 _ShallowColor;
                float4 _DeepColor;
                float4 _FoamColor;
                float _DepthFade;
                float _FoamDistance;
                float _Displace;
                float _WaveFadeStart;
                float _WaveFadeEnd;
            CBUFFER_END

            // the sky the water reflects, and the weather, from HarborSkyController / HarborWaterController
            float4 _HarborSkyZenith;
            float4 _HarborSkyHorizon;
            float _HarborStorm;
            float _HarborRain;
            float _HarborNight;

            struct Attributes
            {
                float4 positionOS : POSITION;
            };

            struct Varyings
            {
                float4 positionCS : SV_POSITION;
                float3 positionWS : TEXCOORD0;
                float2 restXZ : TEXCOORD1;
                float waveScale : TEXCOORD2;
                float crest : TEXCOORD3;
                float fogCoord : TEXCOORD4;
                // the surface's distance along the view axis, for the depth difference to what is under it
                float eyeDepth : TEXCOORD5;
            };

            float WaveScaleAt(float3 ws)
            {
                float dist = distance(ws.xz, _WorldSpaceCameraPos.xz);
                return 1.0 - smoothstep(_WaveFadeStart, _WaveFadeEnd, dist);
            }

            Varyings Vert(Attributes v)
            {
                Varyings o;
                float3 ws = TransformObjectToWorld(v.positionOS.xyz);
                float2 rest = ws.xz;
                float scale = WaveScaleAt(ws);
                float3 offset, n;
                HarborWaves(rest, scale * _Displace, offset, n);
                ws = float3(rest.x, HarborTide(rest.x), rest.y) + offset;
                o.positionWS = ws;
                o.restXZ = rest;
                o.waveScale = scale;
                o.crest = offset.y / max(_WaveAmp.x * 1.8, 1e-3);
                o.positionCS = TransformWorldToHClip(ws);
                o.eyeDepth = -TransformWorldToView(ws).z;
                o.fogCoord = ComputeFogFactor(o.positionCS.z);
                return o;
            }

            float Hash21(float2 p)
            {
                p = frac(p * float2(123.34, 456.21));
                p += dot(p, p + 45.32);
                return frac(p.x * p.y);
            }

            float Noise(float2 p)
            {
                float2 i = floor(p);
                float2 f = frac(p);
                float2 u = f * f * (3.0 - 2.0 * f);
                return lerp(lerp(Hash21(i), Hash21(i + float2(1, 0)), u.x), lerp(Hash21(i + float2(0, 1)), Hash21(i + float2(1, 1)), u.x), u.y);
            }

            // small ripples that keep the surface alive between the big waves: the gradient of two drifting noise layers
            float2 DetailSlope(float2 xz, float t)
            {
                float e = 0.15;
                float2 p1 = xz * 0.55 + float2(t * 0.35, t * 0.21);
                float2 p2 = xz * 1.30 + float2(-t * 0.27, t * 0.41);
                float h0 = Noise(p1) + 0.5 * Noise(p2);
                float hx = Noise(p1 + float2(e, 0)) + 0.5 * Noise(p2 + float2(e, 0));
                float hz = Noise(p1 + float2(0, e)) + 0.5 * Noise(p2 + float2(0, e));
                return float2(hx - h0, hz - h0) / e;
            }

            // rain: expanding rings, one per cell, each cell's drop at its own random time
            float2 RainSlope(float2 xz, float t)
            {
                float2 slope = 0;
                [unroll]
                for (int layer = 0; layer < 2; layer++)
                {
                    float2 p = xz * (layer == 0 ? 1.6 : 2.3) + layer * 17.0;
                    float2 cell = floor(p);
                    float2 f = frac(p) - 0.5;
                    float h = Hash21(cell);
                    float2 centre = (float2(Hash21(cell + 3.1), Hash21(cell + 7.7)) - 0.5) * 0.6;
                    float age = frac(t * 0.9 + h);
                    float2 d = f - centre;
                    float r = length(d);
                    float ring = sin((r - age * 0.5) * 60.0) * saturate(1.0 - age) * smoothstep(0.5, 0.0, r) * step(r, age * 0.5 + 0.08);
                    slope += ring * d / max(r, 1e-3);
                }
                return slope * 0.6;
            }

            float3 SkyColour(float3 dir)
            {
                float up = saturate(dir.y);
                return lerp(_HarborSkyHorizon.rgb, _HarborSkyZenith.rgb, pow(up, 0.5));
            }

            half4 Frag(Varyings i, bool frontFace : SV_IsFrontFace) : SV_Target
            {
                float3 offset, n;
                HarborWaves(i.restXZ, i.waveScale, offset, n);
                // the fine ripples and the rain rings are smaller than a pixel a few hundred metres out: fade them before they sparkle
                float camDist = distance(i.positionWS, _WorldSpaceCameraPos);
                float2 detail = DetailSlope(i.restXZ, _WaterTime) * (0.05 + 0.10 * _HarborStorm) * saturate(1.0 - camDist / 700.0);
                float2 rain = RainSlope(i.restXZ, _WaterTime) * _HarborRain * saturate(1.0 - camDist / 160.0);
                n = normalize(n + float3(-detail.x - rain.x, 0, -detail.y - rain.y));
                if (!frontFace) n = -n;

                float3 v = normalize(GetWorldSpaceViewDir(i.positionWS));
                float ndv = saturate(dot(n, v));
                float fresnel = 0.02 + 0.98 * pow(1.0 - ndv, 5.0);

                float4 shadowCoord = TransformWorldToShadowCoord(i.positionWS);
                Light sun = GetMainLight(shadowCoord);
                float3 r = reflect(-v, n);
                r.y = abs(r.y);
                float3 reflection = SkyColour(r);
                float glint = pow(saturate(dot(r, sun.direction)), 900.0) * 18.0 + pow(saturate(dot(r, sun.direction)), 90.0) * 0.6;
                float3 specular = glint * sun.color * sun.shadowAttenuation * (1.0 - 0.85 * _HarborStorm);

                // refraction: what is under the surface, nudged by the slope; a nudge onto something in front of the water is undone
                float2 screenUV = GetNormalizedScreenSpaceUV(i.positionCS);
                float surfaceDepth = i.eyeDepth;
                float2 refractUV = screenUV + n.xz * 0.025;
                float sceneDepth = LinearEyeDepth(SampleSceneDepth(refractUV), _ZBufferParams);
                if (sceneDepth < surfaceDepth)
                {
                    refractUV = screenUV;
                    sceneDepth = LinearEyeDepth(SampleSceneDepth(screenUV), _ZBufferParams);
                }

                float waterDepth = max(0.0, sceneDepth - surfaceDepth);
                float3 under = SampleSceneColor(refractUV);
                float3 ambient = _HarborSkyZenith.rgb * 0.6 + _HarborSkyHorizon.rgb * 0.4;
                float lit = saturate(sun.direction.y) * (1.0 - 0.7 * _HarborStorm);
                float3 deep = _DeepColor.rgb * (ambient * 1.4 + sun.color * lit * 0.35);
                float absorb = 1.0 - exp(-waterDepth / max(_DepthFade, 0.01));
                float3 body = lerp(under * _ShallowColor.rgb * 1.6, deep, absorb);

                float3 colour = lerp(body, reflection, fresnel) + specular;

                // foam where the water meets a wall or the shore, broken up by noise, and on the storm's crests
                float edge = 1.0 - saturate(waterDepth / max(_FoamDistance * (1.0 + _HarborStorm), 0.01));
                float breakup = 0.6 * Noise(i.restXZ * 0.9 + _WaterTime * 0.3) + 0.4 * Noise(float2(-i.restXZ.y, i.restXZ.x) * 2.3 - _WaterTime * 0.2);
                float foam = saturate(edge * (0.45 + breakup));
                // storm crests: soft streaks where a crest is high, thinned by the noise rather than cut by it
                float crest = smoothstep(0.55, 1.15, i.crest + (breakup - 0.5) * 0.5);
                foam = max(foam, crest * _HarborStorm * 0.75);
                float3 foamLit = _FoamColor.rgb * (ambient * 1.2 + sun.color * lit * 0.8);
                colour = lerp(colour, foamLit, saturate(foam) * 0.85);

                colour = MixFog(colour, i.fogCoord);
                return half4(colour, 1.0);
            }
            ENDHLSL
        }
    }

    Fallback Off
}
