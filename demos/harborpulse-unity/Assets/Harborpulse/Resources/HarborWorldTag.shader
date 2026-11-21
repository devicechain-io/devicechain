// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// A text tag that belongs to a place in the world and is hidden by whatever stands in front of it. The built-in font material draws
// over everything, which is right for an evidence label and wrong for the wet well's level tags: they sit below the deck inside the
// cut-away, and from a camera beside the well they were drawn through the quay. This one tests depth and writes none.
Shader "Harborpulse/WorldTag"
{
    Properties
    {
        _MainTex ("Font atlas", 2D) = "white" {}
    }
    SubShader
    {
        Tags { "RenderPipeline"="UniversalPipeline" "Queue"="Transparent" "RenderType"="Transparent" }
        Pass
        {
            ZTest LEqual
            ZWrite Off
            Cull Off
            Blend SrcAlpha OneMinusSrcAlpha
            HLSLPROGRAM
            #pragma vertex Vert
            #pragma fragment Frag
            #include "Packages/com.unity.render-pipelines.universal/ShaderLibrary/Core.hlsl"
            TEXTURE2D(_MainTex); SAMPLER(sampler_MainTex);
            struct Attributes { float4 positionOS : POSITION; float2 uv : TEXCOORD0; half4 color : COLOR; };
            struct Varyings { float4 positionCS : SV_POSITION; float2 uv : TEXCOORD0; half4 color : COLOR; };
            Varyings Vert(Attributes i)
            {
                Varyings o;
                o.positionCS = TransformObjectToHClip(i.positionOS.xyz);
                o.uv = i.uv; o.color = i.color;
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
