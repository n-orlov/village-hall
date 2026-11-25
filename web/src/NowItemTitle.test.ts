// Renders the title line of a Now item (NowItemTitle.svelte, server-side through svelte/server: the unit tier has no DOM)
// for rows built from an attention read, so the notice mark is checked in the markup the view shows, not in the row data.
import { render } from 'svelte/server';
import { describe, expect, it } from 'vitest';
import NowItemTitle from './NowItemTitle.svelte';
import { rowsFor, type Attention } from './lib/attentionView';

const NOW = new Date('2026-01-01T12:00:00Z');
const bob = { handle: 'bob', way_back: 'ralphctl status bob' };
const lease = {
  lease: 'L4',
  holder: bob,
  task: 'aa-3',
  title: 'Slow',
  intent: 'ride',
  renewed_at: '2026-01-01T08:00:00Z',
  expired_at: '2026-01-01T09:00:00Z',
  notice: false,
  tags: [],
  tag_marks: [],
};
const attention: Attention = {
  counts: { open_asks: 0, done_not_closed: 0, expired_leases: 2, overdue_watches: 0, conflicts: 0, total: 2 },
  expired_leases: [{ ...lease, lease: 'L8', notice: true }, lease],
};

const markup = (row: ReturnType<typeof rowsFor>[number]) => render(NowItemTitle, { props: { row } }).body;
const marks = (html: string) => html.match(/<span[^>]*data-testid="notice-mark"[^>]*>([^<]*)<\/span>/g) ?? [];

describe('NowItemTitle on the Expired band (R9)', () => {
  const [notice, ordinary] = rowsFor('expired_leases', attention, NOW);

  it('shows a visible "notice" mark beside an expired notice', () => {
    const html = markup(notice!);
    expect(html).toContain('L8 on aa-3 Slow');
    expect(marks(html)).toHaveLength(1);
    expect(marks(html)[0]).toMatch(/>notice<\/span>$/);
    expect(marks(html)[0]).not.toMatch(/sr-only|hidden/);
  });

  it('shows no notice mark on an ordinary expired lease', () => {
    const html = markup(ordinary!);
    expect(html).toContain('L4 on aa-3 Slow');
    expect(marks(html)).toEqual([]);
    expect(html).not.toContain('notice');
  });
});
