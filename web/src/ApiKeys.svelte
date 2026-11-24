<script lang="ts">
  import { replaceHash } from './lib/historyEntry';
  import Dialog from './Dialog.svelte';
  import KeyForm from './KeyForm.svelte';
  import SortHeader from './SortHeader.svelte';
  import SortSelect from './SortSelect.svelte';
  import { copyText } from './lib/clipboard';
  import { absoluteTime, relativeTime } from './lib/format';
  import { KeysApi, KeysStore, affectsKeys, type KeysState } from './lib/keys';
  import { KEYS_VIEW, KEY_COLUMNS, KEY_SORT_LABELS, isKeysHash, keysHash, queryOfHash, sortOfQuery } from './lib/keysView';
  import { keyOwnerText } from './lib/origin';
  import { keyTone, toneClasses } from './lib/presentation';
  import { live } from './lib/live';
  import { currentBrowser } from './lib/session';
  import { sortRows, sortToQuery, type SortState } from './lib/tableSort';
  import { currentViewEnv, openView, saveViewQuery } from './lib/viewState';

  let { csrf }: { csrf: string } = $props();

  let view: KeysState = $state({ keys: [], loading: true, created: undefined, error: undefined, formError: undefined });
  let formOpen = $state(false);
  let copied = $state(false);
  let copyFailed = $state(false);
  // The name of the key the person asked to revoke; the dialog is open while it is set.
  let revoking: string | undefined = $state(undefined);

  const store = new KeysStore(new KeysApi(currentBrowser(), () => csrf), (s) => (view = s));

  const viewEnv = currentViewEnv();
  // A fragment with a query wins and is saved; a bare one restores the saved query (in place, before the first fetch).
  let sort: SortState = $state(sortOfQuery(openView(viewEnv, KEYS_VIEW, '#/settings/keys', queryOfHash(window.location.hash))));
  // The default order is the server's (oldest key first); a sort replaces it and ties keep it.
  const rows = $derived(sortRows(view.keys, sort, KEY_COLUMNS));

  // A sort only reorders the keys the view holds: the fragment and the saved query follow it, and no request is made.
  function setSort(next: SortState) {
    sort = next;
    replaceHash(keysHash(next));
    saveViewQuery(viewEnv.storage, KEYS_VIEW, sortToQuery(next));
  }

  // The fragment edited by hand, or the back button after a link with another query. A bare route restores the saved query.
  function hashChanged() {
    const hash = window.location.hash;
    if (isKeysHash(hash)) sort = sortOfQuery(openView(viewEnv, KEYS_VIEW, '#/settings/keys', queryOfHash(hash)));
  }

  $effect(() => {
    void store.load();
    // A key created or revoked elsewhere changes the list in place; the once-shown key box and an open form stay.
    return live().subscribe(affectsKeys, () => store.refresh());
  });

  function openForm() {
    store.clearFormError();
    formOpen = true;
  }

  async function create(name: string, expiresIn: string) {
    if (await store.create(name, expiresIn)) {
      formOpen = false;
      copied = false;
      copyFailed = false;
    }
  }

  async function confirmRevoke() {
    const name = revoking;
    revoking = undefined;
    if (name !== undefined) await store.revoke(name);
  }

  async function copy(key: string) {
    const ok = await copyText(navigator.clipboard, key);
    copied = ok;
    copyFailed = !ok;
  }

  const now = new Date();
  const primary = 'min-h-11 rounded-md border border-accent bg-surface-raised px-4 text-sm font-medium text-accent';
  const secondary = 'min-h-11 rounded-md border border-border bg-surface-raised px-4 text-sm font-medium text-text';
  const th = 'px-3 py-2 text-sm font-medium text-text-muted';
  const label = 'mr-1 text-xs text-text-muted md:hidden';
</script>

<svelte:window onhashchange={hashChanged} />

