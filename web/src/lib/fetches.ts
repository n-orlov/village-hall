// The loading rule the list pages share (SPEC 11): a list is loading while ANY loading fetch of it is
// pending, the first or a reload after a create or a revoke, and only once every one of them has
// succeeded or failed is it not. A live refresh is a quiet fetch: it takes part in newest-wins but
// never holds the loading state, so refreshes that keep overlapping (events arriving faster than a
// read lands) cannot keep a list on its skeleton after its own load has landed. Fetches can overlap (a create submitted while the first fetch is
// still out) and can land out of order; the outcome of a fetch that lands after a newer one has
// already landed is stale and is dropped, so the page never goes back to an older list.

export class Fetches {
  private readonly loading = new Set<number>();
  private started = 0;
  private landed = 0;

  /** Records that a fetch started, a loading one unless `quiet`; the ticket is what `end` takes when it lands. */
  begin(quiet = false): number {
    this.started += 1;
    if (!quiet) this.loading.add(this.started);
    return this.started;
  }

  /** Records that the fetch with `ticket` landed (worked or failed); whether its outcome is to be shown. */
  end(ticket: number): boolean {
    this.loading.delete(ticket);
    const newest = ticket > this.landed;
    if (newest) this.landed = ticket;
    return newest;
  }

  /** Whether any loading fetch is still pending: the list's `loading`. */
  get busy(): boolean {
    return this.loading.size > 0;
  }
}

/** The text of a thrown value: an Error's message, else the value itself. */
export function errorText(e: unknown): string {
  return e instanceof Error ? e.message : String(e);
}
