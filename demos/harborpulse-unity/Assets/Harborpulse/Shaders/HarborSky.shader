// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// The procedural sky: a zenith-to-horizon gradient graded by the time of day, the sun and moon discs where
// HarborSkyController puts their lights, a star field at night, and cloud that thickens to a full overcast
// through the storm. Every input is a global the controller sets from DayLighting each frame.
Shader "Harborpulse/Sky"
{
    SubShader
    {
        Tags { "Queue" = "Background" "RenderType" = "Background" "PreviewType" = "Skybox" "RenderPipeline" = "UniversalPipeline" }
        Cull Off
        ZWrite Off

        Pass
        {
            HLSLPROGRAM
            #pragma vertex Vert
            #pragma fragment Frag
            #include "Packages/com.unity.render-pipelines.universal/ShaderLibrary/Core.hlsl"

            float4 _HarborSkyZenith;
            float4 _HarborSkyHorizon;
            float4 _HarborSkyGround;
            float4 _HarborSunDir;    // towards the sun, w = disc visibility
            float4 _HarborSunColor;
            float4 _HarborMoonDir;   // towards the moon, w = disc visibility
            float4 _HarborMoonColor;
            float _HarborStars;
            float _HarborOvercast;
            float _HarborSkyTime;

            struct Attributes { float4 positionOS : POSITION; };
            struct Varyings { float4 positionCS : SV_POSITION; float3 dir : TEXCOORD0; };

            Varyings Vert(Attributes v)
            {
                Varyings o;
                o.positionCS = TransformObjectToHClip(v.positionOS.xyz);
                o.dir = v.positionOS.xyz;
                return o;
            }

            float Hash31(float3 p)
            {
                p = frac(p * 0.3183099 + 0.1);
                p *= 17.0;
                return frac(p.x * p.y * p.z * (p.x + p.y + p.z));
            }

            float Hash21(float2 p)
            {
                p = frac(p * float2(123.34, 456.21));
                p += dot(p, p + 45.32);
                return frac(p.x * p.y);
            }

            float Noise(float2 p)
            {
                float2 i = floor(p);
                float2 f = frac(p);
                float2 u = f * f * (3.0 - 2.0 * f);
                return lerp(lerp(Hash21(i), Hash21(i + float2(1, 0)), u.x), lerp(Hash21(i + float2(0, 1)), Hash21(i + float2(1, 1)), u.x), u.y);
            }

            float Fbm(float2 p)
            {
                float s = 0, a = 0.5;
                [unroll] for (int k = 0; k < 5; k++) { s += a * Noise(p); p = p * 2.03 + 11.7; a *= 0.5; }
                return s;
            }

            half4 Frag(Varyings i) : SV_Target
            {
                float3 d = normalize(i.dir);
                float h = d.y;
                float3 sky = lerp(_HarborSkyHorizon.rgb, _HarborSkyZenith.rgb, pow(saturate(h), 0.45));
                sky = lerp(sky, _HarborSkyGround.rgb, saturate(-h * 6.0));

                // the sun's glow warms the horizon round it
                float sd = dot(d, _HarborSunDir.xyz);
                sky += _HarborSunColor.rgb * pow(saturate(sd), 6.0) * 0.35 * (1.0 - 0.8 * _HarborOvercast) * saturate(_HarborSunDir.y * 4.0 + 0.6);

                // stars, fixed to the sky, fading in towards the zenith
                float3 sp = d * 220.0;
                float3 cell = floor(sp);
                float star = step(0.9965, Hash31(cell));
                float3 f = frac(sp) - 0.5;
                star *= smoothstep(0.25, 0.0, length(f)) * (0.6 + 0.4 * sin(_HarborSkyTime * 2.0 + Hash31(cell + 5.0) * 30.0));
                sky += star * _HarborStars * saturate(h * 5.0) * 1.6;

                // cloud: a few fair-weather patches, thickening to a full overcast through the storm
                float2 uv = d.xz / max(h + 0.12, 0.05) * 0.9 + _HarborSkyTime * float2(0.010, 0.004) * (1.0 + 3.0 * _HarborOvercast);
                float cloud = Fbm(uv);
                float cover = lerp(0.62, 0.15, _HarborOvercast);
                float density = smoothstep(cover, cover + 0.25, cloud) * saturate(h * 8.0 + 0.2);
                density = max(density, _HarborOvercast * 0.85 * saturate(h * 3.0 + 0.6));
                float3 cloudCol = lerp(_HarborSkyHorizon.rgb * 1.05, _HarborSkyZenith.rgb * 0.75 + 0.02, saturate(cloud - 0.3));
                cloudCol += _HarborSunColor.rgb * pow(saturate(sd), 3.0) * 0.25 * (1.0 - _HarborOvercast);

                // the discs sit behind the cloud
                float sunDisc = smoothstep(0.99985, 0.99993, sd) * _HarborSunDir.w;
                float md = dot(d, _HarborMoonDir.xyz);
                float moonDisc = smoothstep(0.99990, 0.99995, md) * _HarborMoonDir.w;
                float moonGlow = pow(saturate(md), 400.0) * 0.3 * _HarborMoonDir.w;
                float3 discs = _HarborSunColor.rgb * sunDisc * 30.0 + _HarborMoonColor.rgb * (moonDisc * 2.5 + moonGlow);
                sky += discs * (1.0 - density);

                sky = lerp(sky, cloudCol, density);
                return half4(max(sky, 0.0), 1.0);
            }
            ENDHLSL
        }
    }

    Fallback Off
}
