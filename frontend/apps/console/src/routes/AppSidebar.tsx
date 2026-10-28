// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

import { useState, type ReactNode } from 'react';
import { Link, useLocation, useNavigate } from 'react-router-dom';
import { useTranslation } from 'react-i18next';
import {
  AlertTriangle,
  BellRing,
  Boxes,
  Building2,
  ChevronRight,
  CalendarClock,
  Cpu,
  FileArchive,
  Filter,
  House,
  Languages,
  Layers,
  LayoutDashboard,
  Map,
  MapPin,
  Pentagon,
  Package,
  Palette,
  ScrollText,
  Send,
  Rocket,
  ShieldCheck,
  SlidersHorizontal,
  Tags,
  UploadCloud,
  UserCog,
  Users,
  Webhook,
  type LucideIcon,
  KeyRound,
  LockKeyhole,
  PlugZap,
} from 'lucide-react';
import { cn } from '@/lib/utils';
import { Logomark, LogoHorizontal } from '@/components/brand/Logo';
import {
  Sidebar,
  SidebarContent,
  SidebarFooter,
  SidebarGroup,
  SidebarGroupContent,
  SidebarHeader,
  SidebarMenu,
  SidebarMenuButton,
  SidebarMenuItem,
  SidebarMenuSub,
  SidebarMenuSubButton,
  SidebarMenuSubItem,
  SidebarRail,
  useSidebar,
} from '@/components/ui/sidebar';
import { useAuth } from '@/auth/AuthProvider';
import { useCurrentTenant } from '@/auth/TenantProvider';
import { useBrandingLogoSrc } from '@/lib/useBrandingLogo';
import { hasAuthority, isScoped, type DecodedClaims } from '@devicechain/client';
import { NavUser } from '@/routes/NavUser';
import { useUpdateAccess } from '@/lib/updateAuthority';
import { readNavState, writeNavState, type NavOpenState } from '@/lib/navState';

interface NavLeaf {
  // A key into the `nav` catalog (ADR-066), resolved with t() at render — never a
  // literal display string, so the sidebar localizes.
  labelKey: string;
  href: string;
  icon: LucideIcon;
  // A second, muted line under the label (a `nav` catalog key). For an entry whose label
  // alone does not say what is inside it.
  hintKey?: string;
  // Authority required to see this item; omit for always-visible (e.g. Home).
  requires?: string;
  // Names a gate beyond authority. 'updates' hides the item unless the update-management
  // area is KNOWN to be deployed and the caller may read it (see useUpdateAccess).
  gate?: 'updates';
  // Shown to a member limited to part of the tenant (a customer user). Everything else is
  // hidden from one, because the server refuses it to them whatever their role says. The
  // opt-in mirrors the server's: a new leaf is hidden from scoped members until someone
  // marks it, rather than shown until someone remembers to hide it. The route guard
  // (lib/scope.ts) applies the same rule to the route table.
  scoped?: true;
}

// A construct's own sub-list (Assets: instances / types / groups). Nested one level inside a section.
type NavGroupNode = { labelKey: string; icon: LucideIcon; children: NavLeaf[] };

type NavItem = NavLeaf | NavGroupNode;

// A top-level collapsible section. Sections are the console's information architecture: what the
// user is doing (watching, managing devices, wiring things up, organizing, granting access,
// configuring), not which service happens to serve it. Adding a screen is one entry in one section.
export interface NavSection {
  // Opaque identity for the remembered expand/collapse state. Not display text, and distinct from
  // labelKey so a label can be renamed without forgetting what users chose.
  id: string;
  labelKey: string;
  icon: LucideIcon;
  items: NavItem[];
}

// A top-level nav node is either a direct link (Home) or a collapsible section.
type NavNode = NavLeaf | NavSection;

