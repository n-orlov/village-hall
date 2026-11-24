<script lang="ts">
  import { choiceOf, sortChoices, sortOfChoice, type SortState } from './lib/tableSort';

  // The phone's Sort select: cards have no headers to click, so it offers the same choices as the headers.
  // 44 px high and 16 px text; the tables hide it from 768 px, or from `hideFrom` = 'lg' (1024 px) for a table that stays cards until then.
  let {
    columns,
    sort,
    onsort,
    testid = 'sort-select',
    hideFrom = 'md',
  }: {
    columns: readonly { column: string; label: string }[];
    sort: SortState;
    onsort: (next: SortState) => void;
    testid?: string;
    hideFrom?: 'md' | 'lg';
  } = $props();

  const choices = $derived(sortChoices(columns));
</script>

<label class="flex max-w-full items-center gap-2 text-sm text-text-muted {hideFrom === 'lg' ? 'lg:hidden' : 'md:hidden'}">
  <span>Sort</span>
  <select
    class="min-h-11 min-w-0 max-w-full rounded-md border border-border bg-surface-raised px-3 text-base text-text"
    data-testid={testid}
    value={choiceOf(sort)}
    onchange={(event) => onsort(sortOfChoice(event.currentTarget.value))}
  >
    {#each choices as choice (choice.value)}
      <option value={choice.value} selected={choice.value === choiceOf(sort)}>{choice.label}</option>
    {/each}
  </select>
</label>
