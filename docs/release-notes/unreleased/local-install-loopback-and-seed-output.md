---
section: before-upgrade
---

## en

- **A new local cluster is reachable from this machine only, and text output that is not a terminal no longer prints the superuser's password.** The kind cluster that `dcctl install local` creates now publishes ports 80, 443 and 1883-1885 on `127.0.0.1` instead of every interface. If you reach a local install from another machine (a device on your network, a phone, a second computer), create the cluster with the new `--expose-lan` flag on `dcctl install local` or `dcctl setup local`. A cluster that already exists keeps the ports it was created with until you delete and re-create it. Separately, `dcctl bootstrap` with plain text output now shows the generated superuser password only when its output is a terminal. Piped or redirected output, such as a CI log, names the Secret that holds it instead; read it with the `kubectl ... get secret` command the report prints. A script that took the password from bootstrap's output must read that Secret.

## es

- **Un clúster local nuevo solo es accesible desde esta máquina, y la salida de texto que no es un terminal ya no imprime la contraseña del superusuario.** El clúster de kind que crea `dcctl install local` publica ahora los puertos 80, 443 y 1883-1885 en `127.0.0.1` en lugar de en todas las interfaces. Si accede a una instalación local desde otra máquina (un dispositivo de su red, un teléfono, otro ordenador), cree el clúster con la nueva opción `--expose-lan` de `dcctl install local` o `dcctl setup local`. Un clúster que ya existe conserva los puertos con los que se creó hasta que lo elimine y lo vuelva a crear. Además, `dcctl bootstrap` con salida de texto solo muestra la contraseña generada del superusuario cuando su salida es un terminal. La salida canalizada o redirigida, como un registro de CI, nombra en su lugar el Secret que la guarda; léala con el comando `kubectl ... get secret` que imprime el informe. Un script que tomaba la contraseña de la salida de bootstrap debe leer ese Secret.

## zh-CN

- **新建的本地集群只能从本机访问，非终端的文本输出不再打印超级用户密码。** `dcctl install local` 创建的 kind 集群现在将端口 80、443 和 1883-1885 发布在 `127.0.0.1` 上，而不是所有网络接口。如果您需要从其他机器（网络中的设备、手机、另一台电脑）访问本地安装，请使用 `dcctl install local` 或 `dcctl setup local` 的新选项 `--expose-lan` 创建集群。已存在的集群保留其创建时的端口，直到您删除并重新创建它。另外，使用纯文本输出的 `dcctl bootstrap` 现在仅在输出为终端时显示生成的超级用户密码。管道或重定向的输出（例如 CI 日志）会改为指明保存该密码的 Secret；请使用报告中打印的 `kubectl ... get secret` 命令读取。原先从 bootstrap 输出中获取密码的脚本必须改为读取该 Secret。
