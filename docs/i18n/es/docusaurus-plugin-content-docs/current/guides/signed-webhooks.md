---
sidebar_position: 7
title: Firma de Webhooks de Salida
---

# Firma de Webhooks de Salida

Un receptor de webhooks no puede responder por sí solo, solo con lo que ve en la red, a una pregunta: ¿esta solicitud viene realmente de DeviceChain y es la misma que se envió? La **firma** la responde. Una entrega firmada lleva tres encabezados, definidos por la especificación abierta [Standard Webhooks](https://www.standardwebhooks.com), de modo que el receptor puede verificarla con unas pocas líneas de código o con cualquier biblioteca que implemente la especificación.

La firma está **desactivada por defecto**. Un webhook que no has pedido firmar se entrega exactamente como antes, byte por byte. Puedes firmar dos tipos de entrega:

- un **canal webhook de notificaciones**, que avisa a una persona o a una herramienta de chat cuando se levanta una alarma (consulta [Configuración de Canales de Notificación](notification-channels.md));
- una **acción `httpCall`** de una regla de detección, que envía a un sistema un payload que tú das forma (consulta [Conectores de Salida](../concepts/outbound-connectors.md)).

El cuerpo de la solicitud es el mismo en ambos casos. La firma añade encabezados y no cambia nada más.

## Qué lleva una entrega firmada

| Encabezado | Valor |
| --- | --- |
| `webhook-id` | La identidad del mensaje. Se mantiene igual en los reintentos de un mismo mensaje, de modo que el receptor puede usarla para descartar un duplicado. |
| `webhook-timestamp` | Segundos Unix enteros, tomados de nuevo en cada intento. El receptor rechaza uno demasiado antiguo, lo que limita cuánto tiempo sirve una solicitud capturada. |
| `webhook-signature` | Una o más entradas separadas por espacios con la forma `v1,<HMAC-SHA256 en base64>`. |

La firma cubre `<webhook-id>.<webhook-timestamp>.<cuerpo sin procesar>`, con tu secreto de firma como clave. Si cambia un solo byte del cuerpo, del id o de la marca de tiempo, deja de coincidir.

En una acción `httpCall`, el `webhook-id` es la clave de idempotencia del despacho, el mismo valor que se envía en `X-DC-Idempotency-Key`. En un canal de notificaciones es un id `msg_…` nuevo por cada entrega, que se reutiliza cuando esa entrega se reintenta. Si el broker vuelve a entregar la alarma completa tras un reinicio, se produce un id nuevo, así que para deduplicar de extremo a extremo usa también los campos de la alarma que van en el payload.

## El secreto de firma

Un secreto de firma tiene la forma `whsec_` seguida de texto en base64. Lo genera la plataforma, se **muestra una sola vez** en la respuesta de la mutación que lo crea y se guarda sellado en el almacén cifrado de secretos de la plataforma. Después no se puede leer por ninguna API: las listas y consultas solo indican si existe uno. Cópialo en la configuración de tu receptor al crearlo.

Puedes tener un secreto por canal de notificaciones y uno por cada identificador de firma. Un **identificador** (handle) es un nombre que eliges para un secreto de `httpCall` (letras, dígitos y `- _ . /`, como máximo 256 caracteres); varias reglas pueden compartir uno.

## Firmar un canal webhook de notificaciones

Crea el canal como siempre, luego genera su secreto y después activa la firma en el `config` del canal. Requiere `notification:write`.

```graphql
mutation {
  generateNotificationChannelSigningSecret(token: "oncall-hook") {
    secret      # whsec_… — se muestra una sola vez
  }
}
```

```graphql
mutation {
  updateNotificationChannel(token: "oncall-hook", request: {
    config: "{\"url\":\"https://hooks.example.com/alarms\",\"auth\":\"bearer\",\"signing\":true}"
  }) { token hasSigningSecret }
}
```

La firma se combina con el modo `auth` del canal: un canal `bearer` sigue enviando su encabezado `Authorization`, y los encabezados de firma se añaden a él.

Genera el secreto **antes** de poner `"signing": true`. Un canal configurado para firmar que no tiene secreto no se envía sin firma: la entrega se rechaza en su primer intento y no se reintenta, y el servicio de notificaciones registra el inquilino y el token del canal. Se cuenta en `devicechain_notificationmanagement_deliveries_refused_total{reason="signing"}`, sobre el que puedes crear una alerta.

## Firmar una acción `httpCall`

Genera un secreto bajo un identificador y nombra ese identificador en el `signingSecretRef` de la acción. Requiere `connector:write`.

```graphql
mutation {
  generateWebhookSigningSecret(handle: "hooks/prod") {
    handle
    secret      # whsec_… — se muestra una sola vez
  }
}
```

En el formulario de reglas, la acción `httpCall` tiene un campo **Identificador del secreto de firma**, y el lienzo de automatización tiene el mismo campo en el nodo de acción. Por la API es el campo `signingSecretRef` de la acción:

```json
{
  "type": "httpCall",
  "httpCall": {
    "url": "https://hooks.example.com/devices",
    "bodyTemplate": "'{\"series\":\"' + series + '\"}'",
    "signingSecretRef": "hooks/prod"
  }
}
```

Una regla que nombra un identificador sin secreto almacenado tiene sus entregas enviadas a la cola de mensajes fallidos como inválidas en el primer intento; la llamada nunca se hace sin firma. Lo mismo ocurre si eliminas un secreto que una regla todavía nombra. El servicio de conectores lee los secretos a través de una caché de aproximadamente un minuto, así que un secreto creado, rotado o eliminado tarda hasta ese tiempo en llegar a todas las entregas.

Para ver si un identificador tiene secreto, y si hay una rotación abierta, sin leerlo:

```graphql
query { webhookSigningSecret(handle: "hooks/prod") { handle rotating } }
```

## Rotar un secreto

La rotación nunca pierde una entrega, porque durante un tiempo firman ambos secretos.

1. **Rotar.** Genera un secreto nuevo, lo devuelve una sola vez y conserva el anterior.

   ```graphql
   mutation { rotateNotificationChannelSigningSecret(token: "oncall-hook") { secret rotating } }
   # o, para un identificador de httpCall:
   mutation { rotateWebhookSigningSecret(handle: "hooks/prod") { secret rotating } }
   ```

   A partir de ese momento, cada entrega lleva **dos** entradas en `webhook-signature`, una con el secreto nuevo y otra con el anterior. Un receptor que tenga cualquiera de los dos secretos la acepta.

2. **Mueve el receptor** al secreto nuevo a tu ritmo. Un verificador que pueda tener dos secretos a la vez convierte esto en un no-evento.

3. **Termina la rotación.** El secreto anterior deja de firmar y las entregas vuelven a llevar una sola entrada.

   ```graphql
   mutation { endNotificationChannelSigningRotation(token: "oncall-hook") { token signingRotationOpen } }
   # o:
   mutation { endWebhookSigningRotation(handle: "hooks/prod") { handle rotating } }
   ```

Si rotas de nuevo mientras hay una rotación abierta, el secreto anterior se sustituye por el actual, de modo que un secreto de hace dos rotaciones deja de usarse. Para dejar de firmar definitivamente, quita primero `"signing": true` del canal (o el `signingSecretRef` de la regla) y luego elimina el secreto con `deleteNotificationChannelSigningSecret` o `deleteWebhookSigningSecret`. Eliminar un canal elimina también sus secretos.

## Verificar una entrega

Un receptor debe hacer cuatro cosas, en este orden:

1. Tomar el **cuerpo sin procesar** exactamente como llegó. Analizar el JSON y volver a serializarlo cambia los bytes y rompe la firma.
2. Comprobar que `webhook-id`, `webhook-timestamp` y `webhook-signature` están presentes, y que la marca de tiempo está a pocos minutos de tu reloj, en cualquiera de los dos sentidos. Cinco minutos es la tolerancia habitual.
3. Calcular `HMAC-SHA256` sobre `<id>.<marca de tiempo>.<cuerpo sin procesar>` con la clave del secreto (el texto en base64 que sigue a `whsec_`, decodificado), codificarlo en base64 y anteponer `v1,`.
4. Compararlo con **cada** entrada de `webhook-signature` separada por espacios, en tiempo constante, y aceptar si alguna coincide. Ignorar las entradas que no empiezan por `v1,`.

Los tres ejemplos siguientes hacen exactamente esto. Pasa todos los secretos que tengas en ese momento, que son dos durante una rotación.

### Go

```go
package main

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// Verify checks a delivery. body must be the raw bytes received, before any JSON decoding.
func Verify(h http.Header, body []byte, now time.Time, secrets ...string) error {
	id := h.Get("webhook-id")
	ts := h.Get("webhook-timestamp")
	sigs := h.Get("webhook-signature")
	if id == "" || ts == "" || sigs == "" {
		return errors.New("missing webhook headers")
	}
	sec, err := strconv.ParseInt(ts, 10, 64)
	if err != nil {
		return errors.New("malformed webhook-timestamp")
	}
	// Whole seconds, not a time.Duration: a timestamp centuries away would saturate a Duration.
	if sec > now.Unix()+300 || sec < now.Unix()-300 {
		return errors.New("timestamp outside the tolerance window")
	}
	for _, secret := range secrets {
		key, err := base64.StdEncoding.DecodeString(strings.TrimPrefix(secret, "whsec_"))
		if err != nil {
			return err
		}
		mac := hmac.New(sha256.New, key)
		mac.Write([]byte(id + "." + ts + "."))
		mac.Write(body)
		want := "v1," + base64.StdEncoding.EncodeToString(mac.Sum(nil))
		for _, s := range strings.Fields(sigs) {
			if hmac.Equal([]byte(s), []byte(want)) {
				return nil
			}
		}
	}
	return errors.New("no valid signature")
}
```

Llámala con el cuerpo leído directamente de la solicitud, por ejemplo `body, _ := io.ReadAll(r.Body)` seguido de `Verify(r.Header, body, time.Now(), secret)`.

### Node

```js
const crypto = require('node:crypto');

// rawBody must be the Buffer or string exactly as received, before JSON.parse.
function verify(headers, rawBody, secrets, now = Date.now()) {
  const id = headers['webhook-id'];
  const ts = headers['webhook-timestamp'];
  const sigs = headers['webhook-signature'];
  if (!id || !ts || !sigs) throw new Error('missing webhook headers');
  if (!/^\d+$/.test(ts)) throw new Error('malformed webhook-timestamp');
  if (Math.abs(now / 1000 - Number(ts)) > 300) throw new Error('timestamp outside the tolerance window');

  for (const secret of secrets) {
    const key = Buffer.from(secret.replace(/^whsec_/, ''), 'base64');
    const want = 'v1,' + crypto.createHmac('sha256', key).update(`${id}.${ts}.`).update(rawBody).digest('base64');
    for (const sig of sigs.split(' ')) {
      if (sig.length === want.length && crypto.timingSafeEqual(Buffer.from(sig), Buffer.from(want))) return;
    }
  }
  throw new Error('no valid signature');
}
```

En Express, captura el cuerpo sin procesar con `express.raw({ type: 'application/json' })` en esta ruta, llama a `verify(req.headers, req.body, secrets)` y solo después haz `JSON.parse`. Node pasa los nombres de encabezado a minúsculas, que es lo que espera el código.

### Python

```python
import base64
import hashlib
import hmac
import time


def verify(headers, raw_body, secrets, tolerance=300, now=None):
    """Raise ValueError unless the delivery is authentic and fresh.

    raw_body is the bytes exactly as received, before any JSON decoding.
    """
    msg_id = headers.get("webhook-id")
    ts = headers.get("webhook-timestamp")
    sigs = headers.get("webhook-signature")
    if not (msg_id and ts and sigs):
        raise ValueError("missing webhook headers")
    try:
        sent = int(ts)
    except ValueError:
        raise ValueError("malformed webhook-timestamp")
    if abs((time.time() if now is None else now) - sent) > tolerance:
        raise ValueError("timestamp outside the tolerance window")

    for secret in secrets:
        key = base64.b64decode(secret.removeprefix("whsec_"))
        digest = hmac.new(key, f"{msg_id}.{ts}.".encode() + raw_body, hashlib.sha256).digest()
        want = "v1," + base64.b64encode(digest).decode()
        for sig in sigs.split():
            if hmac.compare_digest(sig, want):
                return
    raise ValueError("no valid signature")
```

En Flask usa `request.get_data()` para `raw_body` y `request.headers` para `headers`; en FastAPI usa `await request.body()`. `removeprefix` requiere Python 3.9 o posterior.

### Comprueba tu verificador

La especificación publica un vector de prueba. Un verificador correcto lo acepta en el momento en que se firmó:

| Campo | Valor |
| --- | --- |
| secreto | `whsec_MfKQ9r8GKYqrTwjUPD8ILPZIo2LaLaSw` |
| `webhook-id` | `msg_p5jXN8AQM9LWM0D4loKWxJek` |
| `webhook-timestamp` | `1614265330` |
| cuerpo | `{"test": 2432232314}` |
| `webhook-signature` | `v1,g0hM9SsE+OTPJTGt/tmIKtSyZlE3uFJELVlNIOLJ1OE=` |

Debe rechazar la misma solicitud con un byte del cuerpo cambiado, y con el reloj una hora más tarde.

## Activar la firma durante una actualización

Despliega la actualización en todas las réplicas de `outbound-connectors` y `notification-management` antes de activar la firma. Una réplica anterior a la firma ignora `signingSecretRef` y `"signing": true`, así que mientras conviven réplicas antiguas y nuevas, una entrega que llegue a una antigua sale **sin firmar**, y una réplica antigua de `outbound-connectors` no sabe que los secretos de firma nunca deben usarse como credencial Bearer. Hasta que el despliegue termine, no pongas `"signing": true` en un canal, no añadas `signingSecretRef` a una regla y no publiques una regla cuyo `secretRef` Bearer empiece por `webhook-signing/`. Activa la firma después.

## Comportamiento ante fallos

| Situación | Qué ocurre |
| --- | --- |
| La firma no está activada | La entrega no cambia, sin encabezados `webhook-*`. |
| La firma está activada y el secreto existe | La entrega lleva los tres encabezados. |
| La firma está activada pero no hay secreto almacenado, o el valor almacenado no es un secreto válido | La entrega se rechaza y **no se reintenta**. No se envía nada. Un canal lo cuenta como `reason="signing"`; un `httpCall` va a la cola de mensajes fallidos como inválido. |
| El almacén de secretos no está disponible por un momento | La entrega se reintenta, como cualquier fallo transitorio. |
| Un encabezado que configuraste se llama `webhook-id`, `webhook-timestamp` o `webhook-signature` | En una entrega firmada, el valor de la plataforma lo sustituye. |
| El `authHeader` de un canal firmado es `webhook-id`, `webhook-timestamp` o `webhook-signature` | El canal se rechaza al guardarlo, y una entrega se rechaza sin reintento, porque la firma sobrescribiría tu credencial. Usa otro nombre de encabezado para la credencial. |