export const NAV: NavNode[] = [
  { labelKey: 'home', href: '/', icon: House, scoped: true },
  {
    id: 'monitor',
    labelKey: 'monitor',
    icon: LayoutDashboard,
    items: [
      { labelKey: 'dashboards', href: '/dashboards', icon: LayoutDashboard, requires: 'dashboard:read', scoped: true },
      { labelKey: 'alarms', href: '/alarms', icon: AlertTriangle, requires: 'alarm:read', scoped: true },
      // Who gets paged for an alarm, and when they are left alone. notification-management has its
      // own authority pair, separate from alarm:read.
      { labelKey: 'notificationPolicies', href: '/notification-policies', icon: BellRing, requires: 'notification:read' },
    ],
  },
  {
    id: 'devices',
    labelKey: 'devices',
    icon: Cpu,
    items: [
      // All of device-management is gated by device:read (there is no separate
      // devicetype:read), so these share the same requirement.
      { labelKey: 'devices', href: '/devices', icon: Cpu, requires: 'device:read', scoped: true },
      { labelKey: 'deviceTypes', href: '/device-types', icon: Boxes, requires: 'device:read' },
      // Detection rules, calculated metrics and commands exist only as tabs INSIDE a profile, so
      // the entry says so: nobody looks for a rule editor under "Device Profiles".
      {
        labelKey: 'deviceProfiles',
        href: '/device-profiles',
        icon: SlidersHorizontal,
        requires: 'device:read',
        hintKey: 'deviceProfilesHint',
      },
      { labelKey: 'deviceGroups', href: '/device-groups', icon: Layers, requires: 'device:read' },
      // A command batch is a fleet WRITE — one command fanned out to many devices —
      // so it belongs under the construct it acts on rather than in a group of one.
      // command:read is command-delivery's own authority, not device:read: an
      // identity can be allowed to read the registry without being allowed to see
      // what has been actuated across it.
      { labelKey: 'commandBatches', href: '/command-batches', icon: Send, requires: 'command:read' },
      // A maintenance window is configuration OF device types (a type names the window
      // that holds its disruptive commands), so it lives beside them.
      { labelKey: 'maintenanceWindows', href: '/maintenance-windows', icon: CalendarClock, requires: 'device:read' },
      // Over-the-air updates: an OPT-IN area (held out of the default profile), so the whole
      // group is gated on the area being known to be deployed, not on authority alone. The
      // nav FAILS CLOSED while discovery is unanswered (a hidden entry is recoverable, an
      // entry that leads to a "not deployed" page on every default instance is not), which
      // is the opposite of what the routed pages do; see useUpdateAccess.
      {
        labelKey: 'updates',
        icon: UploadCloud,
        children: [
          { labelKey: 'artifacts', href: '/updates/artifacts', icon: FileArchive, requires: 'artifact:read', gate: 'updates' },
          { labelKey: 'rollouts', href: '/updates/rollouts', icon: Rocket, requires: 'artifact:read', gate: 'updates' },
          { labelKey: 'signers', href: '/updates/signers', icon: ShieldCheck, requires: 'artifact:read', gate: 'updates' },
        ],
      },
    ],
  },
  {
    // How devices and outside systems get onto the platform and how data leaves it. None of these
    // is marked `scoped`: the server refuses them to a member limited to part of the tenant.
    id: 'connect',
    labelKey: 'connect',
    icon: Webhook,
    items: [
      // How a fleet gets onto the platform without an operator creating each unit: the shared
      // key and secret it registers with.
      { labelKey: 'provisioningProfiles', href: '/provisioning-profiles', icon: KeyRound, requires: 'device:read' },
      // The CA certificates the tenant trusts to vouch for its devices' client certificates.
      { labelKey: 'trustAnchors', href: '/trust-anchors', icon: ShieldCheck, requires: 'device:read' },
      // Authenticated senders of device data over HTTP (a LoRaWAN network server, a webhook).
      { labelKey: 'integrations', href: '/integrations', icon: PlugZap, requires: 'device:read' },
      { labelKey: 'connectors', href: '/connectors', icon: Webhook, requires: 'connector:read' },
    ],
  },
  {
    // Asset / Customer / Area share device-management's authority model: the whole
    // service is gated by device:read, so every leaf below uses it too.
    id: 'organization',
    labelKey: 'organization',
    icon: Building2,
    items: [
      {
        labelKey: 'assets',
        icon: Package,
        children: [
          { labelKey: 'assets', href: '/assets', icon: Package, requires: 'device:read' },
          { labelKey: 'assetTypes', href: '/asset-types', icon: Boxes, requires: 'device:read' },
          { labelKey: 'assetGroups', href: '/asset-groups', icon: Layers, requires: 'device:read' },
        ],
      },
      {
        labelKey: 'customers',
        icon: Building2,
        children: [
          { labelKey: 'customers', href: '/customers', icon: Building2, requires: 'device:read' },
          { labelKey: 'customerTypes', href: '/customer-types', icon: Boxes, requires: 'device:read' },
          { labelKey: 'customerGroups', href: '/customer-groups', icon: Layers, requires: 'device:read' },
        ],
      },
      {
        labelKey: 'areas',
        icon: MapPin,
        children: [
          { labelKey: 'areas', href: '/areas', icon: MapPin, requires: 'device:read' },
          { labelKey: 'areaTypes', href: '/area-types', icon: Boxes, requires: 'device:read' },
          { labelKey: 'areaGroups', href: '/area-groups', icon: Layers, requires: 'device:read' },
          // Geofences are geometry rather than an organizational construct, but they
          // answer the same question this group answers — where — and they are served
          // by device-management under the same authority.
          { labelKey: 'geofences', href: '/geofences', icon: Pentagon, requires: 'device:read' },
        ],
      },
      // Facets classify every member family (ADR-061), so they are one cross-cutting
      // registry rather than a leaf under each construct group.
      { labelKey: 'facets', href: '/facets', icon: Tags, requires: 'device:read' },
      // Faceted browse + dynamic groups (ADR-061 G4) — compose a selector from facet
      // axes, preview matches live, save it as a dynamic group. Cross-cutting like Facets.
      { labelKey: 'browse', href: '/browse', icon: Filter, requires: 'device:read' },
    ],
  },
  {
    // Who may do what, and the record of what they did. Separate grants throughout, so the
    // section shows exactly the entries the caller holds a grant for.
    id: 'access',
    labelKey: 'access',
    icon: LockKeyhole,
    items: [
      { labelKey: 'members', href: '/members', icon: Users, requires: 'member:read' },
      // The tenant's integrations: non-human principals, administered with the same grant
      // as its people.
      { labelKey: 'serviceAccounts', href: '/service-accounts', icon: KeyRound, requires: 'member:read' },
      { labelKey: 'roles', href: '/roles', icon: UserCog, requires: 'role:read' },
      // Whether the tenant requires a second factor of its people's password sign-ins. It
      // changes who can get in, so it is gated on the member grant, not a cosmetic one.
      { labelKey: 'security', href: '/tenant-security', icon: LockKeyhole, requires: 'member:write' },
      { labelKey: 'audit', href: '/audit', icon: ScrollText, requires: 'audit:read' },
    ],
  },
  // 🔴 Settings stays a SECTION rather than a tabbed page, and the reason is the authorities
  // rather than the aesthetics. `branding:write` and `basemap:write` are deliberately separate
  // grants — bundling them would make each imply the other, which is the whole reason
  // basemap:write exists. visibleNav filters children by `requires` AND drops a section left with
  // no visible children, so this shape gets the correct behaviour at every combination for free.
  // The routes stay flat (/branding, /basemap) like every other entry's.
  {
    id: 'settings',
    labelKey: 'settings',
    icon: SlidersHorizontal,
    items: [
      { labelKey: 'branding', href: '/branding', icon: Palette, requires: 'branding:write' },
      // Labelled "Map", not "Basemap": inside Settings the longer name is redundant,
      // and the page sets the default view (centre/zoom) as well as the tile source,
      // which is not a basemap property at all. The DOMAIN vocabulary stays
      // "basemap" everywhere precision matters — the authority, the GraphQL field,
      // the docs page — because a nav label is wayfinding and an identifier is not.
      { labelKey: 'map', href: '/basemap', icon: Map, requires: 'basemap:write' },
      // The tenant's DEFAULT language — a separate grant for the same reason the others are.
      { labelKey: 'language', href: '/locale', icon: Languages, requires: 'locale:write' },
    ],
  },
  // Inference providers for NL→rule authoring (ADR-056) are NOT here: they are
  // instance config an operator owns, so they live in the admin console (ADR-065). A
  // tenant's only say over AI is its external-routing consent, set per tenant by an
  // operator.
];

