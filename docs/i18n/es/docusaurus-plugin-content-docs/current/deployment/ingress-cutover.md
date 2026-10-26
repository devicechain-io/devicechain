---
sidebar_position: 4.7
title: Pasar a Traefik
---

# Pasar una instalación existente a Traefik {#ingress-cutover}

Las instalaciones nuevas sirven su host con **Traefik** y la **Gateway API** de Kubernetes. Las versiones anteriores
usaban `ingress-nginx`, que el proyecto Kubernetes ha retirado. Esta página es para una instalación que ya ejecuta
`ingress-nginx`: el paso se da por etapas y el host sigue respondiendo todo el tiempo. Un clúster sin `ingress-nginx`
no necesita nada de esto; `dcctl install` le instala solo Traefik.

Qué hace cada comando en un clúster que todavía tiene `ingress-nginx`:

- **`dcctl install`** deja `ingress-nginx` sirviendo e instala Traefik y las definiciones de la Gateway API a su lado.
  Traefik recibe un balanceador de carga propio, y por tanto una dirección propia.
- **`dcctl upgrade`** da a cada instancia un Gateway y HTTPRoutes, y mantiene sus objetos Ingress a su lado mientras
  `ingress-nginx` siga en el clúster. Ambos sirven el host.
- **El DNS** lo mueves tú, cuando estés listo.
- **`dcctl upgrade --drop-legacy-ingress`** deja de generar los objetos Ingress de la instancia.
- **`dcctl install --remove-legacy-ingress`** quita `ingress-nginx`, y solo cuando ningún objeto Ingress nombra su clase.

## Antes de empezar {#ingress-cutover-before}

