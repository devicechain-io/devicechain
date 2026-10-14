// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// What a device-type request says about its maintenance window.
//
// DeviceType create and update take `maintenanceWindowToken` as a three-state field: omitted
// keeps what is stored, an empty value or null detaches, a token attaches. The form therefore
// must NOT spell "unchanged" as null (that would detach a window nobody touched).

/**
 * The `maintenanceWindowToken` to put on a request, or `undefined` for "do not mention it".
 *
 * @param initial  the window the type had when the form opened ('' for none)
 * @param selected the window now chosen ('' for none)
 * @param editing  false on a create, where there is nothing stored to keep or detach
 */
export function maintenanceWindowArg(
  initial: string,
  selected: string,
  editing: boolean,
): string | null | undefined {
  if (!editing) return selected === '' ? undefined : selected;
  if (selected === initial) return undefined;
  return selected === '' ? null : selected;
}
