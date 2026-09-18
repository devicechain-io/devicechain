---
sidebar_position: 5
title: Observabilidad y métricas
---

# Observabilidad y métricas

DeviceChain se distribuye con observabilidad integrada, no añadida después: cada servicio
está instrumentado con **métricas de Prometheus** y sondas de salud estándar de Kubernetes, y
`dcctl install` despliega una pila completa de **Prometheus + Grafana + Alertmanager**
en el clúster, para todas sus instancias, de modo que una instalación nueva se puede observar desde su primer minuto,
sin necesidad de armar un proyecto de monitoreo aparte.

:::note Estado
La pila de monitoreo (kube-prometheus-stack a través de `dcctl install`) y el panel de operaciones
de event-processing están implementados y validados de extremo a extremo. Un panel de command-delivery
y sus reglas de alerta se incluyen junto a ellos. Los paneles para las demás áreas funcionales y el
trazado distribuido OTLP son mejoras planeadas a futuro.
:::

## Lo que expone cada servicio

Cada servicio de área funcional se instrumenta a sí mismo con métricas de cliente de Prometheus y
sirve las dos sondas estándar de Kubernetes:

- **`/healthz`** — vitalidad (liveness): ¿el proceso está vivo?
- **`/readyz`** — disponibilidad (readiness): ¿está listo para recibir tráfico? Un servicio que no está
  listo se mantiene fuera de rotación por su Service de Kubernetes (consulte
  [Despliegue y operador](./kubernetes-operator.md)).

Debido a que cada pod habla las mismas convenciones, la pila de monitoreo recolecta métricas de
toda la instancia de manera uniforme; no hay trabajo de integración por servicio.

## La pila de monitoreo

