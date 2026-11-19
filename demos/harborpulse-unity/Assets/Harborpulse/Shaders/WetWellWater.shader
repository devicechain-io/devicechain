// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

Shader "Harborpulse/WetWellWater"
{
    Properties { _Streak ("Flow streak mapping", Float) = 0 }
    SubShader
    {
        Tags { "RenderType"="Transparent" "Queue"="Transparent-40" "RenderPipeline"="UniversalPipeline" }
        Pass
        {
            Tags { "LightMode"="UniversalForward" }
            Blend SrcAlpha OneMinusSrcAlpha
            ZWrite Off
            Cull Off
            HLSLPROGRAM
            #pragma vertex Vert
            #pragma fragment Frag
            #include "Packages/com.unity.render-pipelines.universal/ShaderLibrary/Core.hlsl"
            #include "Packages/com.unity.render-pipelines.universal/ShaderLibrary/Lighting.hlsl"
            #include "HarborWaves.hlsl"
            float4 _HarborSkyZenith;
            CBUFFER_START(UnityPerMaterial)
                float _Streak;
            CBUFFER_END
            struct Attributes { float4 positionOS : POSITION; };
            struct Varyings { float4 positionCS : SV_POSITION; float3 positionWS : TEXCOORD0; };
            Varyings Vert(Attributes i)
            {
                Varyings o; float3 p = TransformObjectToWorld(i.positionOS.xyz);
                p.y += (1 - _Streak) * sin(p.x * 12 + _WaterTime * 1.7) * cos(p.z * 9 - _WaterTime) * 0.007;
                o.positionWS = p; o.positionCS = TransformWorldToHClip(p); return o;
            }
            half4 Frag(Varyings i) : SV_Target
            {
                if (_Streak > 0.5)
                {
                    float streak = pow(saturate(sin(i.positionWS.z * 11 - _WaterTime * 22 + i.positionWS.x * 7)), 5);
                    return half4(float3(0.06, 0.4, 0.9) * (1 + streak * 2), 0.25 + streak * 0.7);
                }
                float3 n = normalize(float3(cos(i.positionWS.x * 12 + _WaterTime) * 0.035, 1, sin(i.positionWS.z * 9 - _WaterTime) * 0.035));
                Light light = GetMainLight();
                float3 colour = HarborWaterLight(float3(0.07, 0.35, 0.42), n, normalize(GetWorldSpaceViewDir(i.positionWS)), light.direction, light.color, _HarborSkyZenith.rgb + 0.28);
                return half4(colour, 0.63);
            }
            ENDHLSL
        }
    }
    Fallback Off
}
