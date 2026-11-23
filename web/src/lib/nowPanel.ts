// The Now panel's data (PRD R11, SPEC 11): `NowStore` reads the live leases of the board for the Tasks view; what the
// panel prints is `nowPanel` in conflicts.ts. A failed read keeps the panel's last leases and says why.
import { errorText, Fetches } from './fetches';
import type { Lease } from './conflicts';
import type { TasksApi } from './tasks';

export interface NowState {
  leases: Lease[];
  loading: boolean;
  /** Why the live leases could not be read. */
  error: string | undefined;
}

type NowApi = Pick<TasksApi, 'liveLeases'>;

export class NowStore {
  private readonly api: NowApi;
  private readonly onChange: (state: NowState) => void;
  private current: NowState = { leases: [], loading: true, error: undefined };
  private readonly fetches = new Fetches();

  constructor(api: NowApi, onChange: (state: NowState) => void) {
    this.api = api;
    this.onChange = onChange;
  }

  get state(): NowState {
    return this.current;
  }

  private set(patch: Partial<NowState>): void {
    this.current = { ...this.current, ...patch };
    this.onChange(this.current);
  }

  /** Reads the live leases; of overlapping reads the newest outcome to land is shown. */
  async load(): Promise<void> {
    const ticket = this.fetches.begin();
    this.set({ loading: true });
    let outcome: Partial<NowState>;
    try {
      outcome = { leases: await this.api.liveLeases(), error: undefined };
    } catch (e) {
      outcome = { error: errorText(e) };
    }
    const shown = this.fetches.end(ticket) ? outcome : {};
    this.set({ ...shown, loading: this.fetches.busy });
  }

  /** Reads the live leases again for a live event: the panel keeps what it shows (never `loading`); a failed read keeps the leases and says why. */
  async refresh(): Promise<void> {
    const ticket = this.fetches.begin(true);
    let outcome: Partial<NowState>;
    try {
      outcome = { leases: await this.api.liveLeases(), error: undefined };
    } catch (e) {
      outcome = { error: errorText(e) };
    }
    const shown = this.fetches.end(ticket) ? outcome : {};
    this.set({ ...shown, loading: this.fetches.busy });
  }
}

/** Whether a live event can change the Now panel: a lease started, renewed, ended, acknowledged or expired. */
export function affectsNow(event: { type: string }): boolean {
  return event.type.startsWith('lease_');
}
