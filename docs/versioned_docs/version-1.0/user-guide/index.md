---
title: User guide
---

# User guide

This guide walks through the DeviceChain web console, screen by screen, in the order the
console's own navigation presents them. Each chapter explains what a screen is for, when you
would use it, and what to expect, and is illustrated with screenshots of the console as shipped.

This is the guide for working **in the console**. For tasks done through the API or on the
device side, such as connecting a device or sending a command from code, see the
[Guides](/category/guides).

The guide is written for two audiences:

- **Tenant users and administrators**, who work with the devices, rules, alarms and dashboards
  of one tenant. Most chapters are for you.
- **Instance operators**, who administer the whole installation: tenants, tiers, identities and
  identity providers. Those screens are gathered in their own chapter.

New to DeviceChain? Start with [Your First Device](../quickstart/first-device.md) to get data
flowing, then come back here to learn the console. For how the pieces fit together, see the
[Concepts](../concepts/architecture.md) section.

## Chapters

Chapters are published as they are completed.

| # | Chapter | What it covers |
|---|---|---|
| 1 | [Signing in](./signing-in.md) | The sign-in steps, single sign-on, choosing a tenant, an expired session, and the user menu (theme, language, profile). |
| 2 | [The console at a glance](./console-at-a-glance.md) | The home page, the sidebar, the tenant chip, and what you see when an area is unavailable. |
| 3 | [Dashboards](./dashboards.md) | Templates and import, viewing, editing on the canvas, widgets and bindings, export, and version history. |
| 4 | [Viewing dashboards in /dash](./viewing-dashboards-in-dash.md) | The standalone dashboard viewer: signing in, loading a dashboard by token, kiosk mode for wall displays, and staying signed in. |
| 5 | [Alarms](./alarms.md) | The live alarm list, filters, acknowledging and clearing, and exporting the list as CSV. |
| 6 | [Notification policies](./notification-policies.md) | The policy list, and giving a policy quiet hours: time zone, weekly slots and a bypass severity. |
| 7 | [Devices](./devices.md) | Listing, creating and bulk-creating devices, importing devices from a CSV file, and exporting the list as CSV; disabling and enabling a device; the device detail tabs, including assignment, connectivity and location, events, commands, configuration (with restoring an earlier revision or copying another device's), credentials and replacement. |
| 8 | [Device types and profiles](./device-types-and-profiles.md) | Device types and versioned device profiles: metrics, commands, configuration, publishing or rolling back a version, and downloading a version as a Thing Model. |
| 9 | [Calculated metrics](./calculated-metrics.md) | Metrics computed from a formula on a device profile: authoring one, the formula language, what happens when a formula produces no value, backfilling history, and turning the feature on. |
| 10 | [Detection rules](./detection-rules.md) | Authoring rules as a form, on the canvas, or in plain language; replay preview; rule health and the live feed. |
| 11 | [Commanding a fleet](./commanding-a-fleet.md) | Command batches: creating one, following its progress, refusals and cancellation. |
| 12 | [Updates (over the air)](./updates.md) | Artifacts: uploading an update image, its trust and signatures, and retiring it; registering and revoking signers; and the device Updates tab: assigning, cancelling and abandoning an update, and what a disabled device means for updates. |
| 13 | [Provisioning and trust anchors](./provisioning-and-trust-anchors.md) | Provisioning profiles for fleets that register themselves, expecting a device, trust anchors, certificate credentials and certificate profiles, and what works today with certificates. |
| 14 | [Inbound integrations](./inbound-integrations.md) | Receiving device data from ChirpStack, The Things Stack or a generic HTTP sender: creating an integration, connecting the sender, device aliases, unregistered devices and traffic. |
| 15 | [Connectors](./connectors.md) | Outbound connectors: creating, configuring and versioning them. |
| 16 | [Assets, customers and areas](./assets-customers-areas.md) | The registry families: lists, hierarchy, property schemas and members. |
| 17 | [Geofences](./geofences.md) | Drawing and editing fences, and their history. |
| 18 | [Groups, facets and Browse](./groups-facets-browse.md) | Facet keys, the Browse query builder, and saving a result as a dynamic group. |
| 19 | [Members, roles and customer users](./members-roles-and-customer-users.md) | Inviting members and changing their access, the tenant's own roles, and logins limited to your customers: what they see, and sharing dashboards with them. |
| 20 | [Service accounts](./service-accounts.md) | Machine identities for scripts and integrations: what an account can do, creating one, obtaining a token, rotating a secret, and disabling or deleting it. |
| 21 | [Audit](./audit.md) | The tenant audit journal, filters and export. |
| 22 | [Tenant settings](./tenant-settings.md) | Branding, map and language. |
| 23 | [Administering the instance](./administering-the-instance.md) | Tenants, deletions, tiers, identities, roles, identity providers, AI providers, instance audit and settings. |
