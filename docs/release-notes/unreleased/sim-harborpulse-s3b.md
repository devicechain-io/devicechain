---
section: whats-new
---

## en

- **Harborpulse checks shared thresholds, LoRaWAN commands and metric backfill.** Pump alarms follow per-device shared attributes and a mid-day threshold change. A typed gauge command travels through the platform's LPWAN downlink adapter to the scenario sink's ChirpStack queue, returns Class A acknowledgements and changes stored sampling cadence. A calculated-metric backfill fills a measured span whose depth constant was initially missing. Three new verifier invariants each have a planted defect and offline controls. The rig enables the downlink adapter and backfill worker; it still admits only the sink's address for platform egress.

## es

- **Harborpulse comprueba umbrales compartidos, comandos LoRaWAN y el relleno histórico de métricas.** Las alarmas de las bombas siguen atributos compartidos por dispositivo y un cambio de umbral durante el día. Un comando tipado llega a la cola ChirpStack del receptor mediante el adaptador LPWAN, devuelve confirmaciones de clase A y cambia la cadencia de las lecturas almacenadas. Un trabajo rellena las métricas calculadas de un intervalo cuya constante de profundidad faltaba inicialmente. Tres invariantes nuevos tienen defectos deliberados y controles sin conexión. El rig habilita el adaptador y el trabajador de relleno histórico; la salida de la plataforma sigue limitada a la dirección del receptor.

## zh-CN

- **Harborpulse 新增共享阈值、LoRaWAN 命令和计算指标回填验证。** 水泵告警使用各设备的共享属性，并验证白天修改阈值后的行为。带类型的水位计命令通过平台的 LPWAN 下行适配器进入场景接收端的 ChirpStack 队列，返回 Class A 确认，并改变已存储读数的采样间隔。回填任务补齐因最初缺少井深常量而缺失的计算指标。三个新不变量均包含故意植入的缺陷和离线对照测试。测试脚本启用下行适配器和回填工作进程；平台的出站地址仍仅允许场景接收端。
