// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// The mark's surface: the face colour comes from the mesh's vertex colours (the brand colours,
// linear). While the mark moves it is shaded by the intro's own key light, a fresnel rim and a
// light sweep, independent of the scene's lights; _Flat crossfades that to the vertex colour
// alone, which is exactly the brand colour on screen when the camera skips post-processing.
Shader "DeviceChain/Intro/Mark"
{
    Properties
    {
        _Flat ("Flat (0 lit, 1 brand colours)", Range(0, 1)) = 1
        _Light ("Light", Range(0, 1)) = 1
        _Alpha ("Alpha", Range(0, 1)) = 1
        _KeyDir ("Key direction (to the light)", Vector) = (-0.45, 0.65, -0.6, 0)
        _KeyColor ("Key colour", Color) = (1.15, 1.12, 1.08, 1)
        _Ambient ("Ambient", Color) = (0.30, 0.34, 0.40, 1)
        _RimColor ("Rim colour", Color) = (0.32, 0.62, 0.84, 1)
        _Rim ("Rim", Range(0, 2)) = 0.6
        _SweepAxis ("Sweep axis", Vector) = (0.6, 0.8, 0, 0)
        _SweepPos ("Sweep position", Float) = -10
        _SweepWidth ("Sweep width", Float) = 0.12
        _SweepColor ("Sweep colour", Color) = (0.75, 0.9, 1.0, 1)
        _Origin ("Rig origin (world)", Vector) = (0, 0, 0, 0)
    }
    SubShader
    {
        // drawn in the transparent range (still writing depth) so that renderer features that
        // act after the opaque pass, such as screen-space ambient occlusion composited after
        // opaques, never touch the mark's colours
        Tags { "RenderPipeline" = "UniversalPipeline" "RenderType" = "Transparent" "Queue" = "Transparent" }
        Pass
        {
            Name "IntroMark"
            Tags { "LightMode" = "UniversalForward" }
            // colour blends by alpha; the target's alpha is kept, so a fading wordmark never
            // lets the scene behind the intro show through
            Blend SrcAlpha OneMinusSrcAlpha, Zero One
            ZWrite On
            Cull Back

            HLSLPROGRAM
            #pragma vertex vert
            #pragma fragment frag
            #include "Packages/com.unity.render-pipelines.universal/ShaderLibrary/Core.hlsl"

            CBUFFER_START(UnityPerMaterial)
                float _Flat, _Light, _Alpha, _Rim, _SweepPos, _SweepWidth;
                float4 _KeyDir, _KeyColor, _Ambient, _RimColor, _SweepAxis, _SweepColor, _Origin;
            CBUFFER_END

            struct Attributes { float4 positionOS : POSITION; float3 normalOS : NORMAL; float4 color : COLOR; };
            struct Varyings
            {
                float4 positionCS : SV_POSITION;
                float3 positionWS : TEXCOORD0;
                float3 normalWS : TEXCOORD1;
                float4 color : COLOR;
            };

            Varyings vert(Attributes v)
            {
                Varyings o;
                VertexPositionInputs p = GetVertexPositionInputs(v.positionOS.xyz);
                o.positionCS = p.positionCS;
                o.positionWS = p.positionWS;
                o.normalWS = TransformObjectToWorldNormal(v.normalOS);
                o.color = v.color;
                return o;
            }

            half4 frag(Varyings i) : SV_Target
            {
                float3 albedo = i.color.rgb;
                float3 n = normalize(i.normalWS);
                float3 viewDir = GetWorldSpaceNormalizeViewDir(i.positionWS);
                float3 l = normalize(_KeyDir.xyz);
                float ndl = saturate(dot(n, l));
                float fres = pow(1.0 - saturate(dot(n, viewDir)), 3.0);
                float3 lit = albedo * (_Ambient.rgb + _KeyColor.rgb * ndl) + _RimColor.rgb * fres * _Rim;
                // a soft band of light travelling across the faces, strongest where they face it
                float s = dot(i.positionWS - _Origin.xyz, normalize(_SweepAxis.xyz));
                float band = exp(-pow((s - _SweepPos) / max(_SweepWidth, 1e-3), 2.0));
                float3 h = normalize(l + viewDir);
                float spec = pow(saturate(dot(n, h)), 12.0);
                lit += _SweepColor.rgb * band * (0.25 + 0.75 * spec) * (0.4 + 0.6 * albedo);
                lit *= _Light;
                float3 col = lerp(lit, albedo, _Flat);
                return half4(col, _Alpha);
            }
            ENDHLSL
        }
    }
}
