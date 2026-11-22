<script lang="ts">
  import { pushHash, replaceHash } from './lib/historyEntry';
  import { TimelineEntry } from './lib/timelineEntry';
  import { onDestroy, tick, untrack } from 'svelte';
  import { DataSet } from 'vis-data/peer';
  import { Timeline } from 'vis-timeline/peer';
  import 'vis-timeline/styles/vis-timeline-graph2d.min.css';
  import { domSurface } from './arrowSurface';
  import { currentBrowser } from './lib/session';
  import { resolveRoute, ROUTES } from './lib/routes';
  import { currentViewEnv, FilterTurns } from './lib/viewState';
  import FilterSelect from './FilterSelect.svelte';
  import ChipFilter from './ChipFilter.svelte';
  import SortControl from './SortControl.svelte';
  import SegmentedRadio from './SegmentedRadio.svelte';
  import { LABEL_DEFAULT, clampLabelWidth, labelMax, labelWidthForKey, loadLabelWidth, saveLabelWidth, LABEL_MIN } from './lib/labelWidth';
  import { WINDOWS, loadWindow, saveWindow, type NowWindow } from './lib/nowFlight';
  import { ACTIVE_CHOICES, ACTIVE_COLUMNS, WINDOW_HINT, windowVisible, type TimelineOrder } from './lib/timelineActive';
  import type { SortState } from './lib/tableSort';
  import { withFreshSources, type ChipState, type TagChoice } from './lib/chipFilter';
  import TaskPreview from './TaskPreview.svelte';
  import { InitiativesApi } from './lib/initiatives';
  import { CatalogReads } from './lib/catalogReads';
  import { TasksApi } from './lib/tasks';
  import { currentRows, watchRows, rowsName, type RowsEnv } from './lib/timelineRows';
  import { SourceReads, type SourceRead } from './lib/tagCarry';
  import { live } from './lib/live';
  import {
    CLOSED_WITHIN_OPTIONS,
    DEFAULT_TIMELINE_FILTER,
    TIMELINE_LEGEND,
    laneStyle,
    type RowGeometry,
    affectsTimeline,
    choiceOptions,
    isFiltered,
    tagChoices,
    timelineHash,
    withClosedWithin,
    type TimelineFilter,
    type TimelineModel,
  } from './lib/timeline';
  import type { ArrowSurface } from './lib/timelineArrows';
  import {
    canZoomIn,
    canZoomOut,
    chartHeight,
    mountChart,
    sameRange,
    timelineViewHash,
    spanLabel,
    forgetTimeline,
    openTimeline,
    saveTimeline,
    startFilter,
    TimelineApi,
    TimelineViewStore,
    type Chart,
    type ChartLib,
    type TimeWindow,
    type TimelineViewState,
  } from './lib/timelineView';

  // The Timeline view (PRD R6): initiatives and their tasks over time, drawn by vis-timeline (bundled with the app,
  // no CDN). The data layer and the chart glue are lib/timelineView.ts, the mapping is lib/timeline.ts, the dependency
  // arrows are lib/timelineArrows.ts drawn on arrowSurface.ts.
  let { csrf }: { csrf: string; param?: string } = $props();

  const browser = currentBrowser();
  const api = new TimelineApi(browser, () => csrf);
  const lib = { DataSet, Timeline } as unknown as ChartLib;
  const viewEnv = currentViewEnv();
  // A fragment with a query wins and is saved; a bare one restores the saved filter (in place, before the first fetch).
  const opened = openTimeline(viewEnv, window.location.hash);
  // A fragment that names a window (`from`/`to`: a link, or Back to this entry) opens on it; otherwise the default one.
  const store = new TimelineViewStore(api, (s) => (view = s), opened.filter, undefined, undefined, opened.window, loadWindow(viewEnv.storage));
  /** Whether the fragment carries the window: once it named one or the user moved the chart, so a fresh visit keeps the default. */
  let windowInHash = opened.window !== undefined;
  /**
   * The task previewed over the chart (`task` in the fragment). Opening one pushes a history entry, so Back closes it;
   * a reload or a link opens it again over the same window.
   */
  let preview: string | undefined = $state(opened.task);
  let view: TimelineViewState = $state.raw(store.state);
  let frame: HTMLDivElement | undefined = $state();
  let section: HTMLElement | undefined = $state();
  let chart: Chart | undefined;
  // Which initiatives are collapsed and how far the rows are scrolled belong to the history entry the chart is displayed
  // on, and are saved under that entry's key, however late the report comes (see lib/timelineEntry.ts).
  const entry = new TimelineEntry(
    window,
    () => chart,
    () => resolveRoute(window.location.hash, ROUTES).id === 'timeline',
  );
  /** The model the chart holds; a state that changes only the loading flag or the error leaves the chart alone. */
  let shown: TimelineModel | undefined;
  /** The visible range's length in milliseconds, 0 until the chart has drawn. */
  let span = $state(0);
  /** The visible window, for the readout's data attributes (the range the wheel, a pinch and a drag moved to). */
  let shownFrom = $state(0);
  let shownTo = $state(0);

  // The arrows go on whatever chart the frame holds now; the frame is bound before the chart is mounted.
  const arrows: ArrowSurface = {
    boxOf: (itemId) => domSurface(frame!)?.boxOf(itemId),
    render: (paths) => domSurface(frame!)?.render(paths),
  };

  /** What the filter selects offer: the board's initiatives, their tags and its actors. */
  let slugs: string[] = $state([]);
  let boardInitiatives: { tags: string[] }[] = $state([]);
  // The tag chips: the initiatives' tags and every tag of the task rows drawn, kept while a narrower read follows.
  let seen: TagChoice[] = $state([]);
  $effect(() => {
    const rows = view.model.groups;
    const chosen = view.filter.tags;
    seen = untrack(() => tagChoices([], chosen, rows, seen));
  });
  const tags = $derived(tagChoices(boardInitiatives, view.filter.tags, [], seen));
  let actors: string[] = $state([]);

  // The lookup's answer, or undefined until it answers (or when it failed): an empty answer still drops restored values.
  let looked: { slug: string; tags: string[] }[] | undefined;

  // A restored filter may name an initiative or a tag the board no longer has: the first fetch waits for the list to drop them.
  // A link, an applied filter or Clear filters while the check runs is newer and wins: the late answer is dropped.
  const turns = new FilterTurns();
  // Leaving the view drops a check still running: its late answer must not rewrite the view navigated to, nor read.
  onDestroy(() => turns.end());
  let listed: Promise<void> = Promise.resolve();
  // The board's initiatives, read on opening and again on every link or restoration with tags, so a chip's initiative source is
  // the catalog's as it is now; a read landing after a newer one is dropped, and a failed one changes nothing.
  const catalog = new CatalogReads(() => new InitiativesApi(browser, () => csrf).list('all'));
  function readCatalog() {
    // the selects then offer only what the URL names; the chart shows the error
    void catalog.next((list) => {
      looked = list.map((i) => ({ slug: i.slug, tags: i.tags }));
      slugs = list.map((i) => i.slug);
      boardInitiatives = list.map((i) => ({ tags: i.tags }));
    });
    listed = catalog.settled();
  }
  // The restored tags no initiative lists are valid when some task carries them (own, or a derived actor kind): the server says.
  const tasksApi = new TasksApi(browser, () => csrf);
  // Every read of a tag's sources (a link's, a restoration's) shares one order: an older answer never overwrites a newer one.
  const sources = new SourceReads((tag) => tasksApi.tagMarks(tag));
  async function carriedOf(filter: TimelineFilter, current: () => boolean): Promise<SourceRead | undefined> {
    return looked === undefined ? undefined : sources.read(filter.tags, looked, current);
  }
  // A link's own tags are kept as they are, but their chips still name the sources the tasks give them: read in the background,
  // so the board's first read does not wait for it. A failed read leaves the chip as the view's own rows name it.
  function offerSources(tags: readonly string[], current: () => boolean) {
    if (tags.length === 0) return;
    void sources.read(tags, [], current).then((read) => {
      // A navigation superseded by the time it lands names nothing: the current one owns the chips.
      if (!current()) return;
      // The answer is what each tag carries now: a source an earlier read showed and the server no longer reports goes.
      const c = read.fresh();
      seen = withFreshSources(seen, c.answered, c.marks);
    });
  }
  async function start() {
    const current = turns.begin();
    readCatalog();
    void api
      .actors()
      .then((handles) => (actors = handles))
      .catch(() => undefined);
    let carried: SourceRead | undefined;
    if (opened.restored) {
      sources.claim(opened.filter.tags);
      await listed;
      // A restore that a newer navigation has superseded reads nothing: a read it began now would be the newest for its tags and
      // invalidate the answer of the navigation that is current.
      if (!current()) return;
      carried = await carriedOf(opened.filter, current);
    } else {
      offerSources(opened.filter.tags, current);
    }
    if (!current()) return;
    // The restored chips name the sources the tasks give them, whatever the combined filter then returns.
    if (carried !== undefined) {
      const c = carried.fresh();
      seen = withFreshSources(seen, c.answered, c.marks);
    }
    turns.reading();
    const kept = startFilter(opened, looked, carried?.tags);
    if (kept === opened.filter) {
      void store.load();
    } else {
      persist(kept);
      void store.setFilter(kept);
    }
  }

  $effect(() => {
    // Read nothing reactive here: a filter change must not run the first load again.
    void untrack(start);
    // A lease, task, ask or milestone event refetches in place: the window and the collapsed rows stay.
    const unsubscribe = live().subscribe(affectsTimeline, () => (turns.hasRead() ? store.load() : undefined));
    return () => {
      unsubscribe();
      store.cancelPan();
      chart?.destroy(); // reports what it displays to the entry it was shown on first
    };
  });

  // The filter lives in the fragment and in the saved query: a choice rewrites both in place (no history entry per choice).
  function persist(filter: TimelineFilter) {
    replaceHash(timelineViewHash(filter, windowInHash ? view.window : undefined, preview));
    saveTimeline(viewEnv.storage, filter);
  }

  // Active first is the shared sort control's one option beside the current order; Only active is a checkbox. Both ride in
  // the filter (fragment and saved query). The touched-in window is Now's choice, kept under Now's key.
  const sort: SortState = $derived(view.filter.order === 'active' ? { column: 'active', dir: 'desc' } : null);
  const chooseSort = (next: SortState) => apply({ ...view.filter, order: ((next === null ? 'default' : 'active') satisfies TimelineOrder) });
  const showWindow = $derived(windowVisible(view.filter.activeOnly, view.filter.order));
  function chooseActiveWindow(next: NowWindow) {
    saveWindow(viewEnv.storage, next);
    void store.setActiveWindow(next);
  }

  const setChips = (chips: ChipState) => apply({ ...view.filter, ...chips });
  // A change is the newest navigation: it reads as its own the sources of every chosen tag whose newest read a superseded one
  // had not applied.
  function apply(filter: TimelineFilter) {
    const current = turns.begin();
    turns.reading();
    offerSources(sources.owed(filter.tags), current);
    persist(filter);
    void store.setFilter(filter);
  }

  // Clear filters: the defaults, and nothing saved.
  function clear() {
    turns.begin();
    turns.reading();
    forgetTimeline(viewEnv.storage);
    replaceHash('#/timeline');
    void store.setFilter(DEFAULT_TIMELINE_FILTER);
  }

  // The fragment edited by hand, or the back button after a link with another query. A bare fragment restores the saved filter.
  async function hashChanged() {
    if (resolveRoute(window.location.hash, ROUTES).id !== 'timeline') return;
    const next = openTimeline(viewEnv, window.location.hash);
    preview = next.task; // Back from a preview's entry closes it; Forward opens it again
    const current = turns.begin();
    // The chips' initiative sources (and the restore check) use the catalog as it is now, not as it was on opening.
    if (next.filter.tags.length > 0) readCatalog();
    // A restored filter waits for the board's list like the first one does; a link's own query does not.
    let carried: SourceRead | undefined;
    if (next.restored) {
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
    if (carried !== undefined) {
      const c = carried.fresh();
      seen = withFreshSources(seen, c.answered, c.marks);
    }
    const filter = startFilter(next, looked, carried?.tags);
    if (next.window !== undefined && !sameRange(next.window, view.window)) {
      windowInHash = true;
      chart?.moveTo(next.window);
    }
    if (filter !== next.filter) persist(filter);
    if (!turns.mustRead(timelineHash(filter) === timelineHash(view.filter))) return;
    turns.reading();
    void store.setFilter(filter);
  }

  // The chart is mounted once, then handed each new model. Only the frame and the model are dependencies: what the chart
  // does while it draws (the span, the arrows) must not run this again, or it would reset what the user collapsed.
  $effect(() => {
    const target = frame;
    const model = view.model;
    if (target === undefined || !view.loaded) return;
    untrack(() => {
      const hooks = {
        open: openPreview,
        arrows,
        onSpan: (ms: number) => (span = ms),
        onRange: (range: TimeWindow) => store.panTo(range),
        onWindow: (range: TimeWindow) => {
          shownFrom = range.from.getTime();
          shownTo = range.to.getTime();
        },
        onRowScroll: (px: number) => entry.saveRows(px),
        // Back, Forward and a reload show the entry's collapsed initiatives again, and a visit with no entry of its own
        // (the nav, a new tab) starts with every initiative expanded.
        collapsed: entry.collapsed(),
        onCollapse: (ids: string[]) => entry.saveCollapsed(ids),
      };
      if (chart === undefined) {
        chart = mountChart(lib, target, model, view.window, hooks, heightFor(target));
        const rows = entry.rows();
        if (rows !== undefined) chart.scrollRows(rows);
        void placeHandle();
      } else if (model !== shown) chart.show(model);
      shown = model;
    });
  });

  // A move to another history entry first saves what the chart displays to the entry being left. The router unmounts the
  // view on the same event (and Svelte drops its rows before the chart is destroyed), so this runs in the capture phase,
  // ahead of the router, while the rows are still on the page.
  $effect(() => {
    const moved = () => entry.navigated();
    window.addEventListener('popstate', moved, true);
    return () => window.removeEventListener('popstate', moved, true);
  });

  // The window the store last read (a pan or zoom that settled) goes in the fragment, so Back and a shared link show it.
  let lastWindow = untrack(() => view.window);
  $effect(() => {
    const window = view.window;
    if (window === lastWindow) return;
    lastWindow = window;
    windowInHash = true;
    untrack(() => replaceHash(timelineViewHash(view.filter, window, preview)));
  });

  // A tap on a bar previews its task in a dialog, on a new history entry of the same view.
  function openPreview(ref: string) {
    if (preview !== undefined) return;
    entry.leaving(); // the entry pushed starts as a copy of this one's saved state: save the latest first
    pushHash(timelineViewHash(view.filter, windowInHash ? view.window : undefined, ref), { preview: true });
  }

  // Closing goes Back when the preview is on an entry the page pushed for it (its history.state flag, which a reload keeps);
  // a deep-linked preview, whose entry has no flag, is dropped from the fragment in place, so Back still leaves the view.
  function closePreview() {
    const state: unknown = window.history.state;
    if (typeof state === 'object' && state !== null && (state as { preview?: unknown }).preview === true) {
      window.history.back();
      return;
    }
    preview = undefined;
    replaceHash(timelineViewHash(view.filter, windowInHash ? view.window : undefined));
  }

  // The rows follow the density preference and the viewport: tight in Compact from 768 px, roomy otherwise. A change is
  // drawn into the frame's custom properties first, then vis-timeline measures the labels again.
  const desktopQuery = window.matchMedia('(min-width: 768px)');
  const rowsEnv: RowsEnv = {
    compact: () => document.documentElement.getAttribute('data-density') !== 'sparse',
    desktop: () => desktopQuery.matches,
    subscribe: (callback) => {
      const attributes = new MutationObserver(callback);
      attributes.observe(document.documentElement, { attributes: true, attributeFilter: ['data-density'] });
      desktopQuery.addEventListener('change', callback);
      return () => {
        attributes.disconnect();
        desktopQuery.removeEventListener('change', callback);
      };
    },
  };
  let rowShape: RowGeometry = $state.raw(currentRows(rowsEnv)); // right from the first draw: no roomy flash that the chart then redraws
  $effect(() =>
    watchRows(rowsEnv, (next) => {
      if (untrack(() => next === rowShape)) return;
      rowShape = next;
      void tick().then(() => {
        chart?.redraw();
        placeHandle();
      });
    }),
  );

  /** The chart's tallest: the screen below the chart's top, less the phone's tab bar, so the axis stays in view as rows scroll. */
  function heightFor(target: HTMLElement): number {
    const top = target.getBoundingClientRect().top + window.scrollY;
    const tabBar = window.matchMedia('(min-width: 768px)').matches ? 16 : 80;
    return chartHeight(window.innerHeight, top, tabBar);
  }

  function resized() {
    if (chart !== undefined && frame !== undefined) chart.setHeight(heightFor(frame));
    void setLabelWidth(labelWidth); // a narrower window may no longer hold the width
  }

  // What sits above the chart changes after it is drawn (the board's initiative chips arrive after the rows, "more" opens
  // and folds, a chosen chip wraps): the chart keeps the screen below its top, so it is measured again whenever its top moves.
  $effect(() => {
    if (section === undefined || frame === undefined) return;
    const target = frame;
    let top = target.getBoundingClientRect().top + window.scrollY;
    const watch = new ResizeObserver(() => {
      const now = target.getBoundingClientRect().top + window.scrollY;
      if (now === top) return; // the chart's own height moves the section's, not its top
      top = now;
      if (chart !== undefined) chart.setHeight(heightFor(target));
    });
    watch.observe(section);
    return () => watch.disconnect();
  });

  // The label column (md and wider; a phone keeps its capped column): a separator beside it resizes it by drag or arrow keys,
  // double click resets it; the width is held between a minimum and a share of the chart and remembered per browser.
  let labelWidth = $state(loadLabelWidth(viewEnv.storage));
  /** The label panel's right edge inside the frame, where the handle sits. */
  let labelEdge = $state(0);
  let chartW = $state(1000);
  let watched: Element | undefined;
  const panelWatch = new ResizeObserver(() => placeHandle());
  onDestroy(() => panelWatch.disconnect());
  /** Puts the handle on the edge vis-timeline drew; it follows the panel however vis-timeline resizes it. */
  function placeHandle() {
    if (frame === undefined) return;
    const left = frame.querySelector('.vis-panel.vis-left');
    if (left === null) return;
    if (left !== watched) {
      watched = left;
      panelWatch.observe(left);
    }
    chartW = frame.clientWidth;
    labelEdge = left.getBoundingClientRect().right - frame.getBoundingClientRect().left;
  }
  async function setLabelWidth(px: number, save = false) {
    chartW = frame?.clientWidth ?? chartW;
    labelWidth = clampLabelWidth(px, chartW);
    if (save) saveLabelWidth(viewEnv.storage, labelWidth);
    await tick(); // the frame's --tl-label-w first, then vis-timeline measures the labels
    chart?.redraw();
    placeHandle();
  }
  function dragLabel(e: PointerEvent) {
    const bar = e.currentTarget as HTMLElement;
    const x0 = e.clientX;
    const w0 = labelWidth;
    bar.setPointerCapture(e.pointerId);
    const move = (m: PointerEvent) => void setLabelWidth(w0 + m.clientX - x0);
    const up = () => {
      bar.removeEventListener('pointermove', move);
      bar.removeEventListener('pointerup', up);
      bar.removeEventListener('pointercancel', up);
      saveLabelWidth(viewEnv.storage, labelWidth);
    };
    bar.addEventListener('pointermove', move);
    bar.addEventListener('pointerup', up);
    bar.addEventListener('pointercancel', up);
    e.preventDefault();
  }
  function labelKey(e: KeyboardEvent) {
    const next = labelWidthForKey(e.key, labelWidth, chartW);
    if (next === undefined) return;
    e.preventDefault();
    void setLabelWidth(next, true);
  }

  const button = 'inline-flex min-h-11 min-w-11 items-center justify-center rounded-md border border-border bg-surface-raised px-3 text-sm font-medium text-text enabled:hover:bg-surface disabled:opacity-60';
</script>

<svelte:window onhashchange={hashChanged} onresize={resized} onpagehide={() => entry.leaving()} />

<section aria-labelledby="timeline-heading" class="vh-dense w-full pb-16 md:pb-0" data-testid="timeline-view" bind:this={section}>
  <h2 id="timeline-heading" class="mb-4 text-xl font-bold">Timeline</h2>
  <div class="mb-4 flex flex-wrap items-center gap-x-4 gap-y-2" data-testid="timeline-filters">
    <div class="w-full">
      <ChipFilter {tags} initiatives={slugs} value={{ tags: view.filter.tags, initiatives: view.filter.initiatives }} onchange={setChips} />
    </div>
    <FilterSelect label="Actor" options={choiceOptions(actors, view.filter.actor, 'Any actor')} value={view.filter.actor} onchange={(v) => apply({ ...view.filter, actor: v })} testid="actor-filter" />
    <FilterSelect
      label="Closed within"
      options={CLOSED_WITHIN_OPTIONS.map((o) => ({ value: String(o.value), label: o.label }))}
      value={String(view.filter.closedWithin)}
      onchange={(v) => apply(withClosedWithin(view.filter, v))}
      testid="closed-filter"
    />
    <SortControl columns={ACTIVE_COLUMNS} choices={ACTIVE_CHOICES} {sort} onsort={chooseSort} testid="timeline-sort" />
    <!-- The checkbox itself covers the whole 44 px pill (invisible, as in SegmentedRadio), so it is its own hit area. -->
    <label
      class="relative flex min-h-11 cursor-pointer items-center gap-2 rounded-md border border-border px-3 text-sm text-text has-[:checked]:border-accent has-[:checked]:bg-surface has-[:checked]:font-semibold has-[:focus-visible]:outline-2 has-[:focus-visible]:outline-offset-2 has-[:focus-visible]:outline-accent"
    >
      <input
        type="checkbox"
        class="absolute -inset-px h-[calc(100%+2px)] w-[calc(100%+2px)] cursor-pointer text-base opacity-0"
        data-testid="only-active"
        checked={view.filter.activeOnly}
        onchange={(event) => apply({ ...view.filter, activeOnly: event.currentTarget.checked })}
      />
      <span aria-hidden="true" class="inline-flex h-5 w-5 items-center justify-center rounded border border-border text-xs {view.filter.activeOnly ? 'border-accent bg-accent text-surface-raised' : ''}">{view.filter.activeOnly ? '✓' : ''}</span>
      Only active
    </label>
    {#if showWindow}
      <div data-testid="timeline-window-control">
        <SegmentedRadio legend="Touched within" name="timeline-window" options={WINDOWS} value={view.activeWindow} onchoose={chooseActiveWindow} testid="timeline-window" inline />
      </div>
    {/if}
    {#if isFiltered(view.filter)}
      <button type="button" class="min-h-11 rounded-md border border-border px-4 text-sm font-medium text-text" data-testid="clear-filters" onclick={clear}>Clear filters</button>
    {/if}
    {#if showWindow}
      <!-- Its own line under the controls, so they keep one row (one centre line) on a desktop. -->
      <p class="w-full text-xs text-text-muted" data-testid="timeline-window-hint">{WINDOW_HINT}</p>
    {/if}
  </div>
  {#if view.error !== ''}
    <p role="alert" class="mb-4 rounded-md border border-danger px-3 py-2 text-sm text-danger" data-testid="timeline-error">{view.error}</p>
  {/if}
  {#if !view.loaded && view.error === ''}
    <p class="text-sm text-text-muted" data-testid="timeline-loading">Loading the timeline…</p>
  {:else if view.loaded && view.model.groups.length === 0}
    <p class="text-sm text-text-muted" data-testid="timeline-empty">No tasks in this window.</p>
  {/if}
  {#if view.loaded}
    <div class="mb-3 flex flex-wrap items-center gap-2" role="group" aria-label="Zoom">
      <button type="button" class={button} data-testid="zoom-out" aria-label="Zoom out" disabled={span !== 0 && !canZoomOut(span)} onclick={() => chart?.zoom('out')}>−</button>
      <button type="button" class={button} data-testid="zoom-in" aria-label="Zoom in" disabled={span !== 0 && !canZoomIn(span)} onclick={() => chart?.zoom('in')}>+</button>
      <span class="text-sm text-text-muted" data-testid="timeline-span" data-span-ms={span} data-from-ms={shownFrom} data-to-ms={shownTo}>{span === 0 ? '' : `Showing ${spanLabel(span)}`}</span>
      <span class="hidden text-xs text-text-muted md:inline" data-testid="timeline-hint">Scroll to pan · Shift+scroll to zoom</span>
    </div>
    <ul class="timeline-legend mb-3 flex flex-wrap items-center gap-x-3 gap-y-1 text-xs text-text-muted" aria-label="Legend" data-testid="timeline-legend">
      {#each TIMELINE_LEGEND as entry (entry.className)}
        <li class="flex items-center gap-1.5">
          {#if entry.glyph === undefined}
            <span class="tl-swatch {entry.className}" aria-hidden="true"></span>
          {:else}
            <span class="tl-swatch-glyph {entry.className}" aria-hidden="true">{entry.glyph}</span>
          {/if}
          {entry.label}
        </li>
      {/each}
    </ul>
    <!-- vis-timeline scrolls inside this frame; the page itself never scrolls sideways -->
    <div class="relative max-w-full">
      <div class="timeline-frame relative max-w-full overflow-hidden" data-testid="timeline-chart" data-rows={rowsName(rowShape)} style="{laneStyle(rowShape)};--tl-label-w:{labelWidth}px" bind:this={frame}></div>
      <!-- Drag (or Left/Right, Home, End) to resize the label column; double click resets it. -->
      <!-- svelte-ignore a11y_no_noninteractive_element_interactions, a11y_no_noninteractive_tabindex (a focusable separator is a widget) -->
      <div
        class="tl-resizer absolute top-0 bottom-0 z-10 hidden w-3 -translate-x-1/2 cursor-col-resize touch-none items-center justify-center md:flex"
        style="left:{labelEdge}px"
        role="separator"
        aria-orientation="vertical"
        aria-label="Resize the label column"
        aria-valuemin={LABEL_MIN}
        aria-valuemax={labelMax(chartW)}
        aria-valuenow={labelWidth}
        tabindex="0"
        data-testid="label-resizer"
        onpointerdown={dragLabel}
        onkeydown={labelKey}
        ondblclick={() => void setLabelWidth(LABEL_DEFAULT, true)}
      ></div>
    </div>
  {/if}
</section>

{#if preview !== undefined}
  {#key preview}
    <TaskPreview {csrf} ref={preview} onclose={closePreview} />
  {/key}
{/if}

<style>
  /* vis-timeline's own sheet is light-only and sized for a desktop: map it onto the design tokens and make rows and
     labels fit a phone. The bars carry the task's status; lease bars, markers and arrows keep their own look. */
  /* The chart takes its own touch gestures (a one-finger drag pans both ways, a pinch zooms); the page scrolls outside it. */
  .timeline-frame :global(.vis-timeline) {
    touch-action: none;
    border: 1px solid var(--border);
    border-radius: var(--radius);
    background: var(--surface-raised);
    color: var(--text);
    font-family: var(--font-sans);
  }
  .timeline-frame :global(.vis-panel.vis-center),
  .timeline-frame :global(.vis-panel.vis-left),
  .timeline-frame :global(.vis-panel.vis-top),
  .timeline-frame :global(.vis-panel.vis-bottom) {
    border-color: var(--border);
  }
  .timeline-frame :global(.vis-time-axis .vis-text),
  .timeline-frame :global(.vis-label) {
    color: var(--text);
  }
  .timeline-frame :global(.vis-time-axis .vis-grid.vis-minor),
  .timeline-frame :global(.vis-time-axis .vis-grid.vis-major),
  .timeline-frame :global(.vis-group) {
    border-color: var(--border);
  }
  /* The label column: the rows' own colours (vis-timeline paints them light), a cap so a long title cannot take the width
     of a phone. */
  .timeline-frame :global(.vis-labelset .vis-label) {
    display: flex;
    align-items: center;
    background: var(--surface-raised);
    color: var(--text);
    border-color: var(--border);
    cursor: pointer;
  }
  /* The ring sits inside the label: the label panel clips whatever is drawn outside it. */
  .timeline-frame :global(.vis-labelset .vis-label:focus-visible) {
    outline: 2px solid var(--accent);
    outline-offset: -2px;
  }
  .timeline-frame :global(.vis-labelset .vis-label.vis-nested-group) {
    background: var(--surface);
  }
  /* Rows of fixed height: vis-timeline makes a row as tall as its label's inner box or its items, whichever is taller,
     and every item sits at the row's top inside it (lib/timelineView.ts chartOptions: no stacking, no item margin), so
     a row is as tall wherever the chart is panned or zoomed to. A phone's or Sparse's initiative row is 44 px to tap, a task row 52 px; Compact from 768 px has 32 and 40 (lib/timeline.ts). */
  .timeline-frame :global(.vis-label .vis-inner) {
    display: block;
    box-sizing: border-box;
    height: var(--tl-row);
    line-height: var(--tl-row);
    max-width: min(10rem, 28vw);
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
    padding: 0 0.5rem;
  }
  /* From md up the column is as wide as the user set (--tl-label-w); vis-timeline sizes its panel to its labels. */
  @media (min-width: 768px) {
    .timeline-frame :global(.vis-label .vis-inner) {
      width: var(--tl-label-w);
      max-width: none;
    }
  }
  .tl-resizer::after {
    content: '';
    width: 3px;
    height: 100%;
    border-radius: 2px;
    background: transparent;
  }
  .tl-resizer:hover::after,
  .tl-resizer:focus-visible::after {
    background: var(--accent);
  }
  .tl-resizer:focus-visible {
    outline: none;
  }
  .timeline-frame :global(.vis-label) {
    --tl-row: var(--tl-task-row);
  }
  .timeline-frame :global(.vis-label.tl-initiative) {
    --tl-row: var(--tl-initiative-row);
  }
  /* Each kind keeps its lane in a task row, placed by a top margin (vis-timeline measures an item without its margin, so
     the lanes never grow the row): the planned strip on top, the actual span with its title, the leases below it
     (overlapping leases spread over slots, below), the markers on the actual span's lane. */
  .timeline-frame :global(.vis-item.tl-planned),
  .timeline-frame :global(.vis-item.tl-actual),
  .timeline-frame :global(.vis-item.tl-lease),
  .timeline-frame :global(.vis-item.tl-marker:not(.vis-dot)) {
    box-sizing: border-box;
    font-size: 0.75rem;
  }
  .timeline-frame :global(.vis-item.tl-planned) {
    margin-top: var(--tl-planned-top);
    height: var(--tl-planned-h);
  }
  .timeline-frame :global(.vis-item.tl-actual),
  .timeline-frame :global(.vis-item.tl-marker:not(.vis-dot)) {
    margin-top: var(--tl-actual-top);
    height: var(--tl-actual-h);
    line-height: calc(var(--tl-actual-h) - 2px);
  }
  .timeline-frame :global(.vis-item.tl-lease) {
    margin-top: var(--tl-lease-top);
    height: var(--tl-lease-h);
    line-height: calc(var(--tl-lease-h) - 2px);
    font-size: 0.6875rem;
    opacity: 0.85;
  }
  /* Lease slots (R11): a task whose leases overlap spreads them over 2 or 3 slots (tl-lease-c<slots>-s<slot>), each bar
     stepping down from the one above so a strip of every bar is uncovered; the numbers come from laneStyle. The text of
     a 6 px bar is only its tooltip. */
  .timeline-frame :global(.vis-item.tl-lease-c2) {
    height: var(--tl-lease-c2-h);
    line-height: calc(var(--tl-lease-c2-h) - 2px);
    font-size: 0.5625rem;
  }
  .timeline-frame :global(.vis-item.tl-lease-c3) {
    height: var(--tl-lease-c3-h);
    line-height: calc(var(--tl-lease-c3-h) - 2px);
    font-size: 0;
  }
  .timeline-frame :global(.vis-item.tl-lease-c2-s0) {
    margin-top: var(--tl-lease-c2-s0-top);
  }
  .timeline-frame :global(.vis-item.tl-lease-c2-s1) {
    margin-top: var(--tl-lease-c2-s1-top);
  }
  .timeline-frame :global(.vis-item.tl-lease-c3-s0) {
    margin-top: var(--tl-lease-c3-s0-top);
  }
  .timeline-frame :global(.vis-item.tl-lease-c3-s1) {
    margin-top: var(--tl-lease-c3-s1-top);
  }
  .timeline-frame :global(.vis-item.tl-lease-c3-s2) {
    margin-top: var(--tl-lease-c3-s2-top);
  }
  .timeline-frame :global(.vis-item.tl-planned .vis-item-content),
  .timeline-frame :global(.vis-item.tl-actual .vis-item-content),
  .timeline-frame :global(.vis-item.tl-lease .vis-item-content),
  .timeline-frame :global(.vis-item.tl-marker .vis-item-content) {
    padding: 0 0.25rem;
  }
  /* A task bar opens its preview: a pointer. */
  .timeline-frame :global(.vis-item[data-task]) {
    cursor: pointer;
  }
  .timeline-frame :global(.vis-group-level-unknown-but-gte1) {
    border: 0;
  }
  /* Colours by kind and state (lib/timeline.ts TIMELINE_LEGEND): each item sets --tl to a design token; bars are a light
     wash of it with a solid edge, markers the token itself, so both themes read through the tokens' own dark values. */
  .timeline-frame :global(.vis-item),
  .timeline-legend .tl-swatch {
    --tl: var(--text-muted);
    border-color: var(--tl);
    background: color-mix(in srgb, var(--tl) 18%, var(--surface-raised));
    color: var(--text);
  }
  .timeline-frame :global(.vis-item) {
    cursor: pointer;
  }
  .timeline-frame :global(.tl-actual.tl-open),
  .timeline-legend .tl-actual.tl-open {
    --tl: var(--accent);
  }
  .timeline-frame :global(.tl-actual.tl-done),
  .timeline-legend .tl-actual.tl-done {
    --tl: var(--success);
  }
  .timeline-frame :global(.tl-actual.tl-closed),
  .timeline-legend .tl-actual.tl-closed {
    --tl: var(--text-muted);
  }
  .timeline-frame :global(.vis-item.tl-planned),
  .timeline-legend .tl-planned {
    --tl: var(--text-muted);
    background: transparent;
    border-style: dashed;
  }
  .timeline-frame :global(.tl-lease-live),
  .timeline-legend .tl-lease-live {
    --tl: var(--success);
    background: color-mix(in srgb, var(--tl) 32%, var(--surface-raised));
  }
  .timeline-frame :global(.tl-lease-expired),
  .timeline-legend .tl-lease-expired {
    --tl: var(--warning);
  }
  .timeline-frame :global(.tl-lease-ended),
  .timeline-legend .tl-lease-ended {
    --tl: var(--text-muted);
    background: color-mix(in srgb, var(--tl) 10%, var(--surface-raised));
  }
  .timeline-frame :global(.vis-item.tl-marker),
  .timeline-legend .tl-swatch-glyph {
    --tl: var(--text-muted);
    background: transparent;
    border: 0;
    color: var(--tl);
    font-weight: 700;
  }
  .timeline-frame :global(.vis-item.tl-marker-milestone),
  .timeline-legend .tl-marker-milestone {
    --tl: var(--accent);
  }
  .timeline-frame :global(.vis-item.tl-marker-ask),
  .timeline-legend .tl-marker-ask {
    --tl: var(--warning);
  }
  .timeline-frame :global(.vis-item.tl-marker-check),
  .timeline-frame :global(.vis-item.tl-marker-done),
  .timeline-legend .tl-marker-check,
  .timeline-legend .tl-marker-done {
    --tl: var(--success);
  }
  .timeline-frame :global(.vis-item.tl-marker-closed),
  .timeline-legend .tl-marker-closed {
    --tl: var(--text-muted);
  }
  .timeline-frame :global(.vis-item.tl-marker .vis-item-content) {
    color: var(--tl);
  }
  .timeline-legend .tl-swatch {
    display: inline-block;
    width: 1.25rem;
    height: 0.75rem;
    border-width: 1px;
    border-style: solid;
    border-radius: 2px;
  }
  .timeline-legend .tl-swatch.tl-planned {
    border-style: dashed;
  }
  .timeline-legend .tl-swatch-glyph {
    display: inline-block;
    min-width: 1rem;
    text-align: center;
  }
  /* A task row with tags is two lines inside its fixed height: the title with its ellipsis over the chips, cut at the edge. */
  .timeline-frame :global(.vis-label.tl-tagged .vis-inner) {
    display: flex;
    flex-direction: column;
    justify-content: center;
    line-height: 1.25rem;
  }
  .timeline-frame :global(.vis-label.tl-tagged .vis-inner > .tl-label) {
    justify-content: center;
  }
  .timeline-frame :global(.tl-label-title) {
    display: block;
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }
  .timeline-frame :global(.tl-label) {
    display: flex;
    flex-direction: column;
    min-width: 0;
  }
  .timeline-frame :global(.tl-label-tags) {
    display: flex;
    gap: 0.25rem;
    overflow: hidden;
  }
  .timeline-frame :global(.tl-tag) {
    flex: none;
    padding: 0 0.375rem;
    font-size: 0.6875rem;
    line-height: 1rem;
  }
  .timeline-frame :global(.tl-attention) {
    color: var(--danger);
  }
  .timeline-frame :global(svg.tl-arrows) {
    position: absolute;
    inset: 0;
    width: 100%;
    height: 100%;
    overflow: hidden;
    pointer-events: none;
    z-index: 5;
  }
  .timeline-frame :global(path.tl-arrow) {
    fill: none;
    stroke: var(--text-muted);
    stroke-width: 1.5;
  }
  .timeline-frame :global(path.tl-arrowhead) {
    fill: var(--text-muted);
  }
</style>
