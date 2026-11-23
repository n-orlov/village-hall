<script lang="ts">
  import { replaceHash } from './lib/historyEntry';
  import { taskHref } from './lib/taskPage';
  import FilterSelect from './FilterSelect.svelte';
  import NeedsAttention from './NeedsAttention.svelte';
  import NewTaskDialog from './NewTaskDialog.svelte';
  import NowPanel from './NowPanel.svelte';
  import Toast from './Toast.svelte';
  import { InitiativesApi } from './lib/initiatives';
  import { CatalogReads } from './lib/catalogReads';
  import { taskRows } from './lib/initiativePage';
  import { NO_NEEDS, NeedsStore, withoutNeeds, type NeedsState } from './lib/needsAttention';
  import { toneClasses } from './lib/presentation';
  import { currentBrowser } from './lib/session';
  import {
    ATTENTION_OPTIONS,
    DEFAULT_TASKS_FILTER,
    KIND_OPTIONS,
    STATUS_OPTIONS,
    attentionHref,
    flagOptions,
    isFiltered,
    needsFilterQuery,
    type FlagName,
    type TasksFilter,
  } from './lib/taskFilters';
  import { TasksApi, TasksStore, affectsTasks, type TasksState } from './lib/tasks';
  import { live } from './lib/live';
  import { onDestroy, untrack } from 'svelte';
  import ChipFilter from './ChipFilter.svelte';
  import TagChips from './TagChips.svelte';
  import { tagEditor } from './lib/taskTags';
  import type { TaskRow } from './lib/initiativePage';
  import SortControl from './SortControl.svelte';
  import { mergeChoices, withFreshSources, type ChipState, type TagChoice } from './lib/chipFilter';
  import { sortRows, type SortState } from './lib/tableSort';
  import {
    TASKS_VIEW,
    TASK_COLUMNS,
    TASK_SORT_LABELS,
    dropUnknown,
    listedMarks,
    viewFromQuery,
    viewHash,
    viewToQuery,
    type TasksViewState,
  } from './lib/tasksView';
  import { SourceReads, type SourceRead } from './lib/tagCarry';
  import { clearViewQuery, currentViewEnv, FilterTurns, openView, saveViewQuery } from './lib/viewState';

  let { csrf }: { csrf: string; param?: string } = $props();

  const browser = currentBrowser();
  const viewEnv = currentViewEnv();
  const queryOf = (hash: string) => (hash.includes('?') ? hash.slice(hash.indexOf('?') + 1) : '');
  // A fragment with a query wins and is saved; a bare one restores the saved query (in place, before the first fetch).
  const bare = queryOf(window.location.hash) === '';
  const opened = viewFromQuery(openView(viewEnv, TASKS_VIEW, '#/tasks', queryOf(window.location.hash)));
  const initial = opened.filter;
  // A restored initiative may have been deleted since, and a restored tag may no longer be on any task: the first fetch waits for
  // the list of initiatives (and, for a tag, the server's answer) to drop it.
  const checksRestored = (filter: TasksFilter) => filter.initiatives.length > 0 || filter.tags.length > 0;
  const restoredFilter = bare && checksRestored(initial);
  let sort: SortState = $state(opened.sort);
  let view: TasksState = $state({ tasks: [], filter: initial, loading: true, error: undefined });
  let initiatives: string[] = $state([]);
  // The tag chips on offer: every tag seen on the tasks read so far (a filtered list only holds what it matches) plus the chosen ones.
  let tagChoices: TagChoice[] = $state([]);
  // The board's initiatives with their tags once the list answered: an empty answer still drops a restored initiative, only a
  // failed lookup (undefined) keeps it.
  let looked: { slug: string; tags: string[] }[] | undefined;
  let formOpen = $state(false);
  let toast = $state('');

  const api = new TasksApi(browser, () => csrf);
  const store = new TasksStore(api, (s) => (view = s), initial);
  let needsView: NeedsState = $state({ needs: NO_NEEDS, error: undefined });
  const needsStore = new NeedsStore((query) => api.needs(query), (s) => (needsView = s));
  // The section reads under the same filters as the table, plus attention=true; the table below it holds the other tasks.
  const reloadNeeds = () => needsStore.load(needsFilterQuery(view.filter));

  // Rewrites the fragment in place (no history entry per choice) and saves the query under the view's key.
  function persist(state: TasksViewState) {
    replaceHash(viewHash(state));
    saveViewQuery(viewEnv.storage, TASKS_VIEW, viewToQuery(state));
  }

  // A link, an applied filter or Clear filters while the check runs is newer and wins: the late answer is dropped.
  const turns = new FilterTurns();
  // Leaving the view drops a check still running: its late answer must not rewrite the view navigated to, nor read.
  onDestroy(() => turns.end());
  let listed: Promise<void> = Promise.resolve();
  // The board's initiatives, read on opening and again on every link or restoration, so a chip's initiative source is the
  // catalog's as it is now; a read landing after a newer one is dropped, and a failed one changes nothing.
  const catalog = new CatalogReads(() => new InitiativesApi(browser, () => csrf).list('all'));
  function readCatalog() {
    // the select then offers only the initiative the URL names; the list shows the error
    void catalog.next((list) => {
      initiatives = list.map((i) => i.slug);
      looked = list.map((i) => ({ slug: i.slug, tags: [...i.tags] }));
    });
    listed = catalog.settled();
  }
  // A restored tag no initiative lists is valid when some task carries it (own, or a derived actor kind): the server says. The same
  // answer names the sources every restored tag has, so its chip keeps them whatever the combined filter returns.
  // Every read of a tag's sources (a link's, a restoration's) shares one order: an older answer never overwrites a newer one.
  const sources = new SourceReads((tag) => api.tagMarks(tag));
  async function carriedOf(filter: TasksFilter, current: () => boolean): Promise<SourceRead | undefined> {
    return looked === undefined ? undefined : sources.read(filter.tags, looked, current);
  }
  // A link's own tags are kept as they are, but their chips still name the sources the tasks and the initiatives give them: read
  // in the background, so the first read does not wait for it. A failed read leaves the chip as the rows name it.
  function offerSources(tags: readonly string[], current: () => boolean) {
    if (tags.length === 0) return;
    // The answer is what each tag carries now, with the catalog read for this link: a source an earlier read showed and the
    // server no longer reports goes. A navigation superseded by the time it lands names nothing: the current one owns the chips.
    void Promise.all([sources.read(tags, [], current), listed]).then(([read]) => {
      if (!current()) return;
      const c = read.fresh();
      tagChoices = withFreshSources(tagChoices, c.answered, [...c.marks, ...listedMarks(tags, looked ?? [])]);
    });
  }
  function restoredState(state: TasksViewState, carried: SourceRead | undefined): TasksViewState {
    if (looked === undefined) return state;
    const c = carried?.fresh();
    tagChoices = withFreshSources(tagChoices, c?.answered ?? new Set(), [...(c?.marks ?? []), ...listedMarks(state.filter.tags, looked)]);
    return dropUnknown(state, looked, carried?.tags);
  }
  async function start() {
    const current = turns.begin();
    readCatalog();
    let carried: SourceRead | undefined;
    if (restoredFilter) {
      sources.claim(initial.tags);
      await listed;
      // A restore that a newer navigation has superseded reads nothing: a read it began now would be the newest for its tags and
      // invalidate the answer of the navigation that is current.
      if (!current()) return;
      carried = await carriedOf(initial, current);
    } else {
      offerSources(initial.tags, current);
    }
    if (!current()) return;
    turns.reading();
    // Only a restored filter is checked: what a link names stays, and a refusal for it shows in the list.
    const kept = restoredFilter ? restoredState({ filter: initial, sort }, carried) : { filter: initial, sort };
    if (kept.filter === initial) {
      void store.load();
    } else {
      persist(kept);
      void store.setFilter(kept.filter);
    }
    void needsStore.load(needsFilterQuery(kept.filter));
  }

  $effect(() => {
    // Read nothing reactive here: a sort or a filter change must not run the first load again.
    void untrack(start);
    // A change elsewhere refreshes the rows in place under the filter shown: no skeleton, so scroll and an open dialog stay.
    return live().subscribe(affectsTasks, () => (turns.hasRead() ? Promise.all([store.refresh(), reloadNeeds()]) : undefined));
  });

  $effect(() => {
    const marks = [...view.tasks, ...needsView.needs.items].flatMap((task) => task.tag_marks);
    const chosen = view.filter.tags;
    tagChoices = untrack(() => mergeChoices(tagChoices, marks, chosen));
  });

  // The filters and the sort live in the fragment and in the saved query: a change fetches (the sort only reorders). It is the
  // newest navigation, so it reads as its own the sources of every chosen tag whose newest read a superseded one had not applied.
  function apply(filter: TasksFilter) {
    const current = turns.begin();
    turns.reading();
    offerSources(sources.owed(filter.tags), current);
    persist({ filter, sort });
    void store.setFilter(filter);
    void reloadNeeds();
  }

  function setSort(next: SortState) {
    sort = next;
    persist({ filter: view.filter, sort });
  }

  // Clear filters: the defaults, and nothing saved.
  function clear() {
    turns.begin();
    turns.reading();
    sort = null;
    clearViewQuery(viewEnv.storage, TASKS_VIEW);
    replaceHash('#/tasks');
    void store.setFilter(DEFAULT_TASKS_FILTER);
    void reloadNeeds();
  }

  // The fragment edited by hand, or the back button after a link with another query. A bare `#/tasks` restores the saved query.
  async function hashChanged() {
    const hash = window.location.hash;
    if (hash !== '#/tasks' && !hash.startsWith('#/tasks?')) return;
    const restoring = queryOf(hash) === '';
    let next = viewFromQuery(openView(viewEnv, TASKS_VIEW, '#/tasks', queryOf(hash)));
    const current = turns.begin();
    // The chips' initiative sources (and the restore check) use the catalog as it is now, not as it was on opening.
    if (next.filter.tags.length > 0) readCatalog();
    // A restored filter waits for the board's list and the tags' check like the first one does; a link's own query does not.
    const checking = restoring && checksRestored(next.filter);
    let carried: SourceRead | undefined;
    if (checking) {
      sources.claim(next.filter.tags);
      await listed;
      // A restore that a newer navigation has superseded reads nothing: a read it began now would be the newest for its tags and
      // invalidate the answer of the navigation that is current.
      if (!current()) return;
      carried = await carriedOf(next.filter, current);
    } else {
      offerSources(next.filter.tags, current);
    }
    if (!current()) return; // the fragment (or the filter) changed again while the list was read
    if (checking) {
      const kept = restoredState(next, carried);
      if (kept !== next) persist(kept);
      next = kept;
    }
    sort = next.sort;
    if (!turns.mustRead(viewToQuery({ filter: next.filter, sort: null }) === viewToQuery({ filter: view.filter, sort: null }))) return;
    turns.reading();
    void store.setFilter(next.filter);
    void reloadNeeds();
  }

  // The tags on offer when typing a new one: every tag the list shows or has shown, and the operator's edits read the list again.
  const inUse = $derived(tagChoices.map((c) => c.tag));
  const reread = () => Promise.all([store.refresh(), reloadNeeds()]);
  // A closed task takes no tag change (the server refuses it), so its chips have no controls.
  const editorOf = (row: TaskRow) => (row.status === 'closed' ? undefined : tagEditor(api, row.ref, row.marks, inUse, reread));
  const setChips = (chips: ChipState) => apply({ ...view.filter, ...chips });
  const setFlag = (name: FlagName, value: string) => apply({ ...view.filter, [name]: value });
  const needs = $derived(needsView.needs);
  const needsRows = $derived(taskRows(needs.items, 'all'));
  // The table sorts by the chosen column; the Needs attention section above it keeps its own order.
  const rows = $derived(sortRows(taskRows(withoutNeeds(view.tasks, needs), 'all'), sort, TASK_COLUMNS));
  const empty = $derived(!view.loading && view.error === undefined && rows.length === 0 && needsRows.length === 0);

  function created(ref: string) {
    formOpen = false;
    toast = `Task ${ref} created`;
    if (!turns.hasRead()) return; // the first read, still waiting on its check, shows the new task
    void store.load();
    void reloadNeeds();
  }

  const primary = 'min-h-11 rounded-md border border-accent bg-surface-raised px-4 text-sm font-medium text-accent';
  const badge = 'inline-block rounded-md border px-2 py-0.5 text-xs font-medium';
  const cell = 'md:table-cell md:px-3 md:py-3';
  const head = 'px-3 py-2 text-sm font-medium text-text-muted';