export function isLeaf(node: NavItem | NavNode): node is NavLeaf {
  return 'href' in node;
}

/** Every link in a navigation tree, flattened through sections and sub-groups. */
export function navLeaves(nodes: readonly NavNode[] = NAV): NavLeaf[] {
  return nodes.flatMap((n) => (isLeaf(n) ? [n] : n.items.flatMap((i) => (isLeaf(i) ? [i] : i.children))));
}

function canSee(leaf: NavLeaf, claims: DecodedClaims | null, updatesVisible: boolean): boolean {
  if (isScoped(claims) && !leaf.scoped) return false;
  if (leaf.gate === 'updates' && !updatesVisible) return false;
  return !leaf.requires || hasAuthority(claims, leaf.requires);
}

// Drop nav the user can't use: leaves they lack the authority for, then any sub-group and any
// section left with nothing visible. Pages stay fail-closed server-side regardless; this just
// avoids advertising what would only return "forbidden".
export function visibleNav(claims: DecodedClaims | null, updatesVisible: boolean): NavNode[] {
  return NAV.flatMap<NavNode>((node) => {
    if (isLeaf(node)) return canSee(node, claims, updatesVisible) ? [node] : [];
    const items = node.items.flatMap<NavItem>((item) => {
      if (isLeaf(item)) return canSee(item, claims, updatesVisible) ? [item] : [];
      const children = item.children.filter((c) => canSee(c, claims, updatesVisible));
      return children.length > 0 ? [{ ...item, children }] : [];
    });
    return items.length > 0 ? [{ ...node, items }] : [];
  });
}

