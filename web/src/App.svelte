<script lang="ts">
  import type { Component } from 'svelte';
  import Actors from './Actors.svelte';
  import Archive from './Archive.svelte';
    import ApiKeys from './ApiKeys.svelte';
  import NoticeBanner from './NoticeBanner.svelte';
  import Notes from './Notes.svelte';
  import Now from './Now.svelte';
  import InitiativePage from './InitiativePage.svelte';
  import Initiatives from './Initiatives.svelte';
  import SessionCard from './SessionCard.svelte';
  import Shell from './Shell.svelte';
  import TaskPage from './TaskPage.svelte';
  import Tasks from './Tasks.svelte';
  import { live } from './lib/live';
  import { watchLiveTasks } from './lib/liveTasks';
  import { readNotices, watchNotices, type Notice } from './lib/notices';
  import { TasksApi } from './lib/tasks';
  import { loginUrl, currentBrowser, SessionController, type SessionState } from './lib/session';
  import { replaceHash } from './lib/historyEntry';
  import { ROUTES, navEntries, redirectHash, resolveRoute, routeParam, type RouteId } from './lib/routes';

  // The component of each view; the table in lib/routes.ts decides which one is on screen.
  type ViewComponent = Component<{ csrf: string; param: string }>;
  const VIEWS: Record<Exclude<RouteId, 'timeline'>, ViewComponent> = {
    now: Now,
    initiatives: Initiatives,
    initiative: InitiativePage,
    tasks: Tasks,
    task: TaskPage,
    actors: Actors,
    archive: Archive,
    notes: Notes,
    keys: ApiKeys,
  };

  // The Timeline is the one view loaded on demand: vis-timeline, vis-data and moment (and the vis stylesheet) stay out of the entry chunk.
  let timelineView: ViewComponent | undefined = $state();
  function loadTimeline() {
    void import('./Timeline.svelte').then((m) => (timelineView = m.default));
  }

  // The root and `#/attention` land on `#/now`, in place (no history entry), keeping the query of the fragment.
  function land() {
    const to = redirectHash(window.location.hash);
    if (to !== undefined) replaceHash(to);
  }
  land();

  const browser = currentBrowser();
  let auth: SessionState = $state({ kind: 'loading' });
  let route = $state(resolveRoute(window.location.hash, ROUTES));
  let param = $state(routeParam(window.location.hash, ROUTES));
  let signOutFailed = $state(false);

  // The Needs me badge in the navigation: the total of the stream's latest `attention` message.
  let attentionTotal = $state(0);
  // The green badge: how many open tasks hold a live lease, read when the shell is up and on every task_* or lease_* live event.
  let liveTasks = $state(0);
  // The live notices of every actor, shown in a banner above the view: read when the shell is up and on every lease_* live event.
  let notices: Notice[] = $state([]);

  const session = new SessionController(browser, (s) => (auth = s), window.location.hash);

  $effect(() => {
    void session.start();
  });

  $effect(() => live().onAttention((counts) => (attentionTotal = counts.total)));

  $effect(() => {
    if (auth.kind !== 'signed-in') return;
    const csrf = auth.session.csrf_token;
    const api = new TasksApi(browser, () => csrf);
    return watchLiveTasks(live(), api, (count) => (liveTasks = count));
  });

  $effect(() => {
    if (auth.kind !== 'signed-in') return;
    return watchNotices(live(), () => readNotices(browser), (list) => (notices = list));
  });

  async function signOut() {
    signOutFailed = (await session.signOut()) === 'failed';
  }

  function hashChanged() {
    land();
    route = resolveRoute(window.location.hash, ROUTES);
    param = routeParam(window.location.hash, ROUTES);
    void session.hashChanged(window.location.hash);
  }

  $effect(() => {
    if (route.id === 'timeline' && timelineView === undefined) loadTimeline();
  });

  const View = $derived(route.id === 'timeline' ? timelineView : VIEWS[route.id]);
</script>

<svelte:window onhashchange={hashChanged} />

{#if auth.kind === 'loading' || auth.kind === 'redirecting'}
  <SessionCard title={auth.kind === 'loading' ? 'Signing in' : 'Redirecting to sign in'}>
    <div
      class="mx-auto mb-3 size-8 animate-spin rounded-full border-4 border-border border-t-accent"
      role="status"
      aria-label={auth.kind === 'loading' ? 'Signing in' : 'Redirecting to sign in'}
      data-testid="spinner"
    ></div>
    <p class="text-text-muted">{auth.kind === 'loading' ? 'Signing in…' : 'Redirecting to sign in…'}</p>
  </SessionCard>
{:else if auth.kind === 'error'}
  <SessionCard title="Session error">
    <p role="alert" class="mb-4" data-testid="session-error">{auth.message}</p>
    <button
      type="button"
      class="min-h-11 rounded-md border border-accent px-4 font-semibold text-accent hover:bg-surface"
      onclick={() => void session.retry()}>Retry</button
    >
  </SessionCard>
{:else if auth.kind === 'signed-out'}
  <SessionCard title="Signed out">
    <p class="mb-4" data-testid="signed-out">You are signed out.</p>
    <a class="inline-flex min-h-11 items-center rounded-md border border-accent px-4 font-semibold text-accent hover:bg-surface" href={loginUrl(browser.pathname)}
      >Sign in</a
    >
  </SessionCard>
{:else}
  <Shell title={route.title} entries={navEntries(route, ROUTES, attentionTotal, liveTasks)} email={auth.session.email} actor={auth.session.actor} onsignout={signOut}>
    {#if signOutFailed}
      <p role="alert">Sign out failed. Try again.</p>
    {/if}
    <NoticeBanner {notices} />
    {#key param}
      {#if View}
        <View csrf={auth.session.csrf_token} {param} />
      {/if}
    {/key}
  </Shell>
{/if}
