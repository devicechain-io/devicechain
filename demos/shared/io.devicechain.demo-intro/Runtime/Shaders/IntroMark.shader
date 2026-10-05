// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// The mark's surface: the face colour comes from the mesh's vertex colours (the brand colours,
// linear). While the mark moves it is shaded by the intro's own neutral key light, a fresnel rim
// in the brand's lightest blue and a light sweep, independent of the scene's lights, and its
// edges are chamfered so the light catches them. _Flat crossfades the shading to the vertex
// colour alone, which is exactly the brand colour on screen, and the chamfer goes with it: the
// mesh is built in the drawing's shape and holds the chamfer as a per-vertex offset (UV sets 1
// and 2) that _Chamfer scales, so at 0 every strip of it has no area.
Shader "DeviceChain/Intro/Mark"
{
    Properties
    {
        _Flat ("Flat (0 lit, 1 brand colours)", Range(0, 1)) = 1
        _Light ("Light", Range(0, 1)) = 1
        _Dark ("Colour unlit (the background behind the mark)", Color) = (0, 0, 0, 1)
        _Alpha ("Alpha", Range(0, 1)) = 1
        _Chamfer ("Chamfer", Range(0, 1)) = 0
        _KeyDir ("Key direction (to the light)", Vector) = (-0.5, 0.5, -0.7071, 0)
        _KeyColor ("Key colour", Color) = (1.1, 1.1, 1.1, 1)
        _Ambient ("Ambient", Color) = (0.33, 0.35, 0.38, 1)
        _RimColor ("Rim colour (#9ACEEC)", Color) = (0.6039, 0.8078, 0.9255, 1)
        _Rim ("Rim", Range(0, 2)) = 0.35
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
            // premultiplied output: over the opaque background the target stays opaque; over the
            // exit's opening (where the target is clear) a fading cube is translucent
            Blend SrcAlpha OneMinusSrcAlpha, One OneMinusSrcAlpha
            ZWrite On
            Cull Back

            HLSLPROGRAM
            #pragma vertex vert
            #pragma fragment frag
            #include "Packages/com.unity.render-pipelines.universal/ShaderLibrary/Core.hlsl"

            CBUFFER_START(UnityPerMaterial)
                float _Flat, _Light, _Alpha, _Chamfer, _Rim, _SweepPos, _SweepWidth;
                float4 _Dark, _KeyDir, _KeyColor, _Ambient, _RimColor, _SweepAxis, _SweepColor, _Origin;
            CBUFFER_END

            struct Attributes
            {
                float4 positionOS : POSITION;
                float4 color : COLOR;
                float2 chamferXY : TEXCOORD1;
                float2 chamferZ : TEXCOORD2;
            };
            struct Varyings
            {
                float4 positionCS : SV_POSITION;
                float3 positionWS : TEXCOORD0;
                float3 positionOS : TEXCOORD1;
                float4 color : COLOR;
            };

            Varyings vert(Attributes v)
            {
                Varyings o;
                float3 pos = v.positionOS.xyz + float3(v.chamferXY, v.chamferZ.x) * _Chamfer;
                VertexPositionInputs p = GetVertexPositionInputs(pos);
                o.positionCS = p.positionCS;
                o.positionWS = p.positionWS;
                o.positionOS = pos;
                o.color = v.color;
                return o;
            }

            half4 frag(Varyings i) : SV_Target
            {
                float3 albedo = i.color.rgb;
                float3 viewDir = GetWorldSpaceNormalizeViewDir(i.positionWS);
                // every face is flat, so its normal is the surface's own; the chamfer's strips
                // have none in the mesh (they have no area in the drawing's shape). It is taken
                // in object space: the rig sits far from the origin, where world positions are
                // too coarse to differentiate across a pixel
                float3 n = TransformObjectToWorldNormal(cross(ddy(i.positionOS), ddx(i.positionOS)));
                n = dot(n, viewDir) < 0 ? -n : n;
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
                // before the light reaches it the mark is the background's colour, so it emerges
                // from the dark rather than starting as a black shape
                lit = lerp(_Dark.rgb, lit, _Light);
                float3 col = lerp(lit, albedo, _Flat);
                return half4(col, _Alpha);
            }
            ENDHLSL
        }
    }
}
