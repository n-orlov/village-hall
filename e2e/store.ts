import { DatabaseSync } from 'node:sqlite';
import { join } from 'node:path';

const DAY_MS = 24 * 60 * 60 * 1000;

// The calling worker's own SQLite store (fixtures.ts starts a server per worker and sets E2E_WORK in that worker), for the one thing the API cannot do:
// make a closed item old. Closing stamps `closed_at` with the server's clock, and a spec cannot wait thirty days.
// Times are stored as UTC milliseconds since the epoch (internal/store EncodeTime).
function backdate(table: 'tasks' | 'initiatives', id: string, days: number): void {
  const work = process.env.E2E_WORK;
  if (!work) throw new Error('E2E_WORK is not set (the worker fixture in fixtures.ts sets it)');
  const db = new DatabaseSync(join(work, 'board.db'));
  try {
    db.exec('PRAGMA busy_timeout = 5000');
    const row = db.prepare(`SELECT closed_at FROM ${table} WHERE id = ? AND closed_at IS NOT NULL`).get(id) as { closed_at: number } | undefined;
    if (row === undefined) throw new Error(`${table} ${id} is not closed`);
    db.prepare(`UPDATE ${table} SET closed_at = ? WHERE id = ?`).run(row.closed_at - days * DAY_MS, id);
  } finally {
    db.close();
  }
}

/** Moves the `closed_at` of a closed task (by its ULID) `days` back. */
export function backdateTask(id: string, days: number): void {
  backdate('tasks', id, days);
}

/** Moves the `closed_at` of a closed initiative (by its ULID) `days` back. */
export function backdateInitiative(id: string, days: number): void {
  backdate('initiatives', id, days);
}

/**
 * Moves every event of a task (by its ULID) `hours` back, so the task counts as untouched in the Now windows (8h, 24h, 3d)
 * without a spec waiting for them. Creating a task writes an event, so a task a spec wants idle is aged this way.
 */
export function ageTaskEvents(id: string, hours: number): void {
  const work = process.env.E2E_WORK;
  if (!work) throw new Error('E2E_WORK is not set (the worker fixture in fixtures.ts sets it)');
  const db = new DatabaseSync(join(work, 'board.db'));
  try {
    db.exec('PRAGMA busy_timeout = 5000');
    db.prepare('UPDATE events SET at = at - ? WHERE task_id = ?').run(hours * 60 * 60 * 1000, id);
  } finally {
    db.close();
  }
}
