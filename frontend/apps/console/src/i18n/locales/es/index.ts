// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// The Spanish catalogs as ONE module, so the bundler emits them as one on-demand chunk (see
// loadCatalog in ../config.ts) instead of folding ~70 kB gzip into the first download.

import esCommon from './common.json';
import esLogin from './login.json';
import esNav from './nav.json';
import esUserMenu from './userMenu.json';
import esTheme from './theme.json';
import esDevices from './devices.json';
import esAlarms from './alarms.json';
import esDashboards from './dashboards.json';
import esDeviceProfiles from './deviceProfiles.json';
import esTenants from './tenants.json';
import esTiers from './tiers.json';
import esConnectors from './connectors.json';
import esCommandBatches from './commandBatches.json';
import esUpdateSigners from './updateSigners.json';
import esBranding from './branding.json';
import esBasemap from './basemap.json';
import esLocale from './locale.json';
import esBrowse from './browse.json';
import esFacets from './facets.json';
import esAudit from './audit.json';
import esIdentities from './identities.json';
import esRoles from './roles.json';
import esMembers from './members.json';
import esAiProviders from './aiProviders.json';
import esIdentityProviders from './identityProviders.json';
import esAiPackaging from './aiPackaging.json';
import esAdminSettings from './adminSettings.json';
import esAdminAudit from './adminAudit.json';
import esEntities from './entities.json';
import esVersions from './versions.json';
import esUpdates from './updates.json';

const es = {
    common: esCommon,
    login: esLogin,
    nav: esNav,
    userMenu: esUserMenu,
    theme: esTheme,
    devices: esDevices,
    alarms: esAlarms,
    dashboards: esDashboards,
    deviceProfiles: esDeviceProfiles,
    tenants: esTenants,
    tiers: esTiers,
    connectors: esConnectors,
    commandBatches: esCommandBatches,
    updateSigners: esUpdateSigners,
    branding: esBranding,
    basemap: esBasemap,
    locale: esLocale,
    browse: esBrowse,
    facets: esFacets,
    audit: esAudit,
    identities: esIdentities,
    roles: esRoles,
    aiProviders: esAiProviders,
    identityProviders: esIdentityProviders,
    aiPackaging: esAiPackaging,
    adminSettings: esAdminSettings,
    adminAudit: esAdminAudit,
    entities: esEntities,
    versions: esVersions,
    updates: esUpdates,
    members: esMembers,
} as const;

export default es;
