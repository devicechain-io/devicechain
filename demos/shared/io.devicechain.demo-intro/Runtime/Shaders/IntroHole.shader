// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// The exit's opening: where it is drawn, the intro's target is cleared toward transparent (by
// _Open, 0..1), colour and alpha alike, so the overlay shows the demo there. It is drawn after
// the backdrop and before the mark, so the frame and the cube still draw over it.
Shader "DeviceChain/Intro/Hole"
{
    Properties
    {
        _Open ("Open", Range(0, 1)) = 1
    }
    SubShader
    {
        Tags { "RenderPipeline" = "UniversalPipeline" "RenderType" = "Transparent" "Queue" = "Transparent-40" }
        Pass
        {
            Name "IntroHole"
            Tags { "LightMode" = "UniversalForward" }
            Blend Zero OneMinusSrcAlpha
            ZWrite Off
            ZTest Always
            Cull Off

            HLSLPROGRAM
            #pragma vertex vert
            #pragma fragment frag
            #include "Packages/com.unity.render-pipelines.universal/ShaderLibrary/Core.hlsl"

            CBUFFER_START(UnityPerMaterial)
                float _Open;
            CBUFFER_END

            float4 vert(float4 positionOS : POSITION) : SV_POSITION
            {
                return TransformObjectToHClip(positionOS.xyz);
            }

            half4 frag() : SV_Target
            {
                return half4(0, 0, 0, _Open);
            }
            ENDHLSL
        }
    }
}
