// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// Additive light for the intro: the rim light tracing the frame (a line, drawn up to _Trace of
// its length with a brighter head), the soft glow on the edges, the lock pulse and the
// background's radial glow. It adds colour and leaves the target's alpha alone. _Mode 0 = line (uv.x along the line, uv.y across it, as a
// LineRenderer stretches them), 1 = radial (uv 0..1 over a quad).
Shader "DeviceChain/Intro/Glow"
{
    Properties
    {
        [HDR] _Color ("Colour", Color) = (0.6, 0.81, 0.93, 1)
        _Intensity ("Intensity", Float) = 1
        _Softness ("Softness (higher = tighter)", Float) = 4
        _Trace ("Trace (fraction of the line drawn)", Float) = 1
        _Head ("Head brightness", Float) = 0
        [Enum(Line, 0, Radial, 1)] _Mode ("Mode", Float) = 0
        [Enum(UnityEngine.Rendering.CompareFunction)] _ZTest ("ZTest", Float) = 4
    }
    SubShader
    {
        Tags { "RenderPipeline" = "UniversalPipeline" "RenderType" = "Transparent" "Queue" = "Transparent+100" }
        Pass
        {
            Name "IntroGlow"
            Tags { "LightMode" = "UniversalForward" }
            Blend One One
            ZWrite Off
            ZTest [_ZTest]
            Cull Off

            HLSLPROGRAM
            #pragma vertex vert
            #pragma fragment frag
            #include "Packages/com.unity.render-pipelines.universal/ShaderLibrary/Core.hlsl"

            CBUFFER_START(UnityPerMaterial)
                float4 _Color;
                float _Intensity, _Softness, _Trace, _Head, _Mode, _ZTest;
            CBUFFER_END

            struct Attributes { float4 positionOS : POSITION; float2 uv : TEXCOORD0; float4 color : COLOR; };
            struct Varyings { float4 positionCS : SV_POSITION; float2 uv : TEXCOORD0; float4 color : COLOR; };

            Varyings vert(Attributes v)
            {
                Varyings o;
                o.positionCS = TransformObjectToHClip(v.positionOS.xyz);
                o.uv = v.uv;
                o.color = v.color;
                return o;
            }

            half4 frag(Varyings i) : SV_Target
            {
                float a;
                if (_Mode < 0.5)
                {
                    float d = abs(i.uv.y - 0.5) * 2.0;
                    a = exp(-d * d * _Softness);
                    float behind = _Trace - i.uv.x;           // how far behind the head this point is
                    a *= step(0.0, behind);
                    a *= 1.0 + _Head * exp(-max(behind, 0.0) * 40.0) * step(_Trace, 0.9999);
                }
                else
                {
                    float d = length(i.uv - 0.5) * 2.0;
                    a = exp(-d * d * _Softness) * (1.0 - smoothstep(0.85, 1.0, d));
                }
                float3 c = _Color.rgb * i.color.rgb * (i.color.a * a * _Intensity);
                if (_Mode >= 0.5)
                {
                    // a wide, dark gradient bands in an 8-bit target: dither it by about one
                    // step of the encoded (gamma) value
                    float n = frac(52.9829189 * frac(dot(i.positionCS.xy, float2(0.06711056, 0.00583715)))) - 0.5;
                    c = pow(max(pow(max(c, 0.0), 1.0 / 2.2) + n / 255.0, 0.0), 2.2);
                }
                return half4(c, 0);
            }
            ENDHLSL
        }
    }
}
