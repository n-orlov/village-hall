import { describe, expect, it } from 'vitest';
import {
  conflictText,
  holderText,
  isLive,
  leasesOfTask,
  liveLeases,
  nowPanel,
  panelSummary,
  resourceConflicts,
  taskConflicts,
  taskConflictText,
  type Lease,
} from './conflicts';

const NOW = new Date('2026-11-06T12:00:00Z');
const at = (seconds: number): string => new Date(NOW.getTime() + seconds * 1000).toISOString();

function lease(ref: string, task: string, actor: string, resources: string[], extra: Partial<Lease> = {}): Lease {
  return {
    id: `id-${ref}`,
    ref,
    task,
    actor,
    intent: `work on ${task}`,
    ttl_seconds: 7200,
    started_at: at(-60),
    renewed_at: at(-60),
    expires_at: at(2460),
    state: 'live',
    notice: false,
    resources,
    ...extra,
  };
}

describe('isLive and liveLeases', () => {
  it('keeps live leases whose time has not run out', () => {
    const ended = lease('L2', 'aa-2', 'bob', [], { state: 'ended' });
    const lapsed = lease('L3', 'aa-3', 'bob', [], { expires_at: at(-1) });
    const at_the_edge = lease('L4', 'aa-4', 'bob', [], { expires_at: at(0) });
    const live = lease('L1', 'aa-1', 'alice', []);
    expect(isLive(live, NOW)).toBe(true);
    expect(isLive(ended, NOW)).toBe(false);
    expect(isLive(lapsed, NOW)).toBe(false);
    expect(isLive(at_the_edge, NOW)).toBe(false);
    expect(liveLeases([ended, live, lapsed], NOW)).toEqual([live]);
  });
});

describe('no conflict', () => {
  const leases = [lease('L1', 'aa-1', 'alice', ['repo:board']), lease('L2', 'bb-1', 'bob', ['repo:other'])];
  it('finds none for leases on different resources', () => {
    expect(resourceConflicts(leases, NOW)).toEqual([]);
    expect(taskConflicts('aa-1', leases, NOW)).toEqual([]);
    const panel = nowPanel(leases, NOW);
    expect(panel.conflicts).toEqual([]);
    expect(panel.rows.map((row) => row.conflicting)).toEqual([false, false]);
    expect(panel.summary).toBe('2 live leases');
  });
  it('finds none for a lease alone, for leases without resources and for an empty board', () => {
    expect(resourceConflicts([lease('L1', 'aa-1', 'alice', ['repo:board'])], NOW)).toEqual([]);
    expect(resourceConflicts([lease('L1', 'aa-1', 'a', []), lease('L2', 'bb-1', 'b', [])], NOW)).toEqual([]);
    expect(nowPanel([], NOW)).toEqual({ rows: [], conflicts: [], summary: 'no live leases' });
  });
  it('lists the live leases of a task whatever their resources', () => {
    expect(leasesOfTask('aa-1', leases, NOW).map((l) => l.ref)).toEqual(['L1']);
    expect(leasesOfTask('zz-9', leases, NOW)).toEqual([]);
  });
});

