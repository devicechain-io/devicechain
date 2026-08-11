// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// ESLint flat config for the console — its SOLE purpose is the ADR-066
// sub-workstream (b) string-externalization sweep: eslint-plugin-i18next's
// `no-literal-string` rule enumerates every user-facing string literal still
// hard-coded in JSX so none is missed (the AdminUser popover slipping through the
// #524 chrome sweep is exactly the miss this rule prevents). It is deliberately
// NOT a general lint config — no style/correctness rules — so it never competes
// with tsc as the type/quality gate.
//
// Parser: @babel/eslint-parser, NOT typescript-eslint. typescript-eslint hard
// refuses to load against the repo's TypeScript 7 (the Go-based tsc) — its
// typescript-estree throws a version guard at import. Babel strips types and
// emits an ESTree AST with no dependency on the installed tsc, so it parses our
// TSX regardless of the TS version. The i18next rule only walks JSX text /
// attribute / literal nodes, so a type-free parse is sufficient.
import i18next from 'eslint-plugin-i18next';
import i18nextDefaults from 'eslint-plugin-i18next/lib/options/defaults.js';
import reactHooks from 'eslint-plugin-react-hooks';
import babelParser from '@babel/eslint-parser';

// The rule REPLACES (does not merge) any option list it is given, so we extend
// the plugin's own defaults rather than hand-copy them — this keeps the built-in
// excludes (htmlEntities, emoji, all-caps enum words, the i18n callees) intact
// and drift-free, and layers this codebase's non-text noise on top.
//
//  - jsx-attributes: shadcn/radix/lucide structural + enum props (variant, size,
//    side, …) are API values, never user text. Real user-text attributes
//    (placeholder/title/alt/label/aria-label/description) are deliberately NOT
//    excluded, so they stay in the worklist.
//  - callees: classname builders (cn/cva/clsx/…) carry tailwind fragments, and
//    navigate()/act() carry route paths / enum tokens — none are user text.
//  - words: the brand name is a proper noun, never localized.
// 🔴 One asymmetry in this rule is worth knowing before reading the exclusions: it
// walks literals inside the attribute EXPRESSIONS of a capitalized component
// (`<Foo onClick={() => go('x')}>`) but NOT of a native lowercase element
// (`<div onClick={() => go('x')}>`). JSX text and plain attribute strings are caught on
// both. So replacing native elements with kit components WIDENS this rule's reach —
// the native-control sweep surfaced a dozen technical literals that had been sitting
// inside `<button onClick=…>` unseen. None were user-facing text (they are enum
// discriminants and route paths), but that is a fact about this codebase, not a
// guarantee: a real string can hide there too.
const EXTRA_ATTRS = [
  'variant', 'size', 'side', 'sideOffset', 'align', 'alignOffset',
  // SegmentedControl's `tone` — an enum picking a look ('inset'/'joined'/'loose'/
  // 'bare'), never display text.
  'tone',
  'mode', 'axis', 'orientation', 'position', 'collapsible', 'banner',
  'color', 'fill', 'stroke', 'deviceColor', 'defaultTheme',
  'to', 'href', 'target', 'rel', 'role', 'name', 'htmlFor',
  'autoComplete', 'inputMode', 'dir', 'slot', 'data-testid',
  // FilePicker's `accept` is a MIME-type list ('image/png,image/jpeg'), never text.
  'accept',
  // radix/shadcn discriminants — Tabs/Select/RadioGroup `value`/`defaultValue`
  // are enum tokens that pick a pane/option, never display text (the visible
  // label is the element's children). Excluding them avoids forcing every such
  // literal to be hoisted into a named const just to dodge the rule.
  'value', 'defaultValue',
  // TokenField's `entityType` is the entity-kind key it resolves a token mask
  // from ('connector', 'role', 'metric-definition', …) — a technical
  // discriminant, never user text.
  'entityType',
  // shadcn structural / API props, never display text:
  //  - data-sidebar / data-mobile: the sidebar primitive's internal state hooks
  //    (styling + query selectors), not rendered anywhere.
  //  - theme: Sonner's 'dark' | 'light' | 'system' enum.
  //  - fallback: ColorField's default hex swatch ('#1f2937'), a color code.
  'data-sidebar', 'data-mobile', 'theme', 'fallback',
  // Registry-kit i18n/technical discriminants, never display text:
  //  - i18nKey: a catalog key. On a RegistryResource it's the family prefix
  //    ('deviceType'); on react-i18next's <Trans> it's the message key. A key,
  //    not prose — the resolved text is what the user sees.
  //  - memberI18nKey / groupType / memberType: MembershipPanel's family prefix
  //    and backend entity-type tokens ('group', 'asset').
  'i18nKey', 'memberI18nKey', 'groupType', 'memberType',
  // React Router `<Route path="devices/:token">` — a URL route pattern, never
  // display text. The visible label lives in the nav/page chrome, not here.
  'path',
];
// `e` is the registry engine's per-resource key resolver (`e('New')` →
// t(`entities:${i18nKey}New`)); its argument is a key suffix, never user text —
// the same reason `t` itself is excluded. No other `e('literal')` call exists.
// leaveGuarded is the dashboard workspace's navigate — it takes a route path through
// the unsaved-changes prompt — so it is excluded for the same reason navigate is.
const EXTRA_CALLEES = [
  'cn', 'cva', 'clsx', 'cx', 'twMerge', 'tv', 'navigate', 'leaveGuarded', 'act', 'e',
];
const EXTRA_WORDS = ['^DeviceChain$'];

