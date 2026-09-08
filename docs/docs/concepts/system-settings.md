---
title: System Settings
description: The instance-wide settings an operator edits, what each one accepts, and the bounds every settings write is subject to.
---

# System Settings

A **system setting** is one instance-wide value an operator sets once, for every tenant. There are
four of them, they live under **Settings** in the admin console, and each sits *below* whatever a
tenant configures for itself — a tenant that sets nothing gets the instance default, and a tenant
that sets its own value never sees it.

| Key | What it decides | Covered in |
| --- | --- | --- |
| `basemap.default` | The map tiles every tenant starts with | [Basemaps](./basemaps.md) |
| `branding.default` | The instance's title, logo and palette | [White-labeling](./white-labeling.md) |
| `entity.token_masks` | The shape of every token the console mints | below |
| `locale.default` | The language the console opens in | below |

Reading a setting needs no special authority beyond being signed in. Writing one requires
`settings:write`, which is an operator-level authority and not part of any tenant role.

## What every settings write is subject to {#settings-write-rules}

Three rules apply to all four keys, in this order:

1. **The value must be under 64 KB.** Over that, the write is refused with the byte count. This
   bounds the whole JSON document, not any one field inside it — which matters most for
   `branding.default`, where an inline `data:` logo could otherwise be far larger. The
   [branding record](./white-labeling.md) allows a 256 KB inline logo *on a tenant*, where it is
   stored as a typed column rather than as a setting; at the instance tier the 64 KB document
   bound applies instead, which works out to roughly 48 KB of image. The console steers you to an
   `https` URL at this tier for exactly that reason.
2. **The value must be valid JSON.**
3. **The key must be one of the four above.** The vocabulary is closed: writing an unrecognised
   key is refused rather than creating a setting. There is no way to add one from the API.

Each key then applies its own validation, which the pages linked in the table describe.

## Token masks {#token-masks}

`entity.token_masks` decides the token every console **create** form pre-fills. Every entity is
addressed by a token, and typing one by hand for each new device is both tedious and easy to get
wrong, so the console generates one from a template and lets you edit it before saving.

The setting is a map of entity type to template. The key `default` applies to any entity type with
no entry of its own:

```json
{
  "default": "{slug}",
  "device": "dev-{alphanumeric-8}",
  "area": "area-{slug}"
}
```

A template is literal text plus placeholders:

| Placeholder | Produces |
| --- | --- |
| `{slug}` | A slug of the name being typed — so naming a device "Cold Store Probe" suggests `cold-store-probe` |
| `{uuid}` | A UUID |
| `{alphanumeric-N}` | `N` random letters and digits |
| `{numeric-N}` | `N` random digits |

The shipped default is `{"default": "{slug}"}`.

Whatever a mask produces still has to satisfy the [token grammar](../reference/graphql-api.md#what-a-token-may-contain),
which is what makes some templates impossible. A mask is refused if it:

- is **empty**
- uses an **unknown placeholder** — `dev-{sulg}` would silently generate `dev-` for every entity,
  because an unrecognised placeholder produces nothing
- has **no placeholder at all** — every entity would be handed the identical token, so the first
  create succeeds and every one after it collides
- declares a **width larger than 128 characters**, which could never mint a valid token
- generates a **sample that fails the token grammar** — `my.device-{slug}` is refused for the dot,
  before any entity is created with it

The last one is the point of validating here rather than at create time: an operator who saves a
bad mask would otherwise not learn about it, and every console user who hit a create form would.

:::note This shapes suggestions, not rules
A mask decides what the console *offers*. A token typed by hand, or sent by an integration over the
API, is subject only to the token grammar — masks are not enforced on the write path, and changing
one does not affect entities that already exist.
:::

## Default language {#locale-default}

`locale.default` decides the language the console opens in, for people who have not picked one for
themselves. Its value is a [BCP-47](https://www.rfc-editor.org/info/bcp47) language tag in a JSON
string — `"en"`, `"es"`, `"pt-BR"` — or **`null`**, which is what it ships as.

`null` is not "unset". It is the value that means *no instance-wide default: let each viewer's
browser decide*, and it is the reason the shipped console still follows a Spanish browser out of
the box. Setting a tag here overrides that for everyone who has not chosen; clearing the field in
the console stores `null` again.

It is the bottom of four tiers, and the order is worth knowing before you set it, because this
setting is the only one of the four whose effect a *user* can override:

1. a language the person picked from the switcher, which nothing here changes
2. the tenant's own default, set under **Settings → Language** by a tenant admin
3. the languages the viewer's browser asks for
4. English

So a tag here moves only the people in tiers 3 and 4 — and if you set one, colleagues who have
already used the switcher will not see it change. That is deliberate, and it is the usual reason a
change here "does not work".

The tag is checked for **shape**, not for whether this build ships that language: an unknown but
well-formed tag is stored and simply has no effect until its catalog exists. The console warns you
when you type one. A tag must be stored in canonical form (`es-MX`, not `es-mx`), and a blank
string is refused — use `null`.

A regional tag falls back to its base language, so `es-MX` renders Spanish on a build that ships
only `es`.