function isActive(pathname: string, href: string) {
  return href === '/' ? pathname === '/' : pathname.startsWith(href);
}

const itemActive = (pathname: string, item: NavItem) =>
  isLeaf(item) ? isActive(pathname, item.href) : item.children.some((c) => isActive(pathname, c.href));

/** True when the current route is one of the section's (visible) links. */
export function sectionHasActive(section: NavSection, pathname: string): boolean {
  return section.items.some((i) => itemActive(pathname, i));
}

// What is expanded when the user has not chosen: the two sections nearly every visit starts in.
const DEFAULT_OPEN: ReadonlySet<string> = new Set(['monitor', 'devices']);

/**
 * Whether a section or sub-group is expanded. The one holding the current route is ALWAYS open —
 * a user must never land on a page whose own entry is hidden — whatever they last chose; any other
 * follows the remembered choice, then the default.
 */
export function isExpanded(id: string, hasActive: boolean, stored: NavOpenState): boolean {
  return hasActive || (stored[id] ?? DEFAULT_OPEN.has(id));
}

const groupId = (g: NavGroupNode) => `group:${g.labelKey}`;

export function AppSidebar() {
  const { pathname } = useLocation();
  const { t } = useTranslation('nav');
  const { claims } = useAuth();
  const tenant = useCurrentTenant();
  const brandLogo = useBrandingLogoSrc(tenant?.branding?.logo);
  const brandHeight = tenant?.branding?.logoMaxHeight ?? 24;
  const updates = useUpdateAccess();
  const nav = visibleNav(claims, updates.canRead);
  // Remembered per browser; read once. Absent or unreadable storage yields {} and the defaults.
  const [stored, setStored] = useState<NavOpenState>(readNavState);

  const toggle = (id: string, currentlyOpen: boolean) => {
    const next = { ...stored, [id]: !currentlyOpen };
    setStored(next);
    writeNavState(next);
  };

  return (
    <Sidebar collapsible="icon" data-shot="sidebar">
      <SidebarHeader className="pt-4">
        <SidebarMenu>
          <SidebarMenuItem>
            <SidebarMenuButton size="lg" asChild tooltip="DeviceChain" className="justify-center">
              <Link to="/">
                {/* Collapsed icon rail: cube mark only (a tenant favicon/mark is
                    Phase 3; the rail keeps the DeviceChain cube for now). */}
                <Logomark className="hidden size-7 shrink-0 group-data-[collapsible=icon]:block" />
                {/* Expanded: the tenant's branding logo when set (ADR-038), else
                    the DeviceChain lockup. Rendered only in an <img> so an
                    SVG-via-https logo cannot execute script. */}
                <div className="flex flex-col items-center gap-1 group-data-[collapsible=icon]:hidden">
                  {brandLogo ? (
                    <img
                      src={brandLogo}
                      alt={tenant?.name || tenant?.token || t('common:tenantFallback')}
                      className="w-auto max-w-full object-contain"
                      style={{ maxHeight: brandHeight }}
                    />
                  ) : (
                    <LogoHorizontal deviceColor="currentColor" className="h-[17px] w-auto" />
                  )}
                  <span className="truncate text-xs text-muted-foreground">
                    {t('managementConsole')}
                  </span>
                </div>
              </Link>
            </SidebarMenuButton>
          </SidebarMenuItem>
        </SidebarMenu>
      </SidebarHeader>

      <SidebarContent className="overflow-x-hidden">
        <SidebarGroup>
          <SidebarGroupContent>
            <SidebarMenu>
              {nav.map((node) =>
                isLeaf(node) ? (
                  <SidebarMenuItem key={node.labelKey}>
                    <SidebarMenuButton
                      asChild
                      isActive={isActive(pathname, node.href)}
                      tooltip={t(node.labelKey)}
                      className={cn(
                        'text-[0.9375rem] font-medium',
                        isActive(pathname, node.href) && '!font-semibold !text-primary',
                      )}
                    >
                      <Link to={node.href}>
                        <node.icon />
                        <span>{t(node.labelKey)}</span>
                      </Link>
                    </SidebarMenuButton>
                  </SidebarMenuItem>
                ) : (
                  <NavSectionView
                    key={node.id}
                    section={node}
                    pathname={pathname}
                    stored={stored}
                    onToggle={toggle}
                  />
                ),
              )}
            </SidebarMenu>
          </SidebarGroupContent>
        </SidebarGroup>
      </SidebarContent>

      <SidebarFooter>
        <NavUser />
      </SidebarFooter>

      <SidebarRail />
    </Sidebar>
  );
}

