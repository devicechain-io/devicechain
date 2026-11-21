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
            float4 _HarborSceneryFog; // density, height density, base height, falloff
            float4 _HarborSceneryFogColor;
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
                // Vertex alpha: .1 concrete, .2 promenade pavers, .3 asphalt, .4 granite. Scenery uses 1.
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
                // Fog counts from the camera: about half at 900 m and nine tenths by 1500 m at noon, half at 550 m in the storm.
                // The air density is gained up (the same factor as DayLighting.SceneryFogGain) because the engine's own fog on the lit
                // materials reads the plain value and must stay light.
                float air = _HarborSceneryFog.x * 6.0 * distanceM;
                float h = (i.world.y + _WorldSpaceCameraPos.y) * .5;
                float heightDensity = _HarborSceneryFog.y * exp(clamp((_HarborSceneryFog.z - h) * _HarborSceneryFog.w, -8, 4));
                // Ramp inland: foreground yards, the church, the town's edge and the quays remain crisp in every view.
                float2 outsideCore = max(float2(abs(i.world.x) - 240, i.world.z - 140), 0);
                float inland = smoothstep(120, 420, length(outsideCore));
                float optical = (air * air + heightDensity * distanceM) * inland;
                float transmission = exp(-optical);
                // The distant ridge (vertex alpha 0.6) has its own, lighter fog so it stays a pale readable shape at noon and nearly
                // vanishes in the storm, and its foot dissolves into the haze by height (DayLighting.RidgeFogScale).
                if (abs(i.surface - 0.6) < 0.06)
                {
                    float ridgeAir = _HarborSceneryFog.x * 6.0 * 0.34 * distanceM;
                    transmission = exp(-ridgeAir * ridgeAir) * saturate((i.world.y - 6.0) / 50.0);
                }
                return half4(lerp(_HarborSceneryFogColor.rgb, lit, transmission), 1);
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
