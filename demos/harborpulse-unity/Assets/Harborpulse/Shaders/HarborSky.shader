// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// The procedural sky. A three-stop gradient graded by the time of day, the low sun's glow along the horizon, the sun and moon discs
// where HarborSkyController puts their lights, a star field at night, and two cloud layers: high streaks and low chunky cumulus whose
// cover, depth and colours follow the weather (separate clouds on a fair day, a building front, a closed dark deck with rain curtains
// in the storm, breaking up afterwards). The story cameras look near the horizon, so the detail lives in the low sky. Every input is a
// global the controller sets from DayLighting each frame; the cloud moves by the scenario clock, so a still of an hour is always the same.
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

            float4 _HarborSkyTop;      // the clear sky at the zenith
            float4 _HarborSkyMid;      // the middle sky
            float4 _HarborSkyLow;      // the horizon band
            float4 _HarborSkyHaze;     // the fog colour: the horizon line itself, so the haze over the land meets the sky without a seam
            float4 _HarborSkyGlow;     // rgb the low sun's glow, a its strength
            float4 _HarborSunDir;      // towards the sun, w = disc visibility
            float4 _HarborSunColor;
            float4 _HarborMoonDir;     // towards the moon, w = disc visibility
            float4 _HarborMoonColor;
            float4 _HarborCloudLit;
            float4 _HarborCloudShade;
            float4 _HarborCloudLight;  // towards the body that lights the cloud
            float4 _HarborCloud;       // x cover, y depth, z rain
            float4 _HarborCloudDrift;  // xy the low layer's offset, zw the high layer's
            float _HarborStars;
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

            float Fbm4(float2 p)
            {
                float s = 0.5 * Noise(p);
                p = p * 2.03 + 11.7; s += 0.25 * Noise(p);
                p = p * 2.03 + 11.7; s += 0.125 * Noise(p);
                p = p * 2.03 + 11.7; s += 0.0625 * Noise(p);
                return s / 0.9375;
            }

            float Fbm3(float2 p)
            {
                float s = 0.5 * Noise(p);
                p = p * 2.11 + 7.3; s += 0.25 * Noise(p);
                p = p * 2.11 + 7.3; s += 0.125 * Noise(p);
                return s / 0.875;
            }

            half4 Frag(Varyings i) : SV_Target
            {
                float3 d = normalize(i.dir);
                float h = d.y;
                float up = max(h, 0.0);
                float cover = _HarborCloud.x;
                float depth = _HarborCloud.y;
                float rain = _HarborCloud.z;

                // the clear sky: horizon band, middle sky, zenith
                float3 sky = lerp(_HarborSkyLow.rgb, _HarborSkyMid.rgb, smoothstep(0.0, 0.10, up));
                sky = lerp(sky, _HarborSkyTop.rgb, smoothstep(0.10, 0.70, up));

                // the low sun's glow lies along the whole horizon and is strongest towards the sun
                float2 az = normalize(d.xz + float2(1e-5, 0.0));
                float2 sunAz = normalize(_HarborSunDir.xz + float2(1e-5, 0.0));
                float toward = saturate(dot(az, sunAz) * 0.5 + 0.5);
                toward = toward * toward * toward;
                sky += _HarborSkyGlow.rgb * _HarborSkyGlow.a * exp(-up * 6.0) * (0.25 + 0.75 * toward);

                float sd = dot(d, _HarborSunDir.xyz);
                float md = dot(d, _HarborMoonDir.xyz);
                float sunUp = saturate(_HarborSunDir.y * 6.0 + 0.5);
                sky += _HarborSunColor.rgb * (pow(saturate(sd), 48.0) * 0.30 + pow(saturate(sd), 6.0) * 0.08) * sunUp * (1.0 - 0.7 * depth);

                // stars, fixed to the sky, fading towards the horizon
                float3 sp = d * 220.0;
                float3 cell = floor(sp);
                float star = step(0.9962, Hash31(cell));
                float3 f = frac(sp) - 0.5;
                star *= (1.0 - smoothstep(0.0, 0.26, length(f))) * (0.6 + 0.4 * sin(_HarborSkyTime * 2.0 + Hash31(cell + 5.0) * 30.0));
                float3 stars = star * _HarborStars * saturate(up * 6.0) * float3(1.7, 1.75, 1.9);

                // both cloud layers lie on planes above the camera; the +0.22 keeps their shapes whole down to the horizon
                float2 plane = d.xz / (up + 0.22);

                // the high layer: thin streaks, stretched along the wind
                float2 hp = plane * float2(0.50, 1.55) + _HarborCloudDrift.zw;
                float streak = Fbm3(hp);
                float high = smoothstep(0.50, 0.78, streak) * (0.30 + 0.50 * cover) * smoothstep(0.0, 0.07, up);

                // the low layer: chunky cumulus that closes into a deck
                float2 lp = plane * 1.30 + _HarborCloudDrift.xy;
                float n = Fbm4(lp);
                float threshold = lerp(0.70, 0.22, cover);
                float low = smoothstep(threshold, threshold + 0.14, n) * smoothstep(-0.01, 0.05, h);

                // relief: the side of a cloud towards the light is lit, its thick middle and far side fall to the base colour
                float2 lightAz = normalize(_HarborCloudLight.xz + float2(1e-4, 0.0));
                float n2 = Fbm4(lp + lightAz * 0.11);
                float tone = saturate((n - n2) * 4.5 + 0.55 - saturate((n - threshold) * 2.2) * 0.45);
                // the deck of a front is never one grey card: broad lighter and darker reaches move through it
                float reach = Noise(lp * 0.37 + 3.1);
                tone = saturate(tone - depth * (0.55 - reach) * 0.9);
                // three flat tones with soft joins, to sit with the low-poly world
                tone = (smoothstep(0.26, 0.36, tone) + smoothstep(0.56, 0.66, tone)) * 0.5;
                float3 cloudCol = lerp(_HarborCloudShade.rgb, _HarborCloudLit.rgb, tone);
                // the sun or the moon behind thin cloud shows as a bright patch
                cloudCol += _HarborSunColor.rgb * pow(saturate(sd), 10.0) * 0.40 * sunUp * (1.0 - depth);
                cloudCol += _HarborMoonColor.rgb * pow(saturate(md), 40.0) * 0.10 * _HarborMoonDir.w * (1.0 - depth);
                float3 highCol = lerp(_HarborCloudLit.rgb, _HarborCloudShade.rgb, 0.25 + 0.5 * depth);

                // rain curtains hang under the deck towards the horizon, slanted by the wind
                // sampled round the compass on a circle, so there is no seam where a bearing would wrap
                float2 around = az * 4.5 + float2(up * 4.0 + _HarborCloudDrift.x * 0.4, 2.7);
                float curtain = Noise(around);
                curtain = smoothstep(0.42, 0.78, curtain) * rain * (1.0 - smoothstep(0.03, 0.40, up));

                // the discs sit behind the cloud
                float sunDisc = smoothstep(0.99968, 0.99978, sd) * _HarborSunDir.w;
                float moonDisc = smoothstep(0.99955, 0.99964, md) * _HarborMoonDir.w;
                float moonGlow = (pow(saturate(md), 260.0) * 0.30 + pow(saturate(md), 30.0) * 0.05) * _HarborMoonDir.w;
                float open = (1.0 - low) * (1.0 - low) * (1.0 - high * 0.6);
                sky += (_HarborSunColor.rgb * sunDisc * 9.0 + _HarborMoonColor.rgb * (moonDisc * 2.4 + moonGlow) + stars) * open;

                sky = lerp(sky, highCol, high * (1.0 - low));
                sky = lerp(sky, cloudCol, low);
                sky = lerp(sky, _HarborCloudShade.rgb * 0.75, curtain * 0.55);

                // the horizon line is the fog colour, and so is everything under it: the land and the sea fade into the same haze
                float haze = exp(-up * 42.0);
                sky = lerp(sky, _HarborSkyHaze.rgb, haze);
                // under a closed deck at sunset a last ember shows low in the west
                sky += _HarborSkyGlow.rgb * _HarborSkyGlow.a * exp(-up * 26.0) * toward * 0.45 * step(0.0, h);
                sky = lerp(sky, _HarborSkyHaze.rgb, step(h, 0.0));
                return half4(max(sky, 0.0), 1.0);
            }
            ENDHLSL
        }
    }

    Fallback Off
}
