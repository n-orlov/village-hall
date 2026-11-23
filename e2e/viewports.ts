// The viewports the browser specs run at (SPEC 15.2, "Logic and layout viewports").
//
// A spec whose assertions do not depend on the viewport (sort, filter, live refresh, what a view restores, which
// rows a source or an origin names) runs at the desktop width only: `logicViewports`. Its phone variant re-ran the
// same logic at 390 px, where the only difference is the 768 px breakpoint's stacking, and that is the layout
// specs' job. A spec with any viewport-dependent assertion, touch interaction or layout check (a branch on the
// viewport, a locator that exists at one width only, a tap, a width, scroll or position measurement, an axe scan)
// keeps the phone through `layoutViewports`, or through its own list when it needs further widths or fields.
// ci/suitecheck (TestPhoneDroppedSpecsUseLogicViewports) holds the list of phone-dropped specs.
export const phone = { name: 'phone', size: { width: 390, height: 844 } } as const;
export const desktop = { name: 'desktop', size: { width: 1440, height: 900 } } as const;

/** Desktop only: for a spec with no viewport-dependent assertion. */
export const logicViewports = [desktop] as const;
/** Phone and desktop: for a spec that checks layout or touch behaviour. */
export const layoutViewports = [phone, desktop] as const;
