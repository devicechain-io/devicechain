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
            #include "Packages/com.unity.render-pipelines.universal/ShaderLibrary/Core.hlsl"
            #include "Packages/com.unity.render-pipelines.universal/ShaderLibrary/Lighting.hlsl"
            float4 _HarborSceneryFog; // density, height density, base height, falloff
            float4 _HarborSceneryFogColor;
            struct Attributes { float4 positionOS : POSITION; float3 normalOS : NORMAL; float4 color : COLOR; UNITY_VERTEX_INPUT_INSTANCE_ID };
            struct Varyings { float4 positionCS : SV_POSITION; float3 world : TEXCOORD0; half3 lit : TEXCOORD1; half surface : TEXCOORD2; };
            Varyings Vert(Attributes input)
            {
                UNITY_SETUP_INSTANCE_ID(input);
                Varyings o;
                o.world = TransformObjectToWorld(input.positionOS.xyz);
                o.positionCS = TransformWorldToHClip(o.world);
                o.surface=input.color.a;
                half3 n = TransformObjectToWorldNormal(input.normalOS);
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
                float depth = max(0, distanceM - 900);
                float air = _HarborSceneryFog.x * depth;
                float h = (i.world.y + _WorldSpaceCameraPos.y) * .5;
                float heightDensity = _HarborSceneryFog.y * exp(clamp((_HarborSceneryFog.z - h) * _HarborSceneryFog.w, -8, 4));
                // Ramp inland: foreground yards, the church and the quays remain crisp in both HOME aspects.
                float2 outsideCore = max(float2(abs(i.world.x) - 240, i.world.z - 140), 0);
                float inland = smoothstep(1050, 1900, length(outsideCore));
                float optical = (air * air + heightDensity * depth) * inland;
                float transmission = exp(-optical);
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
