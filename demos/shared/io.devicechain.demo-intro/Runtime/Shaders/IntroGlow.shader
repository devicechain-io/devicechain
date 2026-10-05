// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// Additive light for the intro, written in high dynamic range so the brightest of it blooms: the
// rim light tracing the frame (a line, drawn up to _Trace of its length with a brighter head),
// the light on the edges, the lock pulse, the data packets and the background's radial glow.
// _Mode 0 = line (uv.x along the line, uv.y across it, as a LineRenderer stretches them),
// 1 = radial (uv 0..1 over a quad), 2 = streak (uv.x from the tail, 0, to the head, 1).
Shader "DeviceChain/Intro/Glow"
{
    Properties
    {
        [HDR] _Color ("Colour", Color) = (0.6, 0.81, 0.93, 1)
        _Intensity ("Intensity", Float) = 1
        _Softness ("Softness (higher = tighter)", Float) = 4
        _Trace ("Trace (fraction of the line drawn)", Float) = 1
        _Head ("Head brightness", Float) = 0
        [Enum(Line, 0, Radial, 1, Streak, 2)] _Mode ("Mode", Float) = 0
        [Enum(UnityEngine.Rendering.CompareFunction)] _ZTest ("ZTest", Float) = 4
        [Enum(UnityEngine.Rendering.BlendMode)] _DstBlend ("Destination blend", Float) = 1
        _Base ("Base colour (radial mode, drawn opaque)", Color) = (0, 0, 0, 0)
    }
    SubShader
    {
        Tags { "RenderPipeline" = "UniversalPipeline" "RenderType" = "Transparent" "Queue" = "Transparent+100" }
        Pass
        {
            Name "IntroGlow"
            Tags { "LightMode" = "UniversalForward" }
            // light adds colour and leaves the target's alpha alone (premultiplied); the backdrop
            // (destination blend Zero) replaces the colour, base and glow together
            Blend One [_DstBlend], Zero One
            ZWrite Off
            ZTest [_ZTest]
            Cull Off

            HLSLPROGRAM
            #pragma vertex vert
            #pragma fragment frag
            #include "Packages/com.unity.render-pipelines.universal/ShaderLibrary/Core.hlsl"

            CBUFFER_START(UnityPerMaterial)
                float4 _Color, _Base;
                float _Intensity, _Softness, _Trace, _Head, _Mode, _ZTest, _DstBlend;
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
                else if (_Mode < 1.5)
                {
                    float d = length(i.uv - 0.5) * 2.0;
                    a = exp(-d * d * _Softness) * (1.0 - smoothstep(0.85, 1.0, d));
                }
                else
                {
                    // a moving point blurred along its path: brightest at the head
                    float d = abs(i.uv.y - 0.5) * 2.0;
                    a = exp(-d * d * _Softness) * i.uv.x * i.uv.x * smoothstep(1.0, 0.92, i.uv.x);
                }
                float3 c = _Color.rgb * i.color.rgb * (i.color.a * a * _Intensity);
                if (_Mode > 0.5 && _Mode < 1.5)
                {
                    // a wide, dark gradient bands in an 8-bit target (and again in a video
                    // encode): dither the final colour (base and glow) by +-1 step of its encoded
                    // (gamma) value, with interleaved gradient noise, which has little
                    // low-frequency energy
                    c += _Base.rgb;
                    float n = frac(52.9829189 * frac(dot(i.positionCS.xy, float2(0.06711056, 0.00583715)))) * 2.0 - 1.0;
                    c = pow(max(pow(max(c, 0.0), 1.0 / 2.2) + n / 255.0, 0.0), 2.2);
                }
                return half4(c, 0);
            }
            ENDHLSL
        }
    }
}