const subLinkClass = (active: boolean) =>
  cn(
    // Squared, edge-to-edge rows; the active item is shown by font/color
    // alone (no fill) so it reads clean against the lighter panel.
    'rounded-none text-[0.8125rem] data-[active=true]:bg-transparent',
    active && '!font-semibold !text-primary',
  );

function NavLeafLink({ leaf, pathname }: { leaf: NavLeaf; pathname: string }) {
  const { t } = useTranslation('nav');
  const active = isActive(pathname, leaf.href);
  return (
    <SidebarMenuSubItem>
      <SidebarMenuSubButton
        asChild
        isActive={active}
        className={cn(subLinkClass(active), leaf.hintKey && 'h-auto py-1')}
      >
        <Link to={leaf.href}>
          <leaf.icon />
          {leaf.hintKey ? (
            <span className="flex min-w-0 flex-col">
              <span>{t(leaf.labelKey)}</span>
              <span className="truncate text-[0.6875rem] font-normal leading-tight text-muted-foreground">
                {t(leaf.hintKey)}
              </span>
            </span>
          ) : (
            <span>{t(leaf.labelKey)}</span>
          )}
        </Link>
      </SidebarMenuSubButton>
    </SidebarMenuSubItem>
  );
}

type Toggle = (id: string, currentlyOpen: boolean) => void;

