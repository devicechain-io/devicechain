// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

Shader "Harborpulse/CoastalScenery"
{
    SubShader
    {
        Tags { "RenderPipeline"="UniversalPipeline" "RenderType"="Opaque" "Queue"="Geometry" }
        Pass
        {
            Name "Scenery"
            Tags { "LightMode"="UniversalForward" }
            HLSLPROGRAM
            #pragma vertex Vert
            #pragma fragment Frag
            #pragma multi_compile_instancing
            #pragma multi_compile _ _ADDITIONAL_LIGHTS_VERTEX _ADDITIONAL_LIGHTS
            #pragma multi_compile _ _CLUSTER_LIGHT_LOOP
            #include "Packages/com.unity.render-pipelines.universal/ShaderLibrary/Core.hlsl"
            #include "Packages/com.unity.render-pipelines.universal/ShaderLibrary/Lighting.hlsl"
            #include "Assets/Harborpulse/Shaders/HarborHaze.hlsl"
            float4 _HarborSceneryFog; // density, height density, base height, falloff
            float4 _HarborSceneryFogColor;
            float4 _HarborSceneryFogLow; // the haze over far ground: a ray below the horizon fades to this (DayLighting.HazeColor)
            struct Attributes { float4 positionOS : POSITION; float3 normalOS : NORMAL; float4 color : COLOR; UNITY_VERTEX_INPUT_INSTANCE_ID };
            struct Varyings { float4 positionCS : SV_POSITION; float3 world : TEXCOORD0; half3 lit : TEXCOORD1; half surface : TEXCOORD2; half3 normalWS : TEXCOORD3; half3 albedo : TEXCOORD4; };
            Varyings Vert(Attributes input)
            {
                UNITY_SETUP_INSTANCE_ID(input);
                Varyings o;
                o.world = TransformObjectToWorld(input.positionOS.xyz);
                o.positionCS = TransformWorldToHClip(o.world);
                o.surface=input.color.a;
                half3 n = TransformObjectToWorldNormal(input.normalOS);
                o.normalWS = n; o.albedo = input.color.rgb;
                Light mainLight = GetMainLight();
                // No specular highlights or tree shadows under the overcast. Geometry supplies each facet's normal.
                // Vertex palettes are authored in display RGB, like the Lit material palettes.
                // Decode once and use Lambert's diffuse scale so concrete and meadow do not clip to cream.
                o.lit = input.color.rgb * (SampleSH(n) + mainLight.color * saturate(dot(n, mainLight.direction)) / PI);
                return o;
            }
            float HashCell(float2 p) { return frac(sin(dot(p,float2(127.1,311.7)))*43758.5453); }
            float Joint(float2 p,float2 spacing,float width)
            {
                float2 cell=p/spacing;
                float2 edge=(.5-abs(frac(cell)-.5))*spacing;
                float2 aa=max(fwidth(p),.002);
                return 1-min(smoothstep(width,width+aa.x,edge.x),smoothstep(width,width+aa.y,edge.y));
            }
            half4 Frag(Varyings i) : SV_Target
            {
                half3 lit=i.lit;
                // URP 17.5 Forward+ uses the clustered loop and requires these InputData
                // members. Evaluate point lights per fragment: paving strips can span a quay.
                // Keep the forest's inexpensive vertex lighting; only core surfaces need lamps.
                #if defined(_ADDITIONAL_LIGHTS) || defined(_ADDITIONAL_LIGHTS_VERTEX) || USE_CLUSTER_LIGHT_LOOP
                if (i.surface < .35)
                {
                    InputData inputData = (InputData)0;
                    inputData.positionWS = i.world;
                    inputData.normalizedScreenSpaceUV = GetNormalizedScreenSpaceUV(i.positionCS);
                    half3 normalWS = normalize(i.normalWS);
                    half3 additional = 0;
                    #if USE_CLUSTER_LIGHT_LOOP
                    [loop] for (uint lightIndex = 0; lightIndex < min(URP_FP_DIRECTIONAL_LIGHTS_COUNT, MAX_VISIBLE_LIGHTS); lightIndex++)
                    {
                        CLUSTER_LIGHT_LOOP_SUBTRACTIVE_LIGHT_CHECK
                        Light light = GetAdditionalLight(lightIndex, i.world);
                        additional += light.color * light.distanceAttenuation * saturate(dot(normalWS, light.direction));
                    }
                    #endif
                    uint lightCount = GetAdditionalLightsCount();
                    LIGHT_LOOP_BEGIN(lightCount)
                        Light light = GetAdditionalLight(lightIndex, i.world);
                        additional += light.color * light.distanceAttenuation * saturate(dot(normalWS, light.direction));
                    LIGHT_LOOP_END
                    lit += i.albedo * additional / PI;
                }
                #endif
                // Vertex alpha: .1 concrete, .2 promenade pavers, .3 asphalt, .4 granite setts. Scenery uses 1, outcrop rock .9, the backdrop .6-.72.
                // Derivative filtering dissolves joints gracefully at HOME distance; there is no texture sampling.
                if(i.surface<.5)
                {
                    float2 p=i.world.xz;
                    float weather=HashCell(floor(p/2.5));
                    float seams=0;
                    if(i.surface<.15)seams=Joint(p,float2(6,6),.025);
                    else if(i.surface<.25)
                    {
                        p.x+=fmod(floor(p.y/.48),2)*.4;
                        seams=Joint(p,float2(.8,.48),.014);
                    }
                    else if(i.surface>.35)seams=Joint(p,float2(3.2,3.2),.025);
                    float grain=HashCell(floor(p*3));
                    float factor=1+(weather-.5)*.10+(grain-.5)*.025-seams*.06;
                    // Distance to the three finite water-facing walls, rather than infinite inland stripes.
                    float edge=min(length(float2(p.x-clamp(p.x,-120,105),p.y-75)),
                        min(length(float2(p.x-140,p.y-clamp(p.y,-80,65))),length(float2(p.x+155,p.y-clamp(p.y,-130,90)))));
                    float stain=(1-smoothstep(.6,5,edge))*(.22+.12*weather);
                    if(i.surface<.25)factor-=stain;
                    lit*=factor;
                }
                float distanceM = distance(_WorldSpaceCameraPos, i.world);
                // Granite (vertex alpha 0.9): blotches, speckle and bedding cracks from the world position, no texture. The fine grain
                // is only drawn close; from HOME the rock reads by its facets' own tones. No derivative is taken in here, so the branch
                // is a real one and the forest and the ground, which are nearly every scenery pixel, do not pay for it.
                [branch] if (i.surface > .85 && i.surface < .95)
                {
                    float3 w = i.world;
                    float blotch = HashCell(floor(w.xz / 1.7) + floor(w.y / 1.1) * 7.3);
                    float speck = HashCell(floor(w.xz * 2.6) + floor(w.y * 2.6) * 3.1);
                    float bedY = w.y / .8 + blotch * .35;
                    float crack = 1 - smoothstep(.04, .07, abs(frac(bedY) - .5));
                    float crackFade = saturate(1 - distanceM / 90.0);
                    float nearFade = saturate(1 - distanceM / 220.0);
                    float midFade = saturate(1 - distanceM / 900.0);
                    lit *= 1 + (blotch - .5) * .26 * midFade + (speck - .5) * .12 * nearFade - crack * .30 * crackFade;
                    // lichen on the faces that look at the sky: pale grey-green rosettes and a little rust
                    float patch = HashCell(floor(w.xz / 2.3) + 11.0);
                    float upward = saturate(normalize(i.normalWS).y * 2 - .6);
                    float3 lichen = patch > .93 ? float3(1.18, .92, .62) : float3(.96, 1.08, .78);
                    lit *= lerp(float3(1, 1, 1), lichen, step(.74, patch) * upward * midFade * .7);
                }
                // Fog counts from 350 m out (DayLighting.SceneryFogStartM): from HOME 1000-1100 m is still clear, half falls near 1750 m at
                // noon and near 1000 m in the storm. The air density is gained up (DayLighting.SceneryFogGain) because the engine's own fog
                // on the lit materials reads the plain value and must stay light.
                float depth = max(0, distanceM - 350.0);
                float air = _HarborSceneryFog.x * 3.5 * depth;
                float h = (i.world.y + _WorldSpaceCameraPos.y) * .5;
                float heightDensity = _HarborSceneryFog.y * exp(clamp((_HarborSceneryFog.z - h) * _HarborSceneryFog.w, -8, 4));
                // Ramp inland: foreground yards, the church, the town's edge and the quays remain crisp in every view.
                float2 outsideCore = max(float2(abs(i.world.x) - 240, i.world.z - 140), 0);
                float inland = smoothstep(120, 420, length(outsideCore));
                float optical = (air * air + heightDensity * depth) * inland;
                float transmission = exp(-optical);
                // The backdrop (vertex alpha 0.6 the far ridge, 0.66 the middle range, 0.72 the foothills) has its own, lighter fog so each
                // layer stays a readable shape at noon and nearly vanishes in the storm; the nearer a layer the less air is in front of it,
                // so they step back in tone. Each layer's foot dissolves into the ground haze over its own height
                // (DayLighting.RidgeFogScale, BackdropTransmission; the heights are CoastalTerrain's FootFadeM).
                if (i.surface > 0.55 && i.surface < 0.78)
                {
                    float ridgeAir = _HarborSceneryFog.x * 3.5 * 0.40 * distanceM;
                    float foot = i.surface < 0.63 ? saturate((i.world.y - 6.0) / 50.0)
                        : i.surface < 0.69 ? saturate((i.world.y - 4.0) / 26.0) : saturate((i.world.y - 4.0) / 14.0);
                    float layerT = exp(-ridgeAir * ridgeAir) * foot;
                    // Seen from well above (the 9:16 HOME stands 230 m up and looks along the coast at the eastern uplands), a layer lies in
                    // the same haze as the ground round it: with its lighter fog alone it stood out of that haze as a dark lens. Edge-on
                    // from the 16:9 HOME and from the quays (the ray within 1.7 degrees of level) nothing changes.
                    float above = smoothstep(0.03, 0.09, -(i.world.y - _WorldSpaceCameraPos.y) / max(distanceM, 1.0));
                    transmission = lerp(layerT, layerT * (0.40 + 0.60 * transmission), above);
                }
                // The haze a ray fades into depends on its elevation: the horizon's colour along the horizon, the darker far-ground haze
                // below it. The sky shader draws the same function below the horizon, so the land's far edge has no seam.
                float3 ray = (i.world - _WorldSpaceCameraPos) / max(distanceM, 1.0);
                float3 hazeColor = HarborHazeColor(ray, _HarborSceneryFogColor.rgb, _HarborSceneryFogLow.rgb);
                return half4(lerp(hazeColor, lit, transmission), 1);
            }
            ENDHLSL
        }
        Pass
        {
            Name "DepthOnly"
            Tags { "LightMode"="DepthOnly" }
            ZWrite On
            ColorMask R
            HLSLPROGRAM
            #pragma vertex DepthVert
            #pragma fragment DepthFrag
            #pragma multi_compile_instancing
            #include "Packages/com.unity.render-pipelines.universal/ShaderLibrary/Core.hlsl"
            struct Attributes { float4 positionOS : POSITION; UNITY_VERTEX_INPUT_INSTANCE_ID };
            float4 DepthVert(Attributes i) : SV_POSITION { UNITY_SETUP_INSTANCE_ID(i); return TransformObjectToHClip(i.positionOS.xyz); }
            half DepthFrag() : SV_Target { return 0; }
            ENDHLSL
        }
    }
}