[`dcctl install`](./bootstrap.md#install) aprovisiona el monitoreo como uno de sus módulos
de OpenTofu integrados, **activado por defecto** (`--no-monitoring` lo omite, y también
`--compact`): la misma capa que aprovisiona la base de datos relacional, cert-manager e
ingress también levanta
[kube-prometheus-stack](https://github.com/prometheus-community/helm-charts/tree/main/charts/kube-prometheus-stack)
(Prometheus, Grafana y Alertmanager). Se instala una vez por clúster, y observa a todas las
instancias arrancadas en ese clúster:

- **Recolección entre espacios de nombres (cross-namespace)** — Prometheus se ejecuta en su propio espacio de nombres y recolecta métricas de
  los servicios de cada instancia a través de los espacios de nombres, de modo que una sola pila observa todo el
  despliegue.
- **Los paneles se distribuyen con la plataforma** — los paneles de Grafana viven en el chart de Helm
  (`deploy/helm/devicechain/dashboards/`) y son importados automáticamente por el sidecar de
  paneles de Grafana. Un panel nuevo es un cambio de chart, no una importación manual.
- **Una carpeta por instancia** — cada instancia tiene su propia carpeta de Grafana,
  `devicechain-<instancia>`, con su propia copia de cada panel. Cada copia está acotada a
  esa instancia y lleva su id en el título; no hay selector de instancia que ajustar. Los
  archivos de `dashboards/` son plantillas que el chart genera por instancia, no paneles
  para importar a mano. La carpeta proviene de una anotación
  `grafana_folder: devicechain-<instancia>` en cada ConfigMap de panel, y solo un sidecar
  configurado para leerla organiza los paneles en carpetas: el sidecar debe ejecutarse con
  `FOLDER_ANNOTATION=grafana_folder` y su proveedor de paneles debe tener
  `foldersFromFilesStructure: true`. La pila que despliega `dcctl install` fija ambos. Si
  instaló con `--no-monitoring` y apunta su propio sidecar de Grafana a estos ConfigMaps
  sin ellos, la anotación se ignora y los paneles de todas las instancias quedan planos en
  un mismo lugar: siguen siendo paneles separados, cada uno acotado a su propia instancia,
  solo que sin carpetas.
- **Las instancias de un clúster mantienen sus paneles separados** — una vez que todas las
  instancias de un clúster usan un chart con carpetas por instancia, eliminar o actualizar
  una deja intactos los paneles de las demás. Hasta entonces, las instancias que aún usan un
  chart anterior comparten un panel por cada tablero fuera de cualquier carpeta de
  instancia, y actualizar cualquier instancia elimina ese archivo compartido: el panel de
  una instancia anterior falta hasta que el sidecar de paneles de Grafana vuelve a
  explorar. Un clúster cuya pila de monitoreo es anterior a esta organización muestra los
  paneles de todas las instancias fuera de sus carpetas hasta que se vuelva a ejecutar
  `dcctl install`.
- **Los enlaces antiguos dejan de funcionar** — los antiguos ids compartidos de los paneles
  (`dc-event-processing-ops`, `dc-command-delivery-ops`) no los usa ninguna instancia
  actualizada, así que los marcadores que apuntan a ellos dejan de funcionar una vez que
  todas las instancias se actualizan.
- **Destruir una instancia deja una carpeta vacía** — sus paneles se eliminan, pero su
  carpeta `devicechain-<instancia>`, ya vacía, permanece en Grafana; elimínela a mano.

## Iniciar sesión en Grafana

Grafana usa su propio **inicio de sesión de administrador**. Iniciar sesión en Grafana a
través del inicio de sesión único de DeviceChain no está disponible actualmente.

Las métricas son a nivel de instancia y entre inquilinos, por lo que Grafana es una superficie
de *operador*, no algo a lo que los usuarios de inquilinos puedan acceder. Los inquilinos ven
sus propios datos a través de la consola y los paneles, nunca a través de Grafana.

### Acceder a Grafana

La pila que despliega `dcctl install` no publica ninguna ruta de ingress para Grafana; su
Service es `ClusterIP`. Haga un port-forward y abra `http://localhost:3000/`:

```bash
kubectl -n monitoring port-forward svc/kube-prometheus-stack-grafana 3000:80
```

### La contraseña de administrador

Inicie sesión como `admin`. La contraseña la genera `dcctl install` y se guarda en el Secret
`dc-grafana-admin` del espacio de nombres `monitoring`, clave `admin-password`:

```bash
kubectl -n monitoring get secret dc-grafana-admin -o jsonpath='{.data.admin-password}' | base64 -d
```

El informe de la instalación imprime estos mismos dos comandos. Grafana se ejecuta **una vez
por clúster**, así que este es el inicio de sesión *del clúster*, compartido por todas las
instancias que hay en él, no una contraseña por instancia. Rotarla deja fuera a los operadores
de todas las instancias de ese clúster, no solo a los de la suya.

### Rotarla

Volver a ejecutar `dcctl install` **conserva** la contraseña: el Secret se vuelve a leer y se
reutiliza, y solo se genera una nueva cuando el Secret no existe. Editar el Secret a mano
tampoco la rota: Grafana lee la contraseña del Secret como variable de entorno al arrancar,
un Secret modificado no reinicia nada, y Grafana sigue aceptando la contraseña con la que
arrancó mientras el Secret nombra una que nunca ha visto. Para rotarla a propósito, ejecute
los tres pasos:

```bash
kubectl -n monitoring delete secret dc-grafana-admin
dcctl install <los flags con los que se instaló el clúster>   # un Secret ausente se genera de nuevo
kubectl -n monitoring rollout restart deployment/kube-prometheus-stack-grafana
```

:::caution
No omita el reinicio. Tras los dos primeros pasos el Secret contiene una contraseña nueva y
Grafana sigue aceptando la anterior, así que la rotación parece hecha y no lo está, y la
siguiente persona que lea el Secret no podrá iniciar sesión. El reinicio es lo que la aplica,
y funciona porque el Grafana desplegado no conserva una base de datos persistente.
:::

## El panel de operaciones de event-processing

El motor DETECT/REACT (consulte [Procesamiento de eventos](../concepts/event-processing.md))
es el componente que un operador más necesita vigilar, y se distribuye con un panel de Grafana
dedicado y alertas. El motor emite métricas orientadas al operador, incluido
un **indicador de retraso del consumidor (consumer-lag gauge)**: cuánto se ha retrasado la detección respecto al
flujo de eventos resueltos, y **recuentos de disparo de reglas**, de modo que "¿el motor de alarmas está al día, y qué
está haciendo?" se puede responder de un vistazo.

## Relacionado

- **[Arrancar una instancia](./bootstrap.md#install)** — `dcctl install`, el comando que
  despliega la pila de monitoreo, y sus indicadores (flags).
- **[Despliegue y operador](./kubernetes-operator.md)** — cómo el chart genera
  las cargas de trabajo por servicio con sus sondas de salud.