// A disclosure: a header BUTTON (aria-expanded, aria-controls) and, only while open, the list it
// controls. Used for a top-level section and for a construct's sub-list inside one. The control is
// never remounted by toggling, so focus stays on it.
function Disclosure({
  id,
  icon: Icon,
  labelKey,
  open,
  onClick,
  nested,
  children,
}: {
  id: string;
  icon: LucideIcon;
  labelKey: string;
  open: boolean;
  onClick: () => void;
  nested?: boolean;
  children: ReactNode;
}) {
  const { t } = useTranslation('nav');
  const contentId = `nav-${id.replace(':', '-')}`;
  return (
    <>
      <SidebarMenuButton
        type="button"
        onClick={onClick}
        aria-expanded={open}
        aria-controls={contentId}
        tooltip={nested ? undefined : t(labelKey)}
        size={nested ? 'sm' : 'default'}
        className={nested ? cn(subLinkClass(false), 'h-7 px-2') : 'text-[0.9375rem] font-medium'}
      >
        <Icon />
        <span>{t(labelKey)}</span>
        <ChevronRight
          className={cn(
            'ml-auto transition-transform group-data-[collapsible=icon]:hidden',
            open && 'rotate-90',
          )}
        />
      </SidebarMenuButton>
      {open && (
        <SidebarMenuSub
          id={contentId}
          className={
            nested
              ? 'mx-0 border-l-0 py-1 pl-4 pr-0'
              : '-mx-2 rounded-none border-l-0 bg-sidebar-accent/70 py-2.5 pl-6 pr-2'
          }
        >
          {children}
        </SidebarMenuSub>
      )}
    </>
  );
}

function NavSectionView({
  section,
  pathname,
  stored,
  onToggle,
}: {
  section: NavSection;
  pathname: string;
  stored: NavOpenState;
  onToggle: Toggle;
}) {
  const navigate = useNavigate();
  const { state, isMobile } = useSidebar();
  const open = isExpanded(section.id, sectionHasActive(section, pathname), stored);
  // In the icon rail the children are not drawn, so a header that only toggled would be a dead
  // button: there it opens the section's first page instead.
  const first = section.items[0];
  const onClick = () =>
    state === 'collapsed' && !isMobile
      ? navigate(isLeaf(first) ? first.href : first.children[0].href)
      : onToggle(section.id, open);
  return (
    <SidebarMenuItem>
      <Disclosure id={section.id} icon={section.icon} labelKey={section.labelKey} open={open} onClick={onClick}>
        {section.items.map((item) =>
          isLeaf(item) ? (
            <NavLeafLink key={item.href} leaf={item} pathname={pathname} />
          ) : (
            <NavSubGroup key={item.labelKey} group={item} pathname={pathname} stored={stored} onToggle={onToggle} />
          ),
        )}
      </Disclosure>
    </SidebarMenuItem>
  );
}

// A construct's own list (Assets: instances, types, groups) nested in a section. Same rule as the
// section above it: the one holding the current route is open, the rest follow the user's choice
// and default to closed.
function NavSubGroup({
  group,
  pathname,
  stored,
  onToggle,
}: {
  group: NavGroupNode;
  pathname: string;
  stored: NavOpenState;
  onToggle: Toggle;
}) {
  const id = groupId(group);
  const open = isExpanded(
    id,
    group.children.some((c) => isActive(pathname, c.href)),
    stored,
  );
  return (
    <SidebarMenuSubItem>
      <Disclosure id={id} icon={group.icon} labelKey={group.labelKey} open={open} onClick={() => onToggle(id, open)} nested>
        {group.children.map((child) => (
          <NavLeafLink key={child.href} leaf={child} pathname={pathname} />
        ))}
      </Disclosure>
    </SidebarMenuSubItem>
  );
}
