import { expect, type Locator, type Page } from '@playwright/test';

// Helpers for the shared chip filter on Tasks (web/src/ChipFilter.svelte): its testids and how a test reaches a chip.
// On a phone the filter is folded behind one line; a long list of initiatives folds behind "more".

export const initiativeChip = (page: Page, slug: string): Locator => page.locator(`[data-testid="chip-initiative"][data-initiative="${slug}"]`);
export const tagChip = (page: Page, tag: string): Locator => page.locator(`[data-testid="chip-tag"][data-tag="${tag}"]`);

/**
 * Resolves once a read of the initiatives catalog has ended (its last page, `next_after` absent) whose last page lists `slug`,
 * or any read's last page when no slug is named; a predicate instead of a slug tests the last page's items itself. The list is in creation order, so a new initiative is on the last page. The
 * chips, a restored filter and the Initiatives list all wait for the whole catalog, which a long run's shared board grows to
 * many pages: a test waits for that read itself, then gives the page its usual bound to draw it. Call it before the navigation.
 */
export interface CatalogItem {
  slug: string;
  counts?: { open: number; done: number; closed: number };
}

export function catalogRead(page: Page, match?: string | ((items: CatalogItem[]) => boolean)): Promise<unknown> {
  const test = typeof match === 'string' ? (items: CatalogItem[]) => items.some((i) => i.slug === match) : match;
  return page.waitForResponse(async (response) => {
    if (new URL(response.url()).pathname !== '/api/v1/initiatives' || response.request().method() !== 'GET') return false;
    const body = (await response.json().catch(() => null)) as { items: CatalogItem[]; next_after?: string } | null;
    return body !== null && body.next_after === undefined && (test === undefined || test(body.items));
  });
}

/** Opens the phone's folded filter; a no-op where the filter is always open. */
export async function openChips(page: Page): Promise<void> {
  const toggle = page.getByTestId('chip-toggle');
  if ((await toggle.isVisible()) && (await toggle.getAttribute('aria-expanded')) === 'false') await toggle.click();
}

/**
 * Folds the filter as a fresh mount draws it: the phone's filter behind its one line, and a long list of initiatives
 * behind "more" (on a long run the shared board has hundreds, and an unfolded list makes the page much taller than the
 * page that comes back). A no-op where neither is open.
 */
export async function foldChips(page: Page): Promise<void> {
  const more = page.getByTestId('chip-more');
  if ((await more.count()) > 0 && (await more.isVisible()) && (await more.getAttribute('aria-expanded')) === 'true') await more.click();
  const toggle = page.getByTestId('chip-toggle');
  if ((await toggle.isVisible()) && (await toggle.getAttribute('aria-expanded')) === 'true') await toggle.click();
}

/** Shows every initiative chip (opens "more" when the list is folded). */
export async function showAllInitiatives(page: Page): Promise<void> {
  await openChips(page);
  await expect(page.getByTestId('chip-initiatives')).toBeVisible();
  const more = page.getByTestId('chip-more');
  if ((await more.count()) > 0 && (await more.getAttribute('aria-expanded')) === 'false') await more.click();
}

/** The chip of an initiative, scrolled into reach: the filter opened and the list unfolded when it is not on show. */
export async function revealInitiative(page: Page, slug: string): Promise<Locator> {
  await openChips(page);
  const chip = initiativeChip(page, slug);
  await expect(async () => {
    if ((await chip.count()) === 0) await showAllInitiatives(page);
    await expect(chip).toBeVisible({ timeout: 500 });
  }).toPass({ timeout: 10_000 });
  return chip;
}

export async function pickInitiative(page: Page, slug: string): Promise<void> {
  await (await revealInitiative(page, slug)).click();
}

/** The initiatives the chips show as chosen, sorted. */
export async function chosenInitiatives(page: Page): Promise<string[]> {
  const chips = page.locator('[data-testid="chip-initiative"][aria-pressed="true"]');
  const slugs = await chips.evaluateAll((els) => els.map((el) => el.getAttribute('data-initiative') ?? ''));
  return slugs.sort();
}

/** Asserts exactly these initiatives are chosen (an empty list is "any initiative"). */
export async function expectInitiatives(page: Page, slugs: string[]): Promise<void> {
  await expect.poll(() => chosenInitiatives(page)).toEqual([...slugs].sort());
}

export async function pickTag(page: Page, tag: string): Promise<void> {
  await openChips(page);
  const chip = tagChip(page, tag);
  await expect(chip).toBeVisible();
  await chip.click();
}

/** The tags the chips show as chosen, sorted. */
export async function chosenTags(page: Page): Promise<string[]> {
  const chips = page.locator('[data-testid="chip-tag"][aria-pressed="true"]');
  const tags = await chips.evaluateAll((els) => els.map((el) => el.getAttribute('data-tag') ?? ''));
  return tags.sort();
}

/** Asserts exactly these tags are chosen (an empty list is "any tag"). */
export async function expectTags(page: Page, tags: string[]): Promise<void> {
  await expect.poll(() => chosenTags(page)).toEqual([...tags].sort());
}
