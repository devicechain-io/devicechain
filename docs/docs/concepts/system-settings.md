---
title: System Settings
description: The instance-wide settings an operator edits, what each one accepts, and the bounds every settings write is subject to.
---

# System Settings

A **system setting** is one instance-wide value that an operator sets once, for every tenant. There
are four of them, and they live under **Settings** in the admin console. Each one sits *below*
whatever a tenant configures for itself: a tenant that sets nothing gets the instance default, and a
tenant that sets its own value never sees it.

| Key | What it decides | Covered in |
| --- | --- | --- |
| `basemap.default` | The map tiles every tenant starts with | [Basemaps](./basemaps.md) |
| `branding.default` | The instance's title, logo and palette | [White-labeling](./white-labeling.md) |
| `entity.token_masks` | The shape of every token the console mints | below |
| `locale.default` | The language the console opens in | below |

Reading a setting requires `settings:read`, and writing one requires `settings:write`. Both are
operator-level authorities, and neither is part of any tenant role. Any signed-in user can read two
things without either authority:

- the `tokenMasks` query, which serves only the effective token-mask map, so that every console
  create form can mint a token
- a tenant's *effective* branding, basemap and language, which the tenant object exposes already
  folded over the instance default

## What every settings write is subject to {#settings-write-rules}

Three rules apply to all four keys, in this order:

1. **The key must be one of the four above.** The vocabulary is closed. Writing an unrecognised key
   is refused rather than creating a setting, and it is refused before the value is even looked at.
   There is no way to add a key from the API.
2. **The value must be at most 64 KB.** Over that, the write is refused with an error that names
   the limit in bytes (65,536). The limit bounds the whole JSON document, not any one field inside
   it. This matters most for `branding.default`, where an inline `data:` logo could otherwise be far larger:
   - On a tenant, the [branding record](./white-labeling.md) allows a 256 KB inline logo, because
     it is stored as a typed column rather than as a setting.
   - At the instance tier, the 64 KB document bound applies instead, which works out to roughly
     48 KB of image. The console's logo field at this tier asks for an `https` URL and offers no
     upload, and with this bound a URL is the practical choice.
3. **The value must be valid JSON.**

Each key then applies its own validation, which the pages linked in the table describe.

## Token masks {#token-masks}

`entity.token_masks` decides the token that every console create form pre-fills. Every entity is
addressed by a token. Typing one by hand for each new device is tedious and easy to get wrong, so
the console generates one from a template and lets you edit it before saving.

The setting is a map of entity type to template. The key `default` applies to any entity type that
has no entry of its own:

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

Whatever a mask produces must still satisfy the [token grammar](../reference/graphql-api.md#what-a-token-may-contain),
and that makes some templates impossible. A mask is refused if it:

- is empty
- uses an unknown placeholder. `dev-{sulg}` would silently generate `dev-` for every entity,
  because an unrecognised placeholder produces nothing.
- has no placeholder at all. Every entity would get the identical token, so the first create
  succeeds and every one after it collides.
- declares a width larger than 128 characters, which could never mint a valid token
- generates a sample that fails the token grammar. `my.device-{slug}` is refused for the dot,
  before any entity is created with it.

That last check is why masks are validated when you save them rather than at create time.
Otherwise the operator who saved a bad mask would not learn about it, and every console user who
opened a create form would.

:::note This shapes suggestions, not rules
A mask decides what the console *offers*. A token typed by hand, or sent by an integration over the
API, is subject only to the token grammar. Masks are not enforced on the write path, and changing
one does not affect entities that already exist.
:::

## Default language {#locale-default}

`locale.default` decides the language the console opens in, for people who have not picked one for
themselves. Its value is a [BCP-47](https://www.rfc-editor.org/info/bcp47) language tag in a JSON
string (`"en"`, `"es"`, `"pt-BR"`), or `null`, which is what it ships as.

`null` is not "unset". It means *no instance-wide default: let each viewer's browser decide*. That
is why the shipped console still follows a Spanish browser out of the box. Setting a tag here
overrides the browser for everyone who has not chosen a language, unless their tenant sets a
default of its own. Clearing the field in the console
stores `null` again.

The console picks a language from four tiers. Know the order before you set this, because it is the
only one of the four settings whose effect a *user* can override:

1. a language the person picked from the switcher, which nothing here changes
2. the tenant's own default, set under **Settings → Language** by a tenant admin. For a tenant
   that has not set one, this setting fills in.
3. the languages the viewer's browser asks for
4. English

A tag here therefore moves only the people who would otherwise land in tiers 3 and 4: those who
have not picked a language, in a tenant with no default of its own. If you set one, colleagues who have
already used the switcher will not see their language change. That is deliberate, and it is the
usual reason a change here "does not work".

The tag is checked for shape, not for whether this build ships that language:

- An unknown but well-formed tag is stored, and has no effect until its catalog exists. The console
  warns you when you type one.
- A tag must be stored in canonical form (`es-MX`, not `es-mx`).
- A blank string is refused. Use `null` instead.

A regional tag falls back to its base language, so `es-MX` renders Spanish on a build that ships
only `es`.
