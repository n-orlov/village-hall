<script lang="ts" generics="T extends string">
  // One segmented choice drawn as a radio group (arrow keys move it, a label is the hit area): the theme and the density
  // switches share it. `name` must differ between two copies on one page (the sidebar foot and the account menu).
  // `inline` puts the label left of the buttons, on one centre line with them, for a row of filter controls (the Timeline's
  // "Touched within"); the legend then stays the group's accessible name and the visible label is its copy.
  let {
    legend,
    name,
    options,
    value,
    onchoose,
    testid,
    inline = false,
  }: {
    legend: string;
    name: string;
    options: readonly { value: T; label: string }[];
    value: T;
    onchoose: (next: T) => void;
    testid: string;
    inline?: boolean;
  } = $props();
</script>

<fieldset class={inline ? 'flex min-w-0 flex-wrap items-center gap-2' : 'mb-4 min-w-0'}>
  <legend class={inline ? 'sr-only' : 'mb-2 text-sm text-text-muted'}>{legend}</legend>
  {#if inline}
    <span aria-hidden="true" class="text-sm text-text-muted" data-testid="{testid}-label">{legend}</span>
  {/if}
  <div class="flex gap-2" data-testid={testid}>
    {#each options as option (option.value)}
      <label
        class="relative flex min-h-11 flex-1 cursor-pointer items-center justify-center rounded-md border border-border px-3 text-sm text-text has-[:checked]:border-accent has-[:checked]:bg-surface has-[:checked]:font-semibold has-[:focus-visible]:outline-2 has-[:focus-visible]:outline-offset-2 has-[:focus-visible]:outline-accent"
      >
        <input
          type="radio"
          {name}
          class="absolute -inset-px h-[calc(100%+2px)] w-[calc(100%+2px)] cursor-pointer opacity-0 text-base"
          value={option.value}
          checked={value === option.value}
          onchange={() => onchoose(option.value)}
        />
        {option.label}
      </label>
    {/each}
  </div>
</fieldset>
