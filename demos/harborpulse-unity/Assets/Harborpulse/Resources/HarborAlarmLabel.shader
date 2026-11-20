// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

Shader "Harborpulse/AlarmLabel"
{
    Properties
    {
        _MainTex ("Font atlas", 2D) = "white" {}
        _Color ("Colour", Color) = (1,1,1,1)
    }
    SubShader
    {
        Tags { "RenderPipeline"="UniversalPipeline" "Queue"="Overlay" "RenderType"="Transparent" }
        Pass
        {
            // Evidence labels draw above emissive beacons; the opaque plate draws first.
            ZTest Always
            ZWrite Off
            Cull Off
            Blend SrcAlpha OneMinusSrcAlpha
            HLSLPROGRAM
            #pragma vertex Vert
            #pragma fragment Frag
            #include "Packages/com.unity.render-pipelines.universal/ShaderLibrary/Core.hlsl"
            TEXTURE2D(_MainTex); SAMPLER(sampler_MainTex);
            CBUFFER_START(UnityPerMaterial)
                half4 _Color;
            CBUFFER_END
            struct Attributes { float4 positionOS : POSITION; float2 uv : TEXCOORD0; };
            struct Varyings { float4 positionCS : SV_POSITION; float2 uv : TEXCOORD0; half4 color : COLOR; };
            Varyings Vert(Attributes i)
            {
                Varyings o;
                o.positionCS = TransformObjectToHClip(i.positionOS.xyz);
                o.uv = i.uv; o.color = _Color;
                return o;
            }
            half4 Frag(Varyings i) : SV_Target
            {
                return half4(i.color.rgb, i.color.a * SAMPLE_TEXTURE2D(_MainTex, sampler_MainTex, i.uv).a);
            }
            ENDHLSL
        }
    }
}
