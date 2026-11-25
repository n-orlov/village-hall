<script lang="ts">
  import { onDestroy, untrack } from 'svelte';
  import { replaceHash } from './lib/historyEntry';
  import ChipFilter from './ChipFilter.svelte';
  import TagChips from './TagChips.svelte';
  import Dialog from './Dialog.svelte';
  import FilterSelect from './FilterSelect.svelte';
  import InitiativeConfirm from './InitiativeConfirm.svelte';
  import SortControl from './SortControl.svelte';
  import Toast from './Toast.svelte';
  import {
    ARCHIVE_WINDOWS,
    ArchiveApi,
    ArchiveStore,
    affectsArchive,
    archiveRows,
    reopenBody,
    reopenLabel,
    reopenedToast,
    rowsAreCurrent,
    type ArchiveRow,
    type ArchiveState,
    type ArchiveWindow,
  } from './lib/archive';
  import { ARCHIVE_COLUMNS, ARCHIVE_SORT_LABELS, ARCHIVE_VIEW, archiveTagChoices, archiveViewHash, queryOfHash, viewFromQuery, viewToQuery, type ArchiveViewState } from './lib/archiveView';
  import { chipsActive, withFreshSources, type ChipState, type TagChoice, type TagMark } from './lib/chipFilter';
  import { InitiativesApi } from './lib/initiatives';
  import { CatalogReads } from './lib/catalogReads';
  import { live } from './lib/live';
  import { hashPath } from './lib/routes';
  import { currentBrowser } from './lib/session';
  import { sortRows, type SortState } from './lib/tableSort';
  import { SourceReads } from './lib/tagCarry';
  import { TasksApi } from './lib/tasks';
  import { currentViewEnv, FilterTurns, openView, saveViewQuery } from './lib/viewState';

  // The Archive view (PRD R7, SPEC 11): the closed tasks and closed initiatives of a window, newest closed first, each
  // with a Reopen button that confirms in the shared Dialog. The window, the shared chip filter (tags AND, initiatives OR,
  // sent to the server) and the shared sort control (Item, Kind, Resolution, Closed) live in the fragment query and are saved under
  // vh.view.v1.archive. The data layer is lib/archive.ts, the view state lib/archiveView.ts.
  let { csrf }: { csrf: string; param?: string } = $props();

  const viewEnv = currentViewEnv();
  // A fragment with a query wins and is saved; a bare one restores the saved query (in place, before the first fetch).
  const bare = queryOfHash(window.location.hash) === '';
  const opened = viewFromQuery(openView(viewEnv, ARCHIVE_VIEW, '#/archive', queryOfHash(window.location.hash)));
  const restored = bare && opened.chips.tags.length > 0;
  const browser = currentBrowser();
  const store = new ArchiveStore(new ArchiveApi(browser, () => csrf), (s) => (view = s), opened.window, opened.chips);
  let sort: SortState = $state(opened.sort);
  let view: ArchiveState = $state.raw(store.state);
  let reopening: ArchiveRow | undefined = $state(undefined);
  let toast = $state('');
  // The initiative chips on offer: every initiative of the board (a closed task may sit in an open one); the tag chips: every tag seen so far plus the chosen ones.
  let initiatives: string[] = $state([]);
  let tagChoices: TagChoice[] = $state([]);
  // Ages are phrased at the moment of each draw; a refetch redraws them.
  // The default order (newest closed first) is the one a sort replaces.
  const rows = $derived(sortRows(archiveRows(view.tasks, view.initiatives, new Date()), sort, ARCHIVE_COLUMNS));

  // A restored tag that no initiative lists is valid when some task of any status carries it (own, or a derived actor kind): the
  // server says, and the same answer names the sources the tag has, so its chip keeps them whatever the closed rows hold. A link's
  // own tags are kept as they are, their chips named the same way in the background. A link, a chip change or a restoration
  // while a check runs is newer and wins; leaving the view drops a check still running.
  const turns = new FilterTurns();
  onDestroy(() => turns.end());
  const tasksApi = new TasksApi(browser, () => csrf);
  const listApi = new InitiativesApi(browser, () => csrf);
  let looked: { slug: string; tags: string[] }[] | undefined;
  let listed: Promise<void> = Promise.resolve();
  // Read on opening and again on every link or restoration with tags, so a chip's initiative source is the catalog's as it is
  // now; a read landing after a newer one is dropped, and a failed one changes nothing.
  const catalog = new CatalogReads(() => listApi.list('all'));
  function readCatalog(): Promise<void> {
    // the chips then offer only the initiatives the fragment names; the list shows its own error
    void catalog.next((list) => {
      initiatives = list.map((i) => i.slug);
      looked = list.map((i) => ({ slug: i.slug, tags: [...i.tags] }));
    });
    return catalog.settled();
  }
  // Every read of a tag's sources (a link's, a restoration's) shares one order: an older answer never overwrites a newer one.
  const sources = new SourceReads((tags) => tasksApi.tagSources(tags));
  const initiativeMarks = (tags: readonly string[]): TagMark[] => {
    const have = new Set((looked ?? []).flatMap((i) => i.tags));
    return tags.filter((tag) => have.has(tag)).map((tag) => ({ tag, source: 'initiative' as const }));
  };
  function offerSources(tags: readonly string[], current: () => boolean) {
    if (tags.length === 0) return;
    // The answer is what each tag carries now, with the catalog read for this link: a source an earlier read showed and the
    // server no longer reports goes. A navigation superseded by the time it lands names nothing: the current one owns the chips.
    void Promise.all([sources.read(tags, [], current), listed]).then(([read]) => {
      if (!current()) return;
      const c = read.fresh();
      tagChoices = withFreshSources(tagChoices, c.answered, [...c.marks, ...initiativeMarks(tags)]);
    });
  }
  // The chips a restored state keeps: the tags some initiative lists or some task carries; the sources of every kept tag are shown.
  async function checkedChips(chips: ChipState, current: () => boolean): Promise<ChipState> {
    await listed;
    // A restore a newer navigation has superseded reads nothing: a read it began now would be the newest for its tags and
    // invalidate the answer of the navigation that is current.
    if (looked === undefined || !current()) return chips;
    const carried = await sources.read(chips.tags, looked, current);
    // Superseded while the lookup was out: the navigation that is current names the chips' sources, not this one.
    if (!current()) return chips;
    const have = new Set([...looked.flatMap((i) => i.tags), ...carried.tags]);
    const tags = chips.tags.filter((tag) => have.has(tag));
    const c = carried.fresh();
    tagChoices = withFreshSources(tagChoices, c.answered, [...c.marks, ...initiativeMarks(chips.tags)]);
    return tags.length === chips.tags.length ? chips : { ...chips, tags };
  }
  async function start() {
    const current = turns.begin();
    listed = readCatalog();
    let chips = opened.chips;
    if (restored) {
      sources.claim(chips.tags);
      chips = await checkedChips(chips, current);
    } else {
      offerSources(chips.tags, current);
    }
    if (!current()) return;
    turns.reading();
    if (chips === opened.chips) {
      void store.load();
    } else {
      remember({ window: view.window, chips, sort });
      void store.navigate(view.window, chips);
    }
  }

  $effect(() => {
    // Read nothing reactive here: a chip change must not run the first load again.
    void untrack(start);
    return live().subscribe(affectsArchive, () => (turns.hasRead() ? store.load() : undefined));
  });

  $effect(() => {
    // Only the rows the current navigation read name sources: rows still shown from an earlier navigation, even one that asked for
    // the same window and chips, would put back a source the current navigation's own reads have taken away.
    const { tasks, initiatives: closed } = rowsAreCurrent(view) ? view : { tasks: [], initiatives: [] };
    const chosen = view.chips.tags;
    tagChoices = untrack(() => archiveTagChoices(tagChoices, tasks, closed, chosen));
  });

  // A change rewrites the fragment in place (no history entry per choice) and saves the query.
  function remember(next: ArchiveViewState) {
    replaceHash(archiveViewHash(next));
    saveViewQuery(viewEnv.storage, ARCHIVE_VIEW, viewToQuery(next));
  }

  // A user's change is the newest navigation: a restoration or a link check still running is superseded, so it neither reads the
  // sources of its tags nor names them nor sets its own filter over this one. The change reads at once, and reads as its own the
  // sources of every chosen tag whose newest read the superseded navigation had not yet applied.
  function userTurn(chips: ChipState) {
    const current = turns.begin();
    turns.reading();
    offerSources(sources.owed(chips.tags), current);
  }

  // A window reads the lists again.
  function choose(next: ArchiveWindow) {
    userTurn(view.chips);
    remember({ window: next, chips: view.chips, sort });
    void store.navigate(next, view.chips);
  }

  // A chip change reads again, the server filtering.
  function setChips(next: ChipState) {
    userTurn(next);
    remember({ window: view.window, chips: next, sort });
    void store.navigate(view.window, next);
  }

  // A sort only reorders the rows the view holds: no request is made.
  function setSort(next: SortState) {
    sort = next;
    remember({ window: view.window, chips: view.chips, sort: next });
  }

  // The fragment edited by hand, or the back button after a link with another query. A bare '#/archive' restores the saved query.
  async function hashChanged() {
    if (hashPath(window.location.hash) !== '#/archive') return;
    const restoring = queryOfHash(window.location.hash) === '';
    const next = viewFromQuery(openView(viewEnv, ARCHIVE_VIEW, '#/archive', queryOfHash(window.location.hash)));
    const current = turns.begin();
    // The navigation begins now, not once its tags are checked: a list an earlier one began lands into nothing meanwhile.
    store.begin();
    let chips = next.chips;
    // The chips' initiative sources (and the restore check) use the catalog as it is now, not as it was on opening.
    if (chips.tags.length > 0) listed = readCatalog();
    if (restoring && chips.tags.length > 0) {
      sources.claim(chips.tags);
      chips = await checkedChips(chips, current);
    } else {
      offerSources(chips.tags, current);
    }
    if (!current()) return; // the fragment changed again while the tags were checked
    sort = next.sort;
    if (chips !== next.chips) remember({ window: next.window, chips, sort });
    turns.reading();
    void store.navigate(next.window, chips);
  }

  function openReopen(row: ArchiveRow) {
    store.clearDialogError();
    reopening = row;
  }

  async function confirm() {
    const row = reopening;
    if (row !== undefined && (await store.reopen(row))) {
      reopening = undefined;
      toast = reopenedToast(row);
    }
  }

  const action = 'inline-flex min-h-11 min-w-11 items-center justify-center rounded-md border border-border bg-surface-raised px-3 text-sm font-medium text-text';
  const link = 'inline-flex min-h-11 min-w-11 items-center font-medium text-accent hover:underline';
  const cell = 'md:table-cell md:px-3 md:py-3 md:align-top';
  const head = 'px-3 py-2 text-sm font-medium text-text-muted';
