---
section: whats-new
---

## en

- **A 3D scene for the waterfront scenario (first milestone).** `demos/harborpulse-unity` is a Unity 6 (URP) project that draws the `harborpulse` simulator's harbour district, laid out from the scenario's own coordinates, under a sky whose sun, moon, stars and storm follow the sim's compressed day on its `/status`. The harbour water is a custom shader with Gerstner waves, foam and rain, and its height on each quay is the median of that quay's tide gauges as the platform reports them. The street lights show what the platform last heard from each one: on, off, or silent. The scene reads the platform through the DeviceChain C# SDK and never writes to it. An offline preview, clearly labelled, runs without an instance.

## es

- **Una escena 3D para el escenario del puerto (primer hito).** `demos/harborpulse-unity` es un proyecto de Unity 6 (URP) que dibuja el distrito portuario del simulador `harborpulse`, dispuesto a partir de las coordenadas del propio escenario, bajo un cielo cuyo sol, luna, estrellas y tormenta siguen el día comprimido de la simulación en su `/status`. El agua del puerto es un shader propio con olas de Gerstner, espuma y lluvia, y su altura en cada muelle es la mediana de los mareógrafos de ese muelle tal como los informa la plataforma. Las farolas muestran lo último que la plataforma supo de cada una: encendida, apagada o en silencio. La escena lee la plataforma a través del SDK de C# de DeviceChain y nunca escribe en ella. Una vista previa sin conexión, claramente etiquetada, funciona sin instancia.

## zh-CN

- **海滨场景的 3D 画面（第一个里程碑）。** `demos/harborpulse-unity` 是一个 Unity 6（URP）项目，按场景自身的坐标绘制 `harborpulse` 模拟器的港区；天空中的太阳、月亮、星星和风暴跟随模拟器在 `/status` 上报告的压缩一天变化。港口水面使用自定义着色器，带有 Gerstner 波浪、泡沫和雨滴，每个码头的水位取平台报告的该码头各潮位计读数的中位数。路灯显示平台最后一次收到的每盏灯的状态：亮、灭或沉默。该场景通过 DeviceChain C# SDK 读取平台数据，从不写入。另有明确标注的离线预览，无需实例即可运行。