- Conoce qué cambia en el borde. Consulta [Qué cambia en el borde](#ingress-cutover-differences).
- Si la instancia obtiene su certificado de un emisor ACME (`ingress.tls.selfSigned: false` y un `ClusterIssuer`),
  lee antes [Certificados de un emisor ACME](#ingress-cutover-acme).
- Pasa a `dcctl install` los ajustes con los que se instaló el clúster, como `--ha`, además de la nueva `--version`,
  igual que en cualquier [actualización progresiva](./releases-and-upgrades.md#rolling-upgrades).

## Los pasos {#ingress-cutover-steps}

1. **Instala el controlador nuevo.** Esto deja `ingress-nginx` sirviendo.

   ```bash
   dcctl install <provider> --version <new-version>
   ```

2. **Actualiza cada instancia.** Con `--dry-run` primero se imprime qué front end tendrá la instancia.

   ```bash
   dcctl upgrade <provider> <instance> --version <new-version> --dry-run
   dcctl upgrade <provider> <instance> --version <new-version>
   ```

   La instancia tiene ahora un Gateway llamado `devicechain` en su namespace (`dci-<instance>`), con un listener `http`
   y otro `https`, y HTTPRoutes detrás. Sus objetos Ingress siguen ahí. Los dos tienen **certificados distintos**: los
   objetos Ingress conservan `devicechain-tls`, que es a lo que el DNS sigue llegando, de modo que el host en
   producción nunca espera un certificado nuevo; el Gateway usa `devicechain-tls-gateway`. El Gateway conserva ese
   nombre para siempre, incluso cuando los objetos Ingress ya no existan.

   Después la actualización espera al certificado del Gateway. Consulta [La compuerta del certificado](#ingress-cutover-cert-gate).

3. **Busca la dirección de Traefik** y comprueba la instancia a través de ella, antes de mover el DNS.

   ```bash
   kubectl -n traefik get service traefik
   kubectl -n dci-<instance> get gateway devicechain
   curl --resolve <host>:443:<traefik-address> https://<host>/
   ```

   El `EXTERNAL-IP` del Service (o el nombre de host, en AWS) es la dirección de Traefik. El Gateway debe informar
   `Programmed`. `--resolve` hace que `curl` envíe la solicitud a Traefik usando aun así tu nombre de host, de modo que
   el certificado y las rutas son los que vería un navegador. Añade `-k` (o `--cacert` con la CA de la instancia, véase
   [TLS, flags e instalación en un portátil](./bootstrap.md#install-tls-and-flags)) cuando la instancia es autofirmada.

4. **Mueve el DNS.** Apunta el host a la dirección de Traefik. No hace falta nada más, y nada tiene que ser rápido:
   los objetos Ingress siguen respondiendo mientras haya clientes que resuelvan la dirección antigua.

5. **Quita los objetos Ingress.** Cuando el tráfico se haya movido, deja de generarlos:

   ```bash
   dcctl upgrade <provider> <instance> --drop-legacy-ingress
   ```

   Hazlo para todas las instancias del clúster.

6. **Quita `ingress-nginx`.**

   ```bash
   dcctl install <provider> --remove-legacy-ingress
   ```

   Pasa los mismos ajustes que en el paso 1. Quitar el controlador no quita los objetos Ingress, y uno que se quedara
   atrás dejaría de responder sin que se informara de ningún fallo, así que el comando **se niega** mientras exista alguno:

   ```text
   ingress-nginx is still serving 1 Ingress object(s), and removing the controller leaves them in place with
   nothing to serve them, so every host they carry stops answering:
     dci-beta/devicechain-api
   Move each instance to the Gateway first: point DNS at the new controller, then run
   `dcctl upgrade <provider> <instance> --drop-legacy-ingress` for it. Nothing has been changed
   ```

   La lista nombra todos los Ingress servidos por la clase de `ingress-nginx`, en cualquier namespace, incluidos los que
   hayas creado tú. Muévelos o elimínalos también. El Ingress que creó la instalación para Grafana no cuenta: pasa a
   Traefik en la misma aplicación.

### En un clúster local {#ingress-cutover-local}

Un clúster local (kind) publica los puertos 80 y 443 de un nodo, y solo un controlador puede ocuparlos. Mientras los
dos controladores estén instalados, `ingress-nginx` conserva los puertos y Traefik es accesible en un NodePort. La
eliminación del paso 6 entrega los puertos a Traefik en la misma aplicación, así que espera un corte breve mientras
ocurre. Un clúster local **nuevo** no tiene `ingress-nginx`, y Traefik ocupa los puertos desde el principio. En un
clúster local que tiene ambos, una instancia **nueva** la sirve `ingress-nginx` (sus puertos son los que alcanza un
navegador) hasta la eliminación; `--ingress-controller gateway` toma el Gateway de todos modos.

### Varias instancias {#ingress-cutover-several}

El paso del clúster es compartido y cada instancia se mueve a su propio ritmo. `ingress-nginx` se queda hasta que la
última instancia haya ejecutado `--drop-legacy-ingress`. Si las instancias de un clúster usan hosts distintos, hay que
mover el DNS de cada uno.

## La compuerta del certificado {#ingress-cutover-cert-gate}

Un Gateway sin certificado es una versión que parece correcta y responde `https` con el certificado por defecto del
controlador. Por eso una actualización que pone una instancia en el Gateway espera, hasta cinco minutos, a que
cert-manager haya emitido el certificado del Gateway y esté `Ready`. No se aplica con `--no-tls`, ni en un clúster sin
cert-manager.

Si falla, los servicios ya están en marcha. El error nombra lo que encontró, y tú lo arreglas y **ejecutas de nuevo la
misma actualización**. Las causas habituales:

- **Existe un Certificate para el Secret, pero el Gateway no lo controla.** cert-manager no adopta un Certificate que
  no creó para el Gateway, así que uno sobrante de un Ingress, o hecho a mano, con el mismo nombre de Secret deja al
  Gateway sin ninguno. El error imprime el comando que lo elimina
  (`kubectl -n dci-<instance> delete certificate <name>`); entonces se crea el del propio Gateway.
- **No se crea ningún Certificate.** El soporte de Gateway API de cert-manager no está activado, o cert-manager arrancó
  antes de que existieran las definiciones de la Gateway API. `kubectl -n cert-manager logs deploy/cert-manager` lo
  indica. Un `dcctl install` de esta versión lo activa.
- **El Certificate no está `Ready`.** El error incluye el mensaje del propio emisor. Con un emisor ACME, véase más abajo.

## Certificados de un emisor ACME {#ingress-cutover-acme}

Un `ClusterIssuer` HTTP-01 que resuelve a través de la clase de ingress `nginx` no puede responder a un desafío de un
Gateway, y **sus renovaciones fallan cuando se quita `ingress-nginx`**, también las de cualquier objeto Ingress que aún
lo use. Dale al emisor un solver `gatewayHTTPRoute`, con un `parentRef` al listener `http` del Gateway de la instancia,
`devicechain`, en el namespace de la instancia:

```yaml
apiVersion: cert-manager.io/v1
kind: ClusterIssuer
metadata:
  name: letsencrypt
spec:
  acme:
    # server, email and privateKeySecretRef as before
    solvers:
      - http01:
          gatewayHTTPRoute:
            parentRefs:
              - group: gateway.networking.k8s.io
                kind: Gateway
                name: devicechain
                namespace: dci-<instance>
                sectionName: http
```

Un emisor compartido por varias instancias lista un `parentRef` por cada Gateway de instancia. La redirección de
`http` a `https` del chart es una coincidencia menos específica que la ruta del desafío, así que no lo intercepta.

La validación HTTP-01 se conecta a donde resuelva el nombre del host. Mientras el DNS no apunte a Traefik, un desafío
para el certificado nuevo del Gateway llega a `ingress-nginx` y no tiene ruta allí, así que espera que la compuerta del
certificado aguarde al cambio de DNS. Un solver DNS-01 no depende de dónde resuelva el host y evita ese orden.

## Volver atrás {#ingress-cutover-rollback}

Mientras `ingress-nginx` siga en el clúster, una instancia puede volver a él:

```bash
dcctl upgrade <provider> <instance> --ingress-controller nginx
```

La elección **queda registrada en la instancia** y la conservan todas las actualizaciones posteriores, porque una
actualización vuelve a derivar todo lo que genera, y un retroceso que una actualización normal deshiciera no sería tal.
`--ingress-controller gateway` registra la otra elección. `--ingress-controller auto` borra el registro, y la instancia
se vuelve a decidir según el clúster: el Gateway donde se ejecuta Traefik.

- `nginx` necesita `ingress-nginx` en el clúster, y `gateway` necesita Traefik. Cada uno se rechaza, sin cambiar nada,
  cuando falta el controlador que nombra.
- `--ingress-controller nginx` y `--drop-legacy-ingress` juntos se rechazan: uno hace de los objetos Ingress el único
  front end y el otro deja de generarlos.
- Una vez quitado `ingress-nginx` ya no hay vuelta atrás a él, y una instalación no devuelve un controlador quitado.

## Qué cambia en el borde {#ingress-cutover-differences}

El Gateway sirve los mismos hosts, rutas y backends que servían los objetos Ingress. Lo que cambia es quién configura
el borde:

- **Las anotaciones dejan de aplicarse.** `ingress.className`, `ingress.annotations` e `ingress.uploadMaxBodySize` en
  una [superposición de valores](./tuning-an-instance.md) pertenecen a los objetos Ingress. En una instancia servida
  solo por el Gateway, un valor que escribas para ellos en una ejecución se **rechaza**, y uno que la instancia ya había
  registrado se omite del render con un aviso y se conserva en el registro, de modo que vuelve a aplicarse con
  `--ingress-controller nginx`.
- **Las cargas no tienen tope ni búfer.** Traefik transmite el cuerpo de la solicitud en flujo, y el servicio de
  actualizaciones aplica su propio límite. El tiempo de lectura de Traefik acota la **solicitud completa**, cuerpo
  incluido, así que se deriva del artefacto más grande que acepta el clúster: la variable de OpenTofu
  `traefik_max_artifact_bytes`, 512 MiB por defecto, da una ventana de 2.228 segundos. Súbela **antes** de subir el
  `updates.maxArtifactBytes` de una instancia. Esa misma ventana es el tiempo que un cliente que envía las cabeceras
  muy despacio puede mantener una conexión, frente a los 60 segundos de antes.
- **Las cadenas de consulta de las descargas quedan fuera del registro de accesos**, que está desactivado por defecto.
  La capacidad de descarga viaja en `?cap=`.
- **Las rutas distinguen mayúsculas y terminan en un límite de segmento.** `/API/<area>` y `/api/<area>X` caen en la
  consola en lugar de llegar al área.
- **Direcciones de cliente.** Traefik escribe el par real en `X-Forwarded-For` y su Service usa
  `externalTrafficPolicy: Local`; consulta [Direcciones de cliente y proxies de confianza](./bootstrap.md#trusted-proxies).
  En AWS un Classic Load Balancer oculta la dirección del cliente, y la variable de NLB que ofrece la instalación de
  dcctl cubre solo `ingress-nginx`.

## Relacionado {#ingress-cutover-related}

- [`dcctl install`](../reference/dcctl.md) y [`dcctl upgrade`](../reference/dcctl.md) listan todos los flags usados aquí.
- [Versiones y actualizaciones](./releases-and-upgrades.md#self-registration-upgrade) contiene las notas de la versión
  que incluye este cambio.
