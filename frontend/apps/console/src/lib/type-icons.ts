// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// A curated set of Lucide icons selectable as a registry type's appearance icon.
// The chosen icon is stored as its key string on the type (the `icon` field) and
// rendered via this map, so we ship only these components — not all of Lucide.

import {
  Activity,
  Battery,
  Bell,
  Box,
  Boxes,
  Building2,
  Camera,
  Car,
  Cloud,
  Cpu,
  Droplet,
  Factory,
  Fan,
  Flame,
  Forklift,
  Gauge,
  Home,
  Lightbulb,
  Lock,
  Map,
  MapPin,
  Monitor,
  Package,
  Plug,
  Radio,
  Router,
  Smartphone,
  Speaker,
  Sun,
  Thermometer,
  Truck,
  Users,
  Warehouse,
  Wifi,
  Wind,
  Zap,
  type LucideIcon,
} from 'lucide-react';

// Keyed by the stable string stored on the type. Order here is the picker order.
export const TYPE_ICONS: Record<string, LucideIcon> = {
  cpu: Cpu,
  router: Router,
  wifi: Wifi,
  radio: Radio,
  smartphone: Smartphone,
  monitor: Monitor,
  camera: Camera,
  speaker: Speaker,
  thermometer: Thermometer,
  gauge: Gauge,
  activity: Activity,
  droplet: Droplet,
  flame: Flame,
  wind: Wind,
  sun: Sun,
  cloud: Cloud,
  zap: Zap,
  battery: Battery,
  plug: Plug,
  lightbulb: Lightbulb,
  fan: Fan,
  lock: Lock,
  bell: Bell,
  package: Package,
  box: Box,
  boxes: Boxes,
  warehouse: Warehouse,
  forklift: Forklift,
  truck: Truck,
  car: Car,
  factory: Factory,
  building: Building2,
  home: Home,
  map: Map,
  pin: MapPin,
  users: Users,
};

export const TYPE_ICON_KEYS = Object.keys(TYPE_ICONS);

// Resolve an icon key to its component, or null when unset/unknown.
export function typeIcon(key?: string | null): LucideIcon | null {
  return key ? (TYPE_ICONS[key] ?? null) : null;
}
