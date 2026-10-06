// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// The machines' material: lit, opaque, with the glTF material's own inputs (the property names the
// glTF importer gives them, so a machine's materials keep their values when they are switched to
// this shader, and the tread scroll and the beacons drive the same names), and site dust on top.
// Dust settles low on a machine: the paint fades towards the dust colour over the lowest metre or
// so above the ground the machine stands on (_DustGround, set per machine as it moves) and a light
// film covers the rest, so the yellow reads as worked paint rather than a toy's. Dust also takes
// the sheen off. The shadow, depth and depth-normal passes are URP Lit's own.
Shader "Sitepulse/Machine Lit"
{
    Properties
    {
        baseColorFactor("Base colour", Color) = (1, 1, 1, 1)
        baseColorTexture("Base colour map", 2D) = "white" {}
        metallicFactor("Metallic", Range(0, 1)) = 0
        roughnessFactor("Roughness", Range(0, 1)) = 0.5
        [HDR] emissiveFactor("Emission", Color) = (0, 0, 0, 1)
        _DustColor("Dust colour", Color) = (0.62, 0.56, 0.47, 1)
        _DustHeight("Dust height (m)", Float) = 1.3
        _DustAmount("Dust at the ground", Range(0, 1)) = 0.6
        _DustBase("Dust film everywhere", Range(0, 0.3)) = 0.07
        _DustGround("Ground height (world y)", Float) = 0
    }

    SubShader
    {
        Tags { "RenderType" = "Opaque" "RenderPipeline" = "UniversalPipeline" "UniversalMaterialType" = "Lit" "Queue" = "Geometry" }

        Pass
        {
            Name "ForwardLit"
            Tags { "LightMode" = "UniversalForward" }
            Cull Off
            HLSLPROGRAM
            #pragma target 3.5
            #pragma vertex Vert
            #pragma fragment Frag

            #pragma multi_compile _ _MAIN_LIGHT_SHADOWS _MAIN_LIGHT_SHADOWS_CASCADE _MAIN_LIGHT_SHADOWS_SCREEN
            #pragma multi_compile _ _ADDITIONAL_LIGHTS_VERTEX _ADDITIONAL_LIGHTS
            #pragma multi_compile _ _CLUSTER_LIGHT_LOOP
            #pragma multi_compile _ EVALUATE_SH_MIXED EVALUATE_SH_VERTEX
            #pragma multi_compile_fragment _ _SHADOWS_SOFT _SHADOWS_SOFT_LOW _SHADOWS_SOFT_MEDIUM _SHADOWS_SOFT_HIGH
            #pragma multi_compile_fragment _ _SCREEN_SPACE_OCCLUSION
            #pragma multi_compile _ LOD_FADE_CROSSFADE
            #include_with_pragmas "Packages/com.unity.render-pipelines.universal/ShaderLibrary/Fog.hlsl"
            #pragma multi_compile_instancing

            #include "Packages/com.unity.render-pipelines.universal/ShaderLibrary/Lighting.hlsl"
            #include "Packages/com.unity.render-pipelines.universal/ShaderLibrary/LODCrossFade.hlsl"

            TEXTURE2D(baseColorTexture); SAMPLER(samplerbaseColorTexture);
            CBUFFER_START(UnityPerMaterial)
                float4 baseColorTexture_ST;
                half4 baseColorFactor;
                half metallicFactor;
                half roughnessFactor;
                half4 emissiveFactor;
                half4 _DustColor;
                float _DustHeight, _DustAmount, _DustBase, _DustGround;
            CBUFFER_END

            struct Attributes
            {
                float4 positionOS : POSITION;
                float3 normalOS : NORMAL;
                float2 uv : TEXCOORD0;
                UNITY_VERTEX_INPUT_INSTANCE_ID
            };

            struct Varyings
            {
                float4 positionCS : SV_POSITION;
                float3 positionWS : TEXCOORD0;
                half3 normalWS : TEXCOORD1;
                float2 uv : TEXCOORD2;
                half fogFactor : TEXCOORD3;
                UNITY_VERTEX_INPUT_INSTANCE_ID
                UNITY_VERTEX_OUTPUT_STEREO
            };

            Varyings Vert(Attributes v)
            {
                Varyings o = (Varyings)0;
                UNITY_SETUP_INSTANCE_ID(v);
                UNITY_TRANSFER_INSTANCE_ID(v, o);
                UNITY_INITIALIZE_VERTEX_OUTPUT_STEREO(o);
                VertexPositionInputs p = GetVertexPositionInputs(v.positionOS.xyz);
                o.positionCS = p.positionCS;
                o.positionWS = p.positionWS;
                o.normalWS = TransformObjectToWorldNormal(v.normalOS);
                o.uv = v.uv * baseColorTexture_ST.xy + baseColorTexture_ST.zw;
                o.fogFactor = ComputeFogFactor(p.positionCS.z);
                return o;
            }

            half4 Frag(Varyings i, bool front : SV_IsFrontFace) : SV_Target
            {
                UNITY_SETUP_INSTANCE_ID(i);
                UNITY_SETUP_STEREO_EYE_INDEX_POST_VERTEX(i);
            #ifdef LOD_FADE_CROSSFADE
                LODFadeCrossFade(i.positionCS);
            #endif
                InputData d = (InputData)0;
                d.positionWS = i.positionWS;
                d.positionCS = i.positionCS;
                half3 n = NormalizeNormalPerPixel(i.normalWS);
                d.normalWS = front ? n : -n;
                d.viewDirectionWS = GetWorldSpaceNormalizeViewDir(i.positionWS);
                d.shadowCoord = TransformWorldToShadowCoord(i.positionWS);
                d.fogCoord = InitializeInputDataFog(float4(i.positionWS, 1.0), i.fogFactor);
                d.bakedGI = SampleSH(d.normalWS);
                d.normalizedScreenSpaceUV = GetNormalizedScreenSpaceUV(i.positionCS);
                d.shadowMask = half4(1, 1, 1, 1);

                half3 albedo = baseColorFactor.rgb * SAMPLE_TEXTURE2D(baseColorTexture, samplerbaseColorTexture, i.uv).rgb;
                // dust: heavy at the ground, thinning out up the machine, a light film above; more
                // on faces that look up, where it settles
                float h = i.positionWS.y - _DustGround;
                half low = saturate(1.0 - h / _DustHeight);
                half dust = max(_DustAmount * low * low * (3.0 - 2.0 * low), _DustBase * (0.6 + 0.4 * saturate(d.normalWS.y)));
                albedo = lerp(albedo, _DustColor.rgb, dust);
                half smoothness = (1.0h - roughnessFactor) * (1.0h - dust);
                half metallic = metallicFactor * (1.0h - dust);
                half4 c = UniversalFragmentPBR(d, albedo, metallic, half3(0, 0, 0), smoothness, 1.0h, emissiveFactor.rgb, 1.0h);
                c.rgb = MixFog(c.rgb, d.fogCoord);
                return half4(c.rgb, 1.0h);
            }
            ENDHLSL
        }

        UsePass "Universal Render Pipeline/Lit/SHADOWCASTER"
        UsePass "Universal Render Pipeline/Lit/DEPTHONLY"
        UsePass "Universal Render Pipeline/Lit/DEPTHNORMALS"
    }
}
