<script lang="ts">
  // The shared chip filter (lib/chipFilter.ts): tag chips (all of them must match), initiative chips (any of them) and
  // the two kinds together. A view whose rows are not in an initiative (`withInitiatives={false}`) offers tags only. Every chip is a toggle button (`aria-pressed`) that names its source to a screen reader and
  // on hover. Phones fold the whole filter behind one line; wider screens show it open.
  import {
    chipName,
    chipSourceText,
    chipSummary,
    chipsActive,
    initiativeChoices,
    tagStyle,
    toggle,
    visibleInitiatives,
    INITIATIVE_FOLD,
    type ChipState,
    type TagChoice,
  } from './lib/chipFilter';

  let {
    tags,
    initiatives,
    value,
    onchange,
    withInitiatives = true,
  }: { tags: readonly TagChoice[]; initiatives: readonly string[]; value: ChipState; onchange: (next: ChipState) => void; withInitiatives?: boolean } = $props();

  let open = $state(false);
  const wide = typeof window.matchMedia !== 'function' || window.matchMedia('(min-width: 768px)').matches;
  let shown = $state(wide);
  const slugs = $derived(initiativeChoices(initiatives, value.initiatives));
  const slugsShown = $derived(visibleInitiatives(slugs, value.initiatives, open));
  const chip = 'min-h-11 rounded-md border border-border bg-surface-raised px-2 text-xs text-text aria-pressed:border-accent aria-pressed:font-semibold aria-pressed:text-accent';
</script>

<div class="flex flex-col gap-2" data-testid="chip-filter">
  <button
    type="button"
    class="flex min-h-11 w-full items-center gap-2 text-left text-sm md:hidden"
    aria-expanded={shown}
    aria-controls="chip-filter-body"
    data-testid="chip-toggle"
    onclick={() => (shown = !shown)}
  >
    <span class="font-medium">Filter</span><span class="text-text-muted">{chipSummary(value, withInitiatives)}</span><span class="ml-auto text-text-muted" aria-hidden="true">{shown ? '▴' : '▾'}</span>
  </button>
  <div id="chip-filter-body" class="flex flex-col gap-2 {shown ? '' : 'hidden'} md:flex">
    {#if tags.length > 0}
      <div role="group" aria-label="Tags, a task must have all of them" class="flex flex-wrap items-center gap-1.5" data-testid="chip-tags">
        <span class="w-20 shrink-0 text-xs text-text-muted md:w-28 md:whitespace-nowrap" title="A task must carry every tag chosen">Tags <span class="whitespace-nowrap">all of</span></span>
        {#each tags as t (t.tag)}
          <button
            type="button"
            class="vh-tag min-h-11"
            style={tagStyle(t.tag)}
            data-source={t.sources.includes('task') ? 'task' : t.sources[0]}
            data-testid="chip-tag"
            data-tag={t.tag}
            aria-pressed={value.tags.includes(t.tag)}
            title="{t.tag}: {chipSourceText(t)}"
            aria-label={chipName(t)}
            onclick={() => onchange({ ...value, tags: toggle(value.tags, t.tag) })}
            >{t.tag}</button
          >
        {/each}
      </div>
    {/if}
    {#if withInitiatives && slugs.length > 0}
      <div role="group" aria-label="Initiatives, a task in any of them" class="flex flex-wrap items-center gap-1.5" data-testid="chip-initiatives">
        <span class="w-20 shrink-0 text-xs text-text-muted md:w-28 md:whitespace-nowrap" title="A task in any initiative chosen">Initiatives <span class="whitespace-nowrap">any of</span></span>
        {#each slugsShown as slug (slug)}
          <button
            type="button"
            class={chip}
            data-testid="chip-initiative"
            data-initiative={slug}
            aria-pressed={value.initiatives.includes(slug)}
            title="{slug}: initiative"
            aria-label="{slug}, initiative"
            onclick={() => onchange({ ...value, initiatives: toggle(value.initiatives, slug) })}
            >{slug}</button
          >
        {/each}
        {#if slugs.length > INITIATIVE_FOLD}
          <button type="button" class="min-h-11 px-2 text-xs text-accent" data-testid="chip-more" aria-expanded={open} onclick={() => (open = !open)}
            >{open ? 'Fewer initiatives' : `${slugs.length - slugsShown.length} more initiatives`}</button
          >
        {/if}
      </div>
    {/if}
    {#if chipsActive(value)}
      <div>
        <button type="button" class="min-h-11 rounded-md border border-border px-3 text-xs text-text" data-testid="chip-clear" onclick={() => onchange({ tags: [], initiatives: [] })}>Clear chips</button>
      </div>
    {/if}
  </div>
</div>