describe('one shared resource', () => {
  const leases = [
    lease('L1', 'aa-1', 'alice', ['host:ci', 'repo:board']),
    lease('L2', 'bb-1', 'bob', ['repo:board', 'repo:docs']),
  ];
  it('names the resource and both holders on the panel', () => {
    const panel = nowPanel(leases, NOW);
    expect(panel.conflicts).toEqual([
      {
        resource: 'repo:board',
        holders: [
          { lease: 'L1', holder: 'alice', task: 'aa-1', intent: 'work on aa-1', expiresAt: at(2460) },
          { lease: 'L2', holder: 'bob', task: 'bb-1', intent: 'work on bb-1', expiresAt: at(2460) },
        ],
      },
    ]);
    expect(panel.rows.map((row) => [row.lease, row.conflicting])).toEqual([['L1', true], ['L2', true]]);
    expect(panel.rows[0]?.expires).toBe('expires in 41m');
    expect(panel.summary).toBe('2 live leases, 1 conflict');
    expect(conflictText(panel.conflicts[0]!)).toBe('repo:board: held by alice on aa-1, bob on bb-1');
  });
  it('shows the other holder on each task page, and ending one lease clears it', () => {
    expect(taskConflicts('aa-1', leases, NOW).map((c) => [c.resource, c.others.map((o) => o.lease)])).toEqual([
      ['repo:board', ['L2']],
    ]);
    expect(taskConflicts('bb-1', leases, NOW).map((c) => [c.resource, c.others.map((o) => o.lease)])).toEqual([
      ['repo:board', ['L1']],
    ]);
    const ended = [leases[0]!, { ...leases[1]!, state: 'ended' as const }];
    expect(taskConflicts('aa-1', ended, NOW)).toEqual([]);
    expect(nowPanel(ended, NOW).conflicts).toEqual([]);
    expect(taskConflictText(taskConflicts('aa-1', leases, NOW)[0]!)).toBe('repo:board: also held by bob on bb-1');
  });
  it('gives a task with no lease of its own no conflict', () => {
    expect(taskConflicts('cc-1', leases, NOW)).toEqual([]);
  });
  it('ignores a lease that has run out', () => {
    const lapsed = [leases[0]!, { ...leases[1]!, expires_at: at(-5) }];
    expect(resourceConflicts(lapsed, NOW)).toEqual([]);
  });
});

describe('two other holders', () => {
  const leases = [
    lease('L1', 'aa-1', 'alice', ['repo:board']),
    lease('L2', 'bb-1', 'bob', ['repo:board']),
    lease('L3', 'cc-1', 'carol', ['repo:board']),
  ];
  it('lists both other holders on the task page and all three on the panel', () => {
    const [conflict] = taskConflicts('aa-1', leases, NOW);
    expect(conflict?.others.map((o) => o.holder)).toEqual(['bob', 'carol']);
    expect(taskConflictText(conflict!)).toBe('repo:board: also held by bob on bb-1, carol on cc-1');
    const panel = nowPanel(leases, NOW);
    expect(panel.conflicts).toHaveLength(1);
    expect(panel.conflicts[0]?.holders).toHaveLength(3);
    expect(panel.summary).toBe('3 live leases, 1 conflict');
  });
  it('sorts several contested resources by name', () => {
    const more = [...leases, lease('L4', 'dd-1', 'dan', ['a:first']), lease('L5', 'ee-1', 'eve', ['a:first'])];
    expect(resourceConflicts(more, NOW).map((c) => c.resource)).toEqual(['a:first', 'repo:board']);
    expect(nowPanel(more, NOW).summary).toBe('5 live leases, 2 conflicts');
  });
});

describe('the same holder on two tasks', () => {
  const leases = [lease('L1', 'aa-1', 'alice', ['repo:board']), lease('L2', 'aa-2', 'alice', ['repo:board'])];
  it('is a conflict, shown on both task pages', () => {
    expect(resourceConflicts(leases, NOW)).toHaveLength(1);
    const first = taskConflicts('aa-1', leases, NOW);
    expect(first[0]?.others.map((o) => [o.holder, o.task])).toEqual([['alice', 'aa-2']]);
    expect(taskConflicts('aa-2', leases, NOW)[0]?.others.map((o) => o.task)).toEqual(['aa-1']);
    expect(taskConflictText(first[0]!)).toBe('repo:board: also held by alice on aa-2');
  });
  it('is no conflict when the two tasks hold different resources', () => {
    const apart = [leases[0]!, lease('L2', 'aa-2', 'alice', ['repo:docs'])];
    expect(resourceConflicts(apart, NOW)).toEqual([]);
  });
  it('lists a second lease on the same task as another holder', () => {
    const twice = [lease('L1', 'aa-1', 'alice', ['repo:board']), lease('L2', 'aa-1', 'bob', ['repo:board'])];
    expect(taskConflicts('aa-1', twice, NOW)[0]?.others.map((o) => o.lease)).toEqual(['L2']);
  });
});

describe('text helpers', () => {
  it('phrases a holder and a summary', () => {
    expect(holderText({ holder: 'alice', task: 'aa-1' })).toBe('alice on aa-1');
    expect(panelSummary(0, 3)).toBe('no live leases');
    expect(panelSummary(1, 0)).toBe('1 live lease');
    expect(panelSummary(1, 1)).toBe('1 live lease, 1 conflict');
  });
});
