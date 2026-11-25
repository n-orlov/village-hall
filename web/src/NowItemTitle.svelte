<script lang="ts">
  // The title line of one item on a Now band: the item's link (or plain title when it has no page) and, on an expired lease
  // that was a notice (SPEC 4.4, `notice: true` on the row), a visible "notice" mark beside it.
  import type { ItemRow } from './lib/attentionView';

  let { row }: { row: Pick<ItemRow, 'title' | 'href' | 'notice'> } = $props();
</script>

<p class="font-medium">
  {#if row.href !== undefined}
    <a class="inline-flex min-h-11 items-center text-accent hover:underline" href={row.href} data-testid="item-link">{row.title}</a>
  {:else}
    <span data-testid="item-title">{row.title}</span>
  {/if}
  {#if row.notice === true}
    <span class="ml-2 inline-block rounded-md border border-border px-2 py-0.5 align-middle text-xs font-normal text-text-muted" data-testid="notice-mark">notice</span>
  {/if}
</p>