</script>

<svelte:window onhashchange={hashChanged} />

<section aria-labelledby="archive-heading" class="vh-dense mx-auto w-full max-w-[1280px] pb-16 md:pb-0" data-testid="archive-view">
  <h2 id="archive-heading" class="mb-4 text-xl font-bold">Archive</h2>
  <div class="mb-4 flex flex-wrap items-center gap-x-4 gap-y-2">
    <FilterSelect label="Closed within" options={ARCHIVE_WINDOWS} value={view.window} onchange={choose} testid="archive-window" />
    <SortControl columns={ARCHIVE_SORT_LABELS} {sort} onsort={setSort} />
  </div>
  <div class="mb-4">
    <ChipFilter tags={tagChoices} {initiatives} value={view.chips} onchange={setChips} />
  </div>
  {#if view.error !== ''}
    <p role="alert" class="mb-4 rounded-md border border-danger px-3 py-2 text-sm text-danger" data-testid="archive-error">{view.error}</p>
  {/if}
  {#if !view.loaded && view.error === ''}
    <p class="text-sm text-text-muted" data-testid="archive-loading">Loading the archive…</p>
  {:else if view.loaded && rows.length === 0}
    <p class="rounded-md border border-dashed border-border px-4 py-4 text-text-muted" data-testid="archive-empty">{chipsActive(view.chips) ? 'No closed item matches these chips in this window.' : 'Nothing was closed in this window.'}</p>
  {:else if rows.length > 0}
    <!-- One table: from 768 px a dense table, below it each closed item is a card. -->
    <table class="block w-full text-left wrap-anywhere md:table md:table-fixed md:border-collapse">
      <thead class="sr-only md:table-header-group md:not-sr-only">
        <tr class="md:border-b md:border-border">
          <th scope="col" class="{head} md:w-2/5">Item</th>
          <th scope="col" class="{head} md:w-32">Kind</th>
          <th scope="col" class="{head} md:w-36">Resolution</th>
          <th scope="col" class="{head} md:w-40">Closed</th>
          <th scope="col" class="{head} md:w-28"><span class="sr-only">Action</span></th>
        </tr>
      </thead>
      <tbody class="flex flex-col gap-3 md:table-row-group">
        {#each rows as row (row.key)}
          <tr
            data-testid="archive-row"
            data-kind={row.kind}
            data-ref={row.ref}
            class="flex flex-col gap-2 rounded-md border border-border bg-surface-raised p-4 md:table-row md:rounded-none md:border-0 md:border-b md:bg-transparent md:p-0"
          >
            <td class={cell}>
              <a class={link} href={row.href} data-testid="archive-link">{row.title}</a>
              <br />
              <code class="font-mono text-sm text-text-muted" data-testid="archive-ref">{row.ref}</code>
              {#if row.marks.length > 0}
                <div class="mt-1"><TagChips marks={row.marks} ref={row.ref} /></div>
              {/if}
            </td>
            <td class="text-sm {cell}" data-testid="archive-kind">{row.kind}</td>
            <td class="text-sm {cell}" data-testid="archive-resolution">{row.resolution}</td>
            <td class="text-sm text-text-muted {cell}">
              <time datetime={row.closedAt} title={row.closedTitle} data-testid="archive-closed">{row.closed}</time>
              {#if row.closedBy !== ''}<span data-testid="archive-closed-by"> by {row.closedBy}</span>{/if}
            </td>
            <td class={cell}>
              <button type="button" class={action} aria-label="Reopen {row.kind} {row.ref}" data-testid="archive-reopen" onclick={() => openReopen(row)}>Reopen</button>
            </td>
          </tr>
        {/each}
      </tbody>
    </table>
  {/if}
</section>

{#if reopening !== undefined}
  <Dialog label={reopenLabel(reopening)} onclose={() => (reopening = undefined)} initialFocus="button[data-cancel]">
    <InitiativeConfirm
      heading={reopenLabel(reopening)}
      body={reopenBody(reopening)}
      confirm={reopenLabel(reopening)}
      error={view.dialogError === '' ? undefined : view.dialogError}
      onconfirm={confirm}
      oncancel={() => (reopening = undefined)}
    />
  </Dialog>
{/if}

<Toast message={toast} onclear={() => (toast = '')} />
