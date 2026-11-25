<script lang="ts">
  import { onMount } from 'svelte';
  import { live, type LiveState } from './lib/live';

  // The live indicator (SPEC 11): a dot plus the state's word, `live`, `reconnecting` or `offline`, so the
  // state never rides on colour alone. The whole element is a status region with an accessible name.
  // `compact` is the 64 px icon rail: the word goes under the dot in small type. The word's box is as wide as the longest
  // word, so the state changing from `reconnecting` to `live` at start-up moves nothing (no layout shift).
  let { compact = false }: { compact?: boolean } = $props();

  let state: LiveState = $state(live().state);

  const DOT: Record<LiveState, string> = { live: 'bg-success', reconnecting: 'bg-warning', offline: 'bg-danger' };
  const TEXT: Record<LiveState, string> = { live: 'text-success', reconnecting: 'text-warning', offline: 'text-danger' };

  onMount(() => {
    const l = live();
    state = l.state;
    const off = l.onState((next) => (state = next));
    l.start();
    return off;
  });
</script>

<span
  role="status"
  aria-label={`Live updates: ${state}`}
  data-testid="live-indicator"
  data-state={state}
  class={`flex items-center gap-1.5 font-semibold ${compact ? 'flex-col text-[10px] leading-tight' : 'text-sm'} ${TEXT[state]}`}
>
  <span class={`inline-block size-2.5 shrink-0 rounded-full ${DOT[state]}`} aria-hidden="true"></span>
  <span class={`inline-block min-w-[12ch] ${compact ? 'text-center' : ''}`} aria-hidden="true">{state}</span>
</span>
