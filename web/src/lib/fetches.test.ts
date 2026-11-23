import { describe, expect, it } from 'vitest';
import { errorText, Fetches } from './fetches';

describe('Fetches', () => {
  it('is not busy before any fetch', () => {
    expect(new Fetches().busy).toBe(false);
  });

  it('is busy while one fetch is pending and not after it lands, and shows its outcome', () => {
    const f = new Fetches();
    const t = f.begin();
    expect(f.busy).toBe(true);
    expect(f.end(t)).toBe(true);
    expect(f.busy).toBe(false);
  });

  it('stays busy until every overlapping fetch has landed, in either order', () => {
    const f = new Fetches();
    const first = f.begin();
    const second = f.begin();
    expect(f.end(second)).toBe(true);
    expect(f.busy).toBe(true);
    expect(f.end(first)).toBe(false);
    expect(f.busy).toBe(false);

    const third = f.begin();
    const fourth = f.begin();
    expect(f.end(third)).toBe(true);
    expect(f.busy).toBe(true);
    expect(f.end(fourth)).toBe(true);
    expect(f.busy).toBe(false);
  });

  it('gives each fetch a new ticket', () => {
    const f = new Fetches();
    expect([f.begin(), f.begin(), f.begin()]).toEqual([1, 2, 3]);
  });

  it('drops an outcome older than the newest one shown, and shows every newer one', () => {
    const f = new Fetches();
    const a = f.begin();
    const b = f.begin();
    const c = f.begin();
    expect(f.end(b)).toBe(true);
    expect(f.end(a)).toBe(false);
    expect(f.end(c)).toBe(true);
    expect(f.busy).toBe(false);
  });

  it('an older outcome landing after the newest does not let a middle one through later', () => {
    const f = new Fetches();
    const a = f.begin();
    const b = f.begin();
    const c = f.begin();
    expect(f.end(c)).toBe(true);
    expect(f.end(a)).toBe(false);
    expect(f.end(b)).toBe(false);
    expect(f.busy).toBe(false);
  });
});

describe('errorText', () => {
  it('is an Error message, or the thrown value as text', () => {
    expect(errorText(new Error('boom'))).toBe('boom');
    expect(errorText('plain')).toBe('plain');
    expect(errorText(42)).toBe('42');
  });
});

describe('Fetches quiet (live refresh) fetches', () => {
  it('a quiet fetch takes part in newest-wins but is never busy', () => {
    const f = new Fetches();
    const q = f.begin(true);
    expect(f.busy).toBe(false);
    expect(f.end(q)).toBe(true);
    expect(f.busy).toBe(false);
  });

  it('overlapping quiet fetches do not hold a landed load busy', () => {
    const f = new Fetches();
    const load = f.begin();
    const r1 = f.begin(true);
    expect(f.end(load)).toBe(true);
    expect(f.busy).toBe(false);
    const r2 = f.begin(true);
    expect(f.end(r1)).toBe(true);
    expect(f.busy).toBe(false);
    expect(f.end(r2)).toBe(true);
  });

  it('a load stays busy until it lands even while quiet fetches land around it', () => {
    const f = new Fetches();
    const r = f.begin(true);
    const load = f.begin();
    expect(f.end(r)).toBe(true);
    expect(f.busy).toBe(true);
    expect(f.end(load)).toBe(true);
    expect(f.busy).toBe(false);
  });
});
