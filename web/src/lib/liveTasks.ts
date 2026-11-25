// The green badge on the Now navigation item (PRD R5, R8 b, SPEC 11): how many open tasks hold at least one live lease.
// The number is the answer of the count form of the list, `GET /api/v1/tasks/count?status=open&active=true` (a count and
// no rows; `active` is "has a live lease", the server expires due leases before it reads). It is read once the shell is
// up and again, at most once per burst, on a `task_*` or `lease_*` event only: a note, an ask or a milestone cannot
// change it.
import type { Live } from './live';
import type { TasksApi } from './tasks';

/** The query of the tasks the badge counts: open and active. */
export function liveTasksQuery(): URLSearchParams {
  return new URLSearchParams({ status: 'open', active: 'true' });
}

/** How many open tasks hold a live lease. A failed read rejects: the caller keeps the count it showed. */
export function countLiveTasks(api: Pick<TasksApi, 'count'>): Promise<number> {
  return api.count(liveTasksQuery());
}

/** Whether a live event can change the count: a task event (created, done, closed, reopened ...) or a lease event (started, ended, expired ...). */
export function affectsLiveTasks(event: { type: string }): boolean {
  return event.type.startsWith('task_') || event.type.startsWith('lease_');
}

/**
 * Shows the count now and again on each event that can change it (and on a reset or reconnect, which `subscribe` adds).
 * A failed read keeps the count on screen; the next event reads again. Returns the stop function.
 */
export function watchLiveTasks(stream: Pick<Live, 'subscribe'>, api: Pick<TasksApi, 'count'>, show: (count: number) => void): () => void {
  const read = async () => {
    try {
      show(await countLiveTasks(api));
    } catch {
      // keep the count on screen
    }
  };
  void read();
  return stream.subscribe(affectsLiveTasks, read);
}