<section aria-labelledby="keys-heading" class="mx-auto w-full max-w-[1280px] pb-16 md:pb-0">
  <div class="mb-4 flex items-center justify-between gap-3">
    <h2 id="keys-heading" class="text-xl font-bold">API keys</h2>
    <button type="button" class="{primary} hidden md:inline-flex md:items-center" onclick={openForm}>New API key</button>
  </div>

  {#if view.error !== undefined}
    <p role="alert" class="mb-4 rounded-md border border-danger px-3 py-2 text-sm text-danger">{view.error}</p>
  {/if}

  {#if view.created !== undefined}
    <div data-testid="new-key" role="status" class="mb-4 flex flex-col gap-3 rounded-md border-2 border-accent bg-surface-raised p-4">
      <p class="text-sm">
        Key <strong class="break-all">{view.created.name}</strong> created. Copy it now: it is shown only once.
      </p>
      <code data-testid="new-key-value" class="block rounded-md border border-border bg-surface px-3 py-3 font-mono text-base break-all whitespace-pre-wrap text-text select-all"
        >{view.created.key}</code
      >
      <div class="flex flex-col gap-2 sm:flex-row sm:items-center">
        <button type="button" class={primary} onclick={() => copy(view.created?.key ?? '')}>Copy</button>
        <button type="button" class={secondary} onclick={() => store.dismiss()}>Done</button>
        {#if copied}
          <span data-testid="copied" class="text-sm text-success">Copied</span>
        {:else if copyFailed}
          <span data-testid="copy-failed" class="text-sm text-danger">Could not copy: select the key and copy it by hand.</span>
        {/if}
      </div>
    </div>
  {/if}

  {#if view.loading}
    <div aria-busy="true" class="flex flex-col gap-3" data-testid="keys-skeleton">
      <span class="sr-only">Loading API keys…</span>
      {#each [0, 1, 2] as n (n)}
        <div class="flex flex-col gap-2 rounded-md border border-border bg-surface-raised p-4" data-testid="skeleton-row">
          <div class="h-4 w-2/3 animate-pulse rounded-md bg-border"></div>
          <div class="h-4 w-1/3 animate-pulse rounded-md bg-border"></div>
        </div>
      {/each}
    </div>
  {:else if view.keys.length === 0}
    <div class="flex flex-col items-center gap-3 rounded-md border border-dashed border-border px-4 py-12 text-center" data-testid="empty-state">
      <p class="text-lg font-medium">No API keys yet</p>
      <p class="text-sm text-text-muted">A key lets an agent call the API.</p>
    </div>
  {:else}
    <div class="mb-3 flex"><SortSelect columns={KEY_SORT_LABELS} {sort} onsort={setSort} hideFrom="lg" /></div>
    <!-- One table: from 1024 px a dense table (its fixed columns need more than the main column of a 768 px screen), below it each row is a card. -->
    <table class="block w-full text-left wrap-anywhere lg:table lg:table-fixed lg:border-collapse">
      <thead class="sr-only lg:table-header-group lg:not-sr-only">
        <tr class="lg:border-b lg:border-border">
          <SortHeader column="name" label="Name" {sort} onsort={setSort} class={th} />
          <th scope="col" class="{th} lg:w-24">Owner</th>
          <SortHeader column="prefix" label="Prefix" {sort} onsort={setSort} class="{th} lg:w-24" />
          <SortHeader column="status" label="Status" {sort} onsort={setSort} class="{th} lg:w-24" />
          <SortHeader column="created" label="Created" {sort} onsort={setSort} class="{th} lg:w-24" />
          <SortHeader column="used" label="Last used" {sort} onsort={setSort} class="{th} lg:w-28" />
          <SortHeader column="expires" label="Expires" {sort} onsort={setSort} class="{th} lg:w-28" />
          <th scope="col" class={`${th} lg:w-28`}><span class="sr-only">Actions</span></th>
        </tr>
      </thead>
      <tbody class="flex flex-col gap-3 lg:table-row-group">
        {#each rows as key (key.id)}
          <tr
            data-testid="key-row"
            data-name={key.name}
            class="flex flex-wrap items-center gap-x-3 gap-y-2 rounded-md border border-border bg-surface-raised p-4 lg:table-row lg:rounded-none lg:border-0 lg:border-b lg:bg-transparent lg:p-0"
          >
            <td class="min-w-0 flex-1 font-medium lg:table-cell lg:px-3 lg:py-3">{key.name}</td>
            <td class="w-full text-sm text-text-muted lg:table-cell lg:w-auto lg:px-3 lg:py-3" data-testid="key-owner">
              <span class={label}>Owner </span>{keyOwnerText(key.owner)}
            </td>
            <td class="lg:table-cell lg:px-3 lg:py-3"><code class="font-mono text-sm text-text-muted">{key.display_prefix}</code></td>
            <td class="lg:table-cell lg:px-3 lg:py-3" data-testid="key-status"
              ><span class="inline-block rounded-md border px-2 py-0.5 text-xs font-medium {toneClasses(keyTone(key.status))}">{key.status}</span></td
            >
            <td class="w-full text-sm text-text-muted lg:table-cell lg:w-auto lg:px-3 lg:py-3">
              <span class={label}>Created </span><time datetime={key.created_at} title={absoluteTime(key.created_at)}>{relativeTime(key.created_at, now)}</time>
            </td>
            <td class="w-full text-sm text-text-muted lg:table-cell lg:w-auto lg:px-3 lg:py-3">
              <span class={label}>Last used </span>
              {#if key.last_used_at !== undefined}
                <time datetime={key.last_used_at} title={absoluteTime(key.last_used_at)}>{relativeTime(key.last_used_at, now)}</time>
              {:else}
                never
              {/if}
            </td>
            <td class="w-full text-sm text-text-muted lg:table-cell lg:w-auto lg:px-3 lg:py-3">
              <span class={label}>Expires </span>
              {#if key.expires_at !== undefined}
                <time datetime={key.expires_at}>{absoluteTime(key.expires_at)}</time>
              {:else}
                never
              {/if}
            </td>
            <td class="w-full lg:table-cell lg:w-auto lg:px-3 lg:py-3 lg:text-right">
              {#if key.status !== 'revoked'}
                <button type="button" class="{secondary} w-full lg:w-auto lg:px-3" onclick={() => (revoking = key.name)}>Revoke</button>
              {/if}
            </td>
          </tr>
        {/each}
      </tbody>
    </table>
  {/if}

  <button
    type="button"
    class="{primary} fixed inset-x-4 bottom-[calc(3.5rem+env(safe-area-inset-bottom)+0.75rem)] z-20 shadow-lg md:hidden"
    onclick={openForm}>New API key</button
  >
</section>

{#if formOpen}
  <Dialog label="New API key" onclose={() => (formOpen = false)} initialFocus="input[name=name]">
    <KeyForm error={view.formError} oncreate={create} oncancel={() => (formOpen = false)} />
  </Dialog>
{/if}

{#if revoking !== undefined}
  <Dialog label="Revoke API key" onclose={() => (revoking = undefined)} initialFocus="button[data-cancel]">
    <div class="flex flex-col gap-4">
      <h2 class="text-lg font-bold">Revoke API key</h2>
      <p class="text-sm">
        Revoke <strong class="break-all">{revoking}</strong>? Anything using it gets 401 from its next request. This cannot be undone.
      </p>
      <div class="flex flex-col-reverse gap-2 sm:flex-row sm:justify-end">
        <button type="button" data-cancel class="{secondary} border-border" onclick={() => (revoking = undefined)}>Cancel</button>
        <button type="button" class="min-h-11 rounded-md border border-danger bg-surface-raised px-4 text-sm font-medium text-danger" onclick={confirmRevoke}
          >Revoke key</button
        >
      </div>
    </div>
  </Dialog>
{/if}
