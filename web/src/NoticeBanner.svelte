<script lang="ts">
  import { noticeLines, type Notice } from './lib/notices';

  // The live notices of every view (SPEC 11). It renders nothing while there is none, so a board with no notice looks as
  // it did. It sits after the view in the main area and sticks to the bottom of the screen (above the phone's tab bar),
  // so the notices read late, when the view is already drawn, move nothing: the view keeps its place. The time left is recomputed every 15 seconds without a read, and a notice whose time has
  // run out leaves the banner the same way.
  let { notices }: { notices: Notice[] } = $props();

  const TICK_MS = 15_000;
  let now = $state(new Date());
  $effect(() => {
    const timer = setInterval(() => (now = new Date()), TICK_MS);
    return () => clearInterval(timer);
  });

  const lines = $derived(noticeLines(notices, now));
</script>

{#if lines.length > 0}
  <section aria-label="Notices" class="sticky bottom-[calc(3.5rem+env(safe-area-inset-bottom)+0.5rem)] z-20 mt-4 flex max-h-[40dvh] flex-col gap-2 overflow-y-auto md:bottom-4" data-testid="notices">
    <ul class="flex flex-col gap-2">
      {#each lines as line (line.lease)}
        <li class="min-w-0 rounded-md border border-warning bg-surface-raised px-3 py-2 text-sm text-text" data-testid="notice">
          <p class="wrap-anywhere">
            <strong class="font-semibold" data-testid="notice-holder">{line.holder}</strong>
            <span class="text-text-muted">notice:</span>
            <span data-testid="notice-intent">{line.intent}</span>
          </p>
          {#if line.resources.length > 0}
            <p class="wrap-anywhere text-text-muted" data-testid="notice-resources">{line.resources.join(', ')}</p>
          {/if}
          <p class="flex flex-wrap items-center gap-x-3 text-text-muted">
            <span data-testid="notice-left">{line.timeLeft}</span>
            <a class="inline-flex min-h-11 items-center font-semibold text-accent underline wrap-anywhere" href={line.href} data-testid="notice-link">Task {line.task}</a>
          </p>
        </li>
      {/each}
    </ul>
  </section>
{/if}
