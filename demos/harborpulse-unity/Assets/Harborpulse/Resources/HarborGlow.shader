// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// The glow round a lit lamp: a camera-facing disc, brightest at the centre and falling off smoothly to nothing at its rim, added
// to what is behind it. Its four vertices all sit at the lamp (the corner is in the UV), so it faces whichever camera draws it, the
// water's reflection camera included. It never shrinks below a small angle of view: the lights stand hundreds of metres from HOME,
// and without that a lit head is a pixel from the overview.
Shader "Harborpulse/Glow"
{
    Properties
    {
        _Color ("Colour", Color) = (1, 0.8, 0.55, 1)
        _Radius ("Radius (m)", Float) = 1.7
        _MinAngle ("Least angular radius (rad)", Float) = 0.004
        _Power ("Power", Float) = 1
    }
    SubShader
    {
        Tags { "RenderPipeline"="UniversalPipeline" "Queue"="Transparent" "RenderType"="Transparent" "IgnoreProjector"="True" }
        Pass
        {
            Name "Glow"
            Blend One One
            ZWrite Off
            Cull Off
            HLSLPROGRAM
            #pragma vertex Vert
            #pragma fragment Frag
            #include "Packages/com.unity.render-pipelines.universal/ShaderLibrary/Core.hlsl"
            CBUFFER_START(UnityPerMaterial)
                half4 _Color;
                float _Radius;
                float _MinAngle;
                float _Power;
            CBUFFER_END
            struct Attributes { float4 positionOS : POSITION; float2 uv : TEXCOORD0; };
            struct Varyings { float4 positionCS : SV_POSITION; float2 uv : TEXCOORD0; half strength : TEXCOORD1; };
            Varyings Vert(Attributes i)
            {
                Varyings o;
                float3 centre = TransformObjectToWorld(i.positionOS.xyz);
                float away = distance(_WorldSpaceCameraPos, centre);
                float radius = max(_Radius, away * _MinAngle);
                // the view matrix's rows are the camera's right and up in world space
                float3 right = UNITY_MATRIX_V[0].xyz;
                float3 up = UNITY_MATRIX_V[1].xyz;
                o.positionCS = TransformWorldToHClip(centre + (right * i.uv.x + up * i.uv.y) * radius);
                o.uv = i.uv;
                // a disc held at its least angle is larger than the lamp's own glow: spread the same light thinner, down to a floor
                o.strength = _Power * max(0.45, _Radius / radius);
                return o;
            }
            half4 Frag(Varyings i) : SV_Target
            {
                float r2 = dot(i.uv, i.uv);
                float rim = saturate(1.0 - sqrt(r2));
                // a bright core inside a wide soft skirt; both reach zero at the rim, so the disc has no edge
                float glow = exp(-r2 * 9.0) * 0.85 + rim * rim * 0.22;
                glow *= smoothstep(0.0, 0.12, rim);
                return half4(_Color.rgb * (glow * i.strength), 1);
            }
            ENDHLSL
        }
    }
    Fallback Off
}
