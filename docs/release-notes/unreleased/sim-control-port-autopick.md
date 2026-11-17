---
section: whats-new
---

## en

- **Separate simulator control ports.** `dcctl sim create` now picks the lowest port from 8090–8189 that no other saved local sim uses, allowing multiple sims to run together. The chosen address is printed; pass `--control-addr` to choose one explicitly.

## es

- **Puertos de control separados para los simuladores.** `dcctl sim create` ahora elige el puerto más bajo entre 8090 y 8189 que no use otro simulador local guardado, lo que permite ejecutar varios a la vez. Se muestra la dirección elegida; use `--control-addr` para indicar una explícitamente.

## zh-CN

- **模拟器使用独立控制端口。** `dcctl sim create` 现在会从 8090–8189 中选择其他已保存的本地模拟器尚未使用的最低端口，让多个模拟器能够同时运行。命令会显示所选地址；如需指定地址，请传入 `--control-addr`。
