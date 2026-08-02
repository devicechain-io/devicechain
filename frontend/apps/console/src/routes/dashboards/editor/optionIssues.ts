// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// How a widget option issue is worded for an author.
//
// 🔴 WHY THE PACKAGE'S OWN `message` IS NOT WHAT THE UI SHOWS. validateWidgetOptions
// returns an English sentence, and it is a good one — but it is built inside
// @devicechain/widgets, which ships no catalogs and has no locale. Rendering it would
// put untranslated English in the middle of a Spanish console, and the i18n lint could
// not see it: the rule flags literal strings in JSX, and `{issue.message}` is an
// expression. So the code is translated here and the message stays where it belongs —
// in the error publishDashboard throws, which is read by a developer, not an author.
//
// The trade is deliberate: the schema's message carries detail this mapping drops ("max
// (0) must be greater than min (100)" becomes "max conflicts with another option"). What
// survives is the part an author acts on — WHICH option, and WHAT KIND of wrong — and
// that beats precise English nobody in the room reads.

import type { OptionIssueCode } from '@devicechain/widgets';

// A translation key per issue code.
//
// 🔑 `Record<OptionIssueCode, …>` is the gate, and it is a compile-time one: a code
// added to the widgets package does not build here until it is worded. Without it a new
// code would fall through to an undefined key and i18next would render the key itself —
// a raw `optionIssueSomething` in the UI, which is the failure mode of every dynamic
// translation lookup and the reason this map is exhaustive rather than a lookup with a
// default. The catalogs are held to it by optionIssues.test.ts.
export const OPTION_ISSUE_MESSAGE_KEYS: Record<OptionIssueCode, string> = {
  missing: 'optionIssueMissing',
  unknown: 'optionIssueUnknown',
  type: 'optionIssueType',
  enum: 'optionIssueEnum',
  range: 'optionIssueRange',
  json: 'optionIssueJson',
  invariant: 'optionIssueInvariant',
};
