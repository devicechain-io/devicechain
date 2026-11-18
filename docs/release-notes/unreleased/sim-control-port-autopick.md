---
section: whats-new
---

## en

- **Separate simulator control ports.** `dcctl sim create` now gives a new sim the lowest port from 8090–8189 that no other saved local sim uses, allowing multiple sims to run together. Re-creating an existing sim keeps the address it already has, unless another saved sim now uses that port, so `dcctl sim status` still reaches a sim that is running. The chosen address is printed; pass `--control-addr` to choose one explicitly.

## es

- **Puertos de control separados para los simuladores.** `dcctl sim create` ahora asigna a un simulador nuevo el puerto más bajo entre 8090 y 8189 que no use otro simulador local guardado, lo que permite ejecutar varios a la vez. Volver a crear un simulador existente conserva la dirección que ya tiene, salvo que otro simulador guardado use ahora ese puerto, así que `dcctl sim status` sigue llegando a un simulador en ejecución. Se muestra la dirección elegida; use `--control-addr` para indicar una explícitamente.

## zh-CN

- **模拟器使用独立控制端口。** `dcctl sim create` 现在会为新模拟器从 8090–8189 中选择其他已保存的本地模拟器尚未使用的最低端口，让多个模拟器能够同时运行。重新创建已有的模拟器时会保留其原有地址，除非另一个已保存的模拟器现在正在使用该端口，因此 `dcctl sim status` 仍能连上正在运行的模拟器。命令会显示所选地址；如需指定地址，请传入 `--control-addr`。