</script>

<svelte:window onhashchange={hashChanged} />

<!-- From 1536 px: the heading and the rest in the main column, the Now panel in a 400 px side column that sticks at the top and scrolls on its own; the pair is at most 2560 px wide, centred. Below it the panel sits above the filters. -->
<section
  aria-labelledby="tasks-heading"
  class="vh-dense mx-auto w-full max-w-[2560px] pb-16 md:pb-0 2xl:grid 2xl:grid-cols-[minmax(0,1fr)_400px] 2xl:gap-x-6"
  data-testid="tasks-view"
>
  <div class="mb-4 flex items-center justify-between gap-3 2xl:col-start-1 2xl:row-start-1">
    <h2 id="tasks-heading" class="text-xl font-bold">Tasks</h2>
    <button type="button" class="{primary} hidden md:inline-flex md:items-center" onclick={() => (formOpen = true)}>New task</button>
  </div>

  <div
    class="2xl:sticky 2xl:top-6 2xl:col-start-2 2xl:row-span-2 2xl:row-start-1 2xl:max-h-[calc(100vh-3rem)] 2xl:self-start 2xl:overflow-y-auto"
    data-testid="now-side"
  >
    <NowPanel {csrf} />
  </div>

  <div class="min-w-0 2xl:col-start-1 2xl:row-start-2" data-testid="tasks-main">
  <div class="mb-4 flex flex-wrap items-center gap-x-4 gap-y-2" data-testid="task-filters">
    <FilterSelect label="Status" options={STATUS_OPTIONS} value={view.filter.status} onchange={(v) => apply({ ...view.filter, status: v })} testid="status-filter" />
    <FilterSelect label="Kind" options={KIND_OPTIONS} value={view.filter.kind} onchange={(v) => apply({ ...view.filter, kind: v })} testid="kind-filter" />
    <FilterSelect label="Active" options={flagOptions('active')} value={view.filter.active} onchange={(v) => setFlag('active', v)} testid="active-filter" />
    <FilterSelect label="Blocked" options={flagOptions('blocked')} value={view.filter.blocked} onchange={(v) => setFlag('blocked', v)} testid="blocked-filter" />
    <FilterSelect label="Overdue" options={flagOptions('overdue')} value={view.filter.overdue} onchange={(v) => setFlag('overdue', v)} testid="overdue-filter" />
    <FilterSelect label="Attention" options={ATTENTION_OPTIONS} value={view.filter.attention} onchange={(v) => setFlag('attention', v)} testid="attention-filter" />
    <label class="flex items-center gap-2 text-sm text-text-muted">
      <span>Search</span>
      <input
        type="search"
        class="min-h-11 w-56 max-w-full rounded-md border border-border bg-surface-raised px-3 text-base text-text"
        data-testid="text-filter"
        value={view.filter.text}
        onchange={(e) => apply({ ...view.filter, text: e.currentTarget.value.trim() })}
      />
    </label>
    <SortControl columns={TASK_SORT_LABELS} {sort} onsort={setSort} />
    {#if isFiltered(view.filter)}
      <button type="button" class="min-h-11 rounded-md border border-border px-4 text-sm font-medium text-text" data-testid="clear-filters" onclick={clear}>Clear filters</button>
    {/if}
  </div>

  <div class="mb-4">
    <ChipFilter tags={tagChoices} {initiatives} value={{ tags: view.filter.tags, initiatives: view.filter.initiatives }} onchange={setChips} />
  </div>

  {#if view.error !== undefined}
    <p role="alert" class="mb-4 rounded-md border border-danger px-3 py-2 text-sm text-danger">{view.error}</p>
  {/if}

  {#if needsView.error !== undefined}
    <p role="alert" class="mb-4 rounded-md border border-danger px-3 py-2 text-sm text-danger" data-testid="needs-error">{needsView.error}</p>
  {/if}

  {#if !view.loading}
    <NeedsAttention rows={needsRows} more={needs.more} moreHref={attentionHref(view.filter)} showInitiative editor={editorOf} />
  {/if}

  {#if view.loading}
    <div aria-busy="true" class="flex flex-col gap-3" data-testid="tasks-skeleton">
      <span class="sr-only">Loading tasks…</span>
      {#each [0, 1, 2, 3] as n (n)}
        <div class="flex flex-col gap-2 rounded-md border border-border bg-surface-raised p-4">
          <div class="h-4 w-2/3 animate-pulse rounded-md bg-border"></div>
          <div class="h-4 w-1/3 animate-pulse rounded-md bg-border"></div>
        </div>
      {/each}
    </div>
  {:else if empty}
    <p class="rounded-md border border-dashed border-border px-4 py-8 text-center text-text-muted" data-testid="no-tasks">
      {isFiltered(view.filter) ? 'No tasks match these filters.' : 'No tasks yet.'}
    </p>
  {:else if rows.length > 0}
    <!-- One table: from 768 px a dense table, below it each row is a card. -->
    <table class="block w-full text-left wrap-anywhere md:table md:table-fixed md:border-collapse">
      <thead class="sr-only md:table-header-group md:not-sr-only">
        <tr class="md:border-b md:border-border">
          <th scope="col" class="{head} md:w-32">Ref</th>
          <th scope="col" class={head}>Title</th>
          <th scope="col" class="{head} md:w-32">Initiative</th>
          <th scope="col" class="{head} md:w-20">Kind</th>
          <th scope="col" class="{head} md:w-24">Status</th>
          <th scope="col" class="{head} md:w-1/6">Flags</th>
          <th scope="col" class="{head} md:w-28">Lease holder</th>
        </tr>
      </thead>
      <tbody class="flex flex-col gap-3 md:table-row-group">
        {#each rows as row (row.id)}
          <tr
            data-testid="task-row"
            data-ref={row.ref}
            class="flex flex-wrap items-center gap-x-3 gap-y-2 rounded-md border border-border bg-surface-raised p-4 md:table-row md:rounded-none md:border-0 md:border-b md:bg-transparent md:p-0"
          >
            <td class="font-mono text-sm {cell}" data-testid="task-ref"><a class="inline-flex min-h-11 min-w-11 items-center text-accent hover:underline" href={taskHref(row.ref)}>{row.ref}</a></td>
            <td class="order-last w-full min-w-0 font-medium md:order-none {cell}">
              <span data-testid="task-title">{row.title}</span>
              {#if row.marks.length > 0 || editorOf(row) !== undefined}
                <div class="mt-1"><TagChips marks={row.marks} ref={row.ref} editor={editorOf(row)} /></div>
              {/if}
            </td>
            <td class="text-sm {cell}"><a class="inline-flex min-h-11 min-w-11 max-w-full items-center text-accent wrap-anywhere hover:underline" href="#/i/{row.initiative}" data-testid="task-initiative">{row.initiative}</a></td>
            <td class="text-sm {cell}" data-testid="task-kind">{row.kind}</td>
            <td class={cell} data-testid="task-status"><span class="{badge} border-border text-text-muted">{row.status}</span></td>
            <td class="flex flex-wrap gap-1 empty:hidden {cell}" data-testid="task-flags">
              {#each row.attention as reason (reason.reason)}
                <span class="{badge} {toneClasses(reason.tone)}" data-testid="attention-{reason.reason}">
                  <span aria-hidden="true">{reason.label}</span><span class="sr-only">{reason.name}</span>
                </span>
              {/each}
              {#each row.badges as flag (flag.key)}
                <span class="{badge} {toneClasses(flag.tone)}" data-testid="flag-{flag.key}">{flag.label}</span>
              {/each}
            </td>
            <td class="text-sm empty:hidden {cell}" data-testid="task-holder">{row.holder}</td>
          </tr>
        {/each}
      </tbody>
    </table>
  {/if}
  </div>

  <button
    type="button"
    class="{primary} fixed inset-x-4 bottom-[calc(3.5rem+env(safe-area-inset-bottom)+0.75rem)] z-20 shadow-lg md:hidden"
    onclick={() => (formOpen = true)}>New task</button
  >
</section>

<Toast message={toast} onclear={() => (toast = '')} />

{#if formOpen}
  <NewTaskDialog {csrf} initiative="" onclose={() => (formOpen = false)} oncreated={created} />
{/if}