export default [
  {
    // Nothing here needs linting for bare strings:
    //  - gql/**            graphql-codegen output (regenerated, no UI text)
    //  - i18n/**           the catalogs + config ARE the translations
    //  - *.test.*          test fixtures legitimately hold literal strings
    //  - build/tool config not shipped to users
    ignores: [
      'dist/**',
      'src/gql/**',
      'src/i18n/**',
      '**/*.test.ts',
      '**/*.test.tsx',
      'vite.config.ts',
      'vitest.setup.ts',
      'codegen.ts',
      'i18next-parser.config.js',
      'eslint.config.js',
    ],
  },
  {
    files: ['src/**/*.{ts,tsx}'],
    // The react-hooks/exhaustive-deps directives (see the plugins note below) are
    // inert here because that rule is off, so eslint would flag every one as an
    // "unused disable directive". This config lints for bare strings only — leave
    // those suppressions untouched.
    linterOptions: { reportUnusedDisableDirectives: 'off' },
    languageOptions: {
      parser: babelParser,
      parserOptions: {
        // No .babelrc in this workspace — presets are supplied inline. Babel
        // reads the per-file name ESLint passes to pick .ts vs .tsx mode.
        requireConfigFile: false,
        babelOptions: {
          presets: ['@babel/preset-typescript', '@babel/preset-react'],
        },
      },
    },
    // react-hooks is registered ONLY so the pre-existing
    // `// eslint-disable-next-line react-hooks/exhaustive-deps` directives across
    // the source resolve to a known rule instead of erroring "rule not found" —
    // which would fail the gate on non-i18n grounds. Its rules stay OFF: this
    // config lints for bare strings only.
    plugins: { i18next, 'react-hooks': reactHooks },
    rules: {
      // jsx-only flags both JSX text nodes and user-facing string-literal
      // attributes — the whole worklist for (b). Once the sweep is done this
      // reaches a true zero, so it can gate CI without false positives.
      'i18next/no-literal-string': [
        'error',
        {
          mode: 'jsx-only',
          'jsx-attributes': {
            exclude: [...i18nextDefaults['jsx-attributes'].exclude, ...EXTRA_ATTRS],
          },
          callees: {
            exclude: [...i18nextDefaults.callees.exclude, ...EXTRA_CALLEES],
          },
          words: {
            exclude: [...i18nextDefaults.words.exclude, ...EXTRA_WORDS],
          },
        },
      ],

      // 🔴 USE THE KIT'S CONTROLS, NOT THE BROWSER'S.
      //
      // The reason is not tidiness. A native control's appearance is only partly
      // ours: `<select>` in particular has its option list drawn by the OPERATING
      // SYSTEM, which takes none of our theme, so on a dark surface it renders a
      // white popup with near-invisible text. That shipped, and it shipped from a
      // considered decision — the a11y argument for a native `<select>` is sound and
      // is entirely about SEMANTICS, which is why it did not catch a visual defect.
      //
      // The durable reason is single-point-of-change: styling and behaviour should
      // move in `components/ui/` and take the whole console with them, rather than
      // being chased across every call site that hand-rolled a control.
      //
      // 🔴 AN ELEMENT JOINS THIS LIST THE MOMENT ITS SWEEP REACHES ZERO, NOT BEFORE.
      // A rule introduced with existing violations either needs a suppression list
      // nobody prunes, or gets demoted to a warning and ignored.
      //
      // `select`, `input`, `textarea` and `button` are all at zero outside
      // components/ui. The first three took three new primitives — Textarea (promoted
      // out of routes/common.tsx), ColorPicker (react-colorful in our own Popover,
      // because `type="color"` opens an unthemeable OS dialog) and FilePicker (the one
      // case where the native element is unavoidable, so the kit owns the only part the
      // user sees).
      //
      // 🔴 `button` WAS NOT A MECHANICAL CONVERSION, AND THE INTERESTING PART WAS NOT
      // THE STYLING. Sorting its 19 sites by what they DO rather than by how they look
      // turned up a defect the duplication was hiding: most of them were pickers where
      // exactly one option is chosen — theme, locale, authoring mode, preview window,
      // member family, tier colour — and only three of those announced a selection at
      // all. A screen reader was told nothing about which segment was current.
      //
      //   pick one of N  -> SegmentedControl (radiogroup: one tab stop, arrow keys)
      //                     ThemeToggle, LocaleSwitcher, PreviewPanel, TierForm,
      //                     DetectionRuleAuthoring, BrowsePage, FacetKeysPage
      //   toggle, clearable -> ToggleButton (aria-pressed, required by the type)
      //                     TypeAppearanceForm icon grid, PreviewPanel firing row
      //   icon-only action -> IconButton (`label` required by the type — an icon
      //                     carries no accessible name of its own)
      //                     WidgetConfigPanel, DeviceCredentialsPanel, TiersPage grip
      //   inline text action -> Button variant=link|linkDestructive|quiet size=inline
      //                     AiProviderForm x3, TypeAppearanceForm, DashboardWorkspace
      //
      // Two sites resisted the obvious answer, and both were worth the second look:
      // DashboardCanvas's react-rnd toolbar is an ordinary <Button> once `rnd-no-drag`
      // rides on className, and BrandingPage's "primary button" preview turned out not
      // to be a control at all — it is a PICTURE of the tenant's button, so it is now a
      // <span> and no longer a tab stop that does nothing when activated.
      //
      // Worth recording how the worklist was found: a grep counted 7, the AST counted
      // 27. Multi-line JSX openings are invisible to `<button[ >/]`, which is the
      // argument for gating in the linter rather than in review.
      'no-restricted-syntax': [
        'error',
        {
          selector: 'JSXOpeningElement > JSXIdentifier[name="select"]',
          message:
            "Use <Select> from '@/components/ui/select'. A native <select>'s option list is drawn by the OS and cannot be themed — on the dark console it renders an unreadable white popup.",
        },
        {
          selector: 'JSXOpeningElement > JSXIdentifier[name="input"]',
          message:
            "Use the kit: <Input>, <Checkbox>, <RadioGroup>, <ColorPicker> or <FilePicker>. A native input cannot be styled consistently across browsers beyond accent-color, and type=\"color\" opens an OS dialog that takes none of our theme.",
        },
        {
          selector: 'JSXOpeningElement > JSXIdentifier[name="textarea"]',
          message: "Use <Textarea> from '@/components/ui/textarea'.",
        },
        {
          selector: 'JSXOpeningElement > JSXIdentifier[name="button"]',
          message:
            'Use the kit, chosen by what the control DOES: <SegmentedControl> to pick one of N, <ToggleButton> for a clearable on/off, <IconButton> for an icon-only action (it requires an accessible name), or <Button> for everything else — variant=link|linkDestructive|quiet with size=inline for a text action. A control that is not interactive is not a button at all.',
        },
      ],
    },
  },

  {
    // components/ui IS the place native elements are allowed: it is what wraps them.
    // Without this the primitives could not be written at all.
    files: ['src/components/ui/**/*.{ts,tsx}'],
    rules: { 'no-restricted-syntax': 'off' },
  },

  {
    // Test doubles may use native elements. A fake map or a stub panel exists to
    // stand IN for a component, and making it import the real kit couples the double
    // to the thing it is replacing — which is how a fake starts hiding the defect it
    // was supposed to expose.
    files: ['src/**/*.test.{ts,tsx}'],
    rules: { 'no-restricted-syntax': 'off' },
  },
];
