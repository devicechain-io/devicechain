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
  lee [Certificados de un emisor ACME](#ingress-cutover-acme): necesita un cambio una vez movido el DNS, no antes.
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

   Después la actualización comprueba el certificado del Gateway. Consulta [La compuerta del certificado](#ingress-cutover-cert-gate).

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

5. **Comprueba el certificado del Gateway.** En una instancia que termina TLS con su propia CA (la
   configuración autofirmada por defecto), confirma que el certificado del Gateway lo emitió la CA de la
   instancia antes de quitar los objetos Ingress. Una instancia actualizada a través del cambio de emisor
   puede quedar con un certificado del Gateway emitido por el emisor autofirmado anterior: tiene el sujeto
   vacío y no es una hoja firmada por una CA, así que un cliente basado en OpenSSL no tiene nada con lo que
   anclarlo, aunque un navegador que acepta excepciones no muestra la diferencia.

   ```bash
   kubectl -n dci-<instance> get secret devicechain-tls-gateway -o jsonpath='{.data.tls\.crt}' \
     | base64 -d | openssl x509 -noout -subject -issuer
   ```

   El sujeto no debe estar vacío y el emisor debe ser la CA de la instancia, `CN=<instance> DeviceChain CA`.
   Si no lo es, pide a cert-manager que lo emita de nuevo desde la CA de la instancia y vuelve a comprobar:

   ```bash
   cmctl renew -n dci-<instance> devicechain-tls-gateway
   # sin cmctl: borra el Secret y cert-manager lo vuelve a crear
   kubectl -n dci-<instance> delete secret devicechain-tls-gateway
   ```

   Una instancia cuyo certificado procede de un `ClusterIssuer` ACME u otro no tiene nada que hacer aquí.

6. **Quita los objetos Ingress.** Cuando el tráfico se haya movido, deja de generarlos:

   ```bash
   dcctl upgrade <provider> <instance> --drop-legacy-ingress
   ```

   Hazlo para todas las instancias del clúster.

7. **Quita `ingress-nginx`.**

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

Un clúster local (kind) publica los puertos 80 y 443 de un nodo, y solo un controlador puede ocuparlos. Mientras los dos controladores estén instalados, `ingress-nginx` conserva los puertos y Traefik solo es accesible en un NodePort.

En un clúster local no hay DNS que mover: el navegador llega al controlador que ocupe los puertos del nodo. Los pasos 3 y 4 (la dirección externa y el DNS) no se aplican, y un `dcctl upgrade` normal ya lleva a la ventana a una instancia que tiene objetos Ingress. Ejecuta los pasos 6 y 7 seguidos: el host no responde desde `--drop-legacy-ingress` hasta que la eliminación haya entregado los puertos a Traefik, y espera un corte breve mientras ocurre.

Un clúster local **nuevo** no tiene `ingress-nginx`, y Traefik ocupa los puertos desde el principio. En un clúster local que tiene ambos, una instancia **nueva** la sirve `ingress-nginx` hasta la eliminación.

### Varias instancias {#ingress-cutover-several}

El paso del clúster es compartido y cada instancia se mueve a su propio ritmo. `ingress-nginx` se queda hasta que la
última instancia haya ejecutado `--drop-legacy-ingress`. Si las instancias de un clúster usan hosts distintos, hay que
mover el DNS de cada uno.

## La compuerta del certificado {#ingress-cutover-cert-gate}

Un Gateway sin certificado responde `https` con el certificado por defecto del controlador, así que el paso comprueba el certificado del Gateway en cuatro momentos. Las comprobaciones se aplican cuando la instancia termina TLS y el clúster tiene cert-manager; `--no-tls` las omite. Un Gateway cuyo Secret aportas tú (no lleva anotación de emisor de cert-manager ni `kubernetes.io/tls-acme: "true"`) no tiene nada que esperar y las supera.

- **Durante la ventana,** `dcctl upgrade` espera 30 segundos al certificado y después **avisa y continúa**. Los objetos Ingress siguen sirviendo el host, y muchos emisores solo pueden emitir el certificado del Gateway cuando el DNS apunta a Traefik.
- **`dcctl upgrade --drop-legacy-ingress`** se niega, antes de generar o cambiar nada, salvo que el Certificate de cert-manager del Gateway esté `Ready` y lo controle el Gateway. También se niega cuando la instancia aún no tiene Gateway. Mueve antes el DNS para que se pueda emitir el certificado, comprueba `kubectl -n dci-<instance> get certificate` y ejecuta de nuevo el comando.
- **`dcctl install --remove-legacy-ingress`** comprueba el Gateway de cada instancia del clúster y nombra las que no tienen el certificado `Ready`. Quitar `ingress-nginx` no cambia cómo se sirven esas instancias; la comprobación evita que un certificado roto del Gateway pase inadvertido hasta que nada más sirva el host.
- **Una actualización que deja una instancia solo en el Gateway** espera hasta cinco minutos y falla indicando el motivo. Los servicios ya están en marcha cuando falla.

Un rechazo termina con "Nothing has been changed". Arregla lo que nombra y **ejecuta de nuevo el mismo comando**. Las causas habituales:

- **Existe un Certificate para el Secret, pero el Gateway no lo controla.** cert-manager no adopta un Certificate que no creó para el Gateway, así que uno sobrante de un Ingress, o hecho a mano, con el mismo nombre de Secret deja al Gateway sin ninguno. El mensaje imprime el comando que lo elimina (`kubectl -n dci-<instance> delete certificate <name>`); entonces se crea el del propio Gateway.
- **No se crea ningún Certificate.** El soporte de Gateway API de cert-manager no está activado, o cert-manager arrancó antes de que existieran las definiciones de la Gateway API. `kubectl -n cert-manager logs deploy/cert-manager` lo indica. Un `dcctl install` de esta versión lo activa.
- **El Certificate no está `Ready`.** El mensaje incluye el texto del propio emisor. Con un emisor ACME, véase más abajo.

## Certificados de un emisor ACME {#ingress-cutover-acme}

Un emisor HTTP-01 que resuelve a través de la clase de ingress `nginx` sigue funcionando durante la ventana, también para el certificado del Gateway: el desafío lo sirve `ingress-nginx`, adonde el DNS sigue apuntando. Déjalo como está hasta que el DNS se haya movido. Después, antes de `--drop-legacy-ingress` y `--remove-legacy-ingress`, dale un solver `gatewayHTTPRoute`, o sus renovaciones fallarán cuando `ingress-nginx` ya no esté. Un solver `gatewayHTTPRoute` fijado antes de mover el DNS falla, porque el desafío llega a `ingress-nginx`, que no tiene ruta para él. Un solver DNS-01 funciona en todo momento.

El solver `gatewayHTTPRoute` nombra el listener `http` del Gateway de la instancia, `devicechain`:

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
                sectionName: http
```

El `parentRef` no lleva namespace, y uno solo cubre todas las instancias: cert-manager crea la ruta del solver en el namespace del Certificate que se está emitiendo, así que se asocia al Gateway `devicechain` de esa instancia. La redirección de `http` a `https` del chart es una coincidencia menos específica que la ruta del desafío, así que no lo intercepta.

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
- Si el DNS ya apunta a Traefik, vuelve a apuntarlo a la dirección de `ingress-nginx`: el retroceso elimina el Gateway.
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
  En AWS un Classic Load Balancer oculta la dirección del cliente. `dcctl install --aws-nlb traefik` da al Service de Traefik un Network Load Balancer, y es el valor que conviene durante la ventana porque `ingress-nginx` sigue sirviendo con su propio balanceador. Cambiar un Service sustituye su balanceador y cambia su nombre de host, así que repunta el DNS después.

## Relacionado {#ingress-cutover-related}

- [`dcctl install`](../reference/dcctl.md) y [`dcctl upgrade`](../reference/dcctl.md) listan todos los flags usados aquí.
- [Versiones y actualizaciones](./releases-and-upgrades.md#v0260-upgrade) contiene las notas de la versión
  que incluye este cambio.
