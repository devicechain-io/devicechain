---
section: before-upgrade
---

## en

- **A new local cluster is reachable from this machine only.** The kind cluster that `dcctl install local` creates now publishes ports 80, 443 and 1883-1885 on `127.0.0.1` instead of every interface. If you reach a local install from another machine (a device on your network, a phone, a second computer), create the cluster with the new `--expose-lan` flag on `dcctl install local` or `dcctl setup local`. A cluster that already exists keeps the ports it was created with until you delete and re-create it.

## es

- **Un clúster local nuevo solo es accesible desde esta máquina.** El clúster de kind que crea `dcctl install local` publica ahora los puertos 80, 443 y 1883-1885 en `127.0.0.1` en lugar de en todas las interfaces. Si accede a una instalación local desde otra máquina (un dispositivo de su red, un teléfono, otro ordenador), cree el clúster con la nueva opción `--expose-lan` de `dcctl install local` o `dcctl setup local`. Un clúster que ya existe conserva los puertos con los que se creó hasta que lo elimine y lo vuelva a crear.

## zh-CN

- **新建的本地集群只能从本机访问。** `dcctl install local` 创建的 kind 集群现在将端口 80、443 和 1883-1885 发布在 `127.0.0.1` 上，而不是所有网络接口。如果您需要从其他机器（网络中的设备、手机、另一台电脑）访问本地安装，请使用 `dcctl install local` 或 `dcctl setup local` 的新选项 `--expose-lan` 创建集群。已存在的集群保留其创建时的端口，直到您删除并重新创建它。
