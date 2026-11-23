---
section: whats-new
---

## en

- **The waterfront 3D scene draws every device of its scenario.** `demos/harborpulse-unity` now shows the six devices the `harborpulse` simulator provisions beside its district: two chargers that connect over MQTT with a device token, two that connect with a client certificate, and two lamps that fetch their commands over HTTP. The chargers continue North Quay's charging row and the lamps stand at a landing by the wall. Each kind looks different and shows only what it reports: a charger's lamp is lit while it reports and turns violet when it has gone silent, and a pull lamp's post shows its reported dim level as four pips. The opening caption now counts 43 devices. The scene's layout file changed with this, so use the scene and the simulator from the same release: a scene built before this release turns its guided tour off when it meets a newer simulator. A live or preview player also accepts `-hp-eye x/y/z/lookX/lookY/lookZ/fov`, which holds the camera at one place for a whole run.

## es

- **La escena 3D del puerto dibuja todos los dispositivos de su escenario.** `demos/harborpulse-unity` muestra ahora los seis dispositivos que el simulador `harborpulse` aprovisiona junto a su distrito: dos cargadores que se conectan por MQTT con un token de dispositivo, dos que se conectan con un certificado de cliente y dos farolas que recogen sus comandos por HTTP. Los cargadores continúan la fila de carga del Muelle Norte y las farolas están en un embarcadero junto al muro. Cada clase tiene un aspecto distinto y muestra solo lo que informa: el piloto de un cargador se enciende mientras informa y pasa a violeta cuando ha quedado en silencio, y el poste de una farola muestra en cuatro puntos el nivel de atenuación que informó. El rótulo inicial cuenta ahora 43 dispositivos. El archivo de trazado de la escena cambió con esto, así que use la escena y el simulador de la misma versión: una escena compilada antes de esta versión desactiva su recorrido guiado cuando encuentra un simulador más nuevo. Un reproductor en vivo o en vista previa acepta además `-hp-eye x/y/z/lookX/lookY/lookZ/fov`, que mantiene la cámara en un mismo lugar durante toda la ejecución.

## zh-CN

- **海滨 3D 场景现在绘制其场景中的全部设备。** `demos/harborpulse-unity` 现在显示 `harborpulse` 模拟器在街区之外预置的六台设备：两台使用设备令牌通过 MQTT 连接的充电桩、两台使用客户端证书连接的充电桩，以及两盏通过 HTTP 拉取命令的路灯。充电桩接在北码头的充电位之后，路灯立在岸壁旁的登岸处。每一类外观各不相同，并且只显示它上报的内容：充电桩在上报期间指示灯亮起，沉默后变为紫色；拉取式路灯的灯杆用四个光点显示它上报的调光级别。开场字幕现在统计 43 台设备。场景的布局文件随之改变，因此请使用同一版本的场景和模拟器：在此版本之前构建的场景遇到更新的模拟器时会关闭其导览。实时或预览播放器还接受 `-hp-eye x/y/z/lookX/lookY/lookZ/fov`，在整个运行期间把镜头固定在一处。
