// The Settings -> API keys page's data layer (SPEC 11, 3.3). `KeysApi` wraps the three key
// operations: every call carries the session cookie, and the writes carry the session's
// `X-CSRF-Token` (SPEC 3.2). `KeysStore` holds what the page shows and is what the page drives:
// the list, the one-time new key, and the error of the last action.
import { mountPrefix, problemDetail } from './apibase';
import { errorText, Fetches } from './fetches';
import type { components } from './api/schema';
import type { Browser } from './session';

export type Key = components['schemas']['Key'];
export type CreatedKey = components['schemas']['CreatedKey'];

/** A refused or failed call; `status` is 0 when the server could not be reached. */
export class KeysError extends Error {
  readonly status: number;
  constructor(status: number, message: string) {
    super(message);
    this.name = 'KeysError';
    this.status = status;
  }
}

/** The text of a problem document's detail (or title), else a line naming the HTTP status. */
export async function problemText(response: Response, what: string): Promise<string> {
  return (await problemDetail(response)) ?? `${what} failed (HTTP ${response.status}).`;
}

export class KeysApi {
  private readonly browser: Browser;
  private readonly csrf: () => string;

  /** `csrf` is read at each write, so a page that rotates its token is followed. */
  constructor(browser: Browser, csrf: () => string) {
    this.browser = browser;
    this.csrf = csrf;
  }

  private async call(path: string, what: string, init: RequestInit): Promise<Response> {
    let response: Response;
    try {
      response = await this.browser.fetch(mountPrefix(this.browser.pathname) + path, { ...init, credentials: 'same-origin' });
    } catch {
      throw new KeysError(0, 'The server is not reachable.');
    }
    if (!response.ok) throw new KeysError(response.status, await problemText(response, what));
    return response;
  }

  async list(): Promise<Key[]> {
    const response = await this.call('/api/v1/keys', 'Listing the keys', { headers: { Accept: 'application/json' } });
    return ((await response.json()) as components['schemas']['KeyList']).items;
  }

  /** `expiresIn` is "90d" or "12h"; blank means the key does not expire. */
  async create(name: string, expiresIn: string): Promise<CreatedKey> {
    const body: components['schemas']['KeyCreate'] = { name };
    if (expiresIn !== '') body.expires_in = expiresIn;
    const response = await this.call('/api/v1/keys', 'Creating the key', {
      method: 'POST',
      headers: { Accept: 'application/json', 'Content-Type': 'application/json', 'X-CSRF-Token': this.csrf() },
      body: JSON.stringify(body),
    });
    return (await response.json()) as CreatedKey;
  }

  async revoke(name: string): Promise<Key> {
    const response = await this.call(`/api/v1/keys/${encodeURIComponent(name)}/revoke`, 'Revoking the key', {
      method: 'POST',
      headers: { Accept: 'application/json', 'X-CSRF-Token': this.csrf() },
    });
    return (await response.json()) as Key;
  }
}

export interface KeysState {
  keys: Key[];
  loading: boolean;
  /** The key just created: shown this once, dropped by `dismiss` or by the next action. */
  created: CreatedKey | undefined;
  /** Why the list or a revoke failed; shown on the page. */
  error: string | undefined;
  /** Why the last create was refused; shown inside the form, which keeps what was typed. */
  formError: string | undefined;
}

export class KeysStore {
  private readonly api: Pick<KeysApi, 'list' | 'create' | 'revoke'>;
  private readonly onChange: (state: KeysState) => void;
  private current: KeysState = { keys: [], loading: true, created: undefined, error: undefined, formError: undefined };
  private readonly fetches = new Fetches();

  constructor(api: Pick<KeysApi, 'list' | 'create' | 'revoke'>, onChange: (state: KeysState) => void) {
    this.api = api;
    this.onChange = onChange;
  }

  get state(): KeysState {
    return this.current;
  }

  private set(patch: Partial<KeysState>): void {
    this.current = { ...this.current, ...patch };
    this.onChange(this.current);
  }

  private async run(action: () => Promise<Partial<KeysState>>, field: 'error' | 'formError' = 'error'): Promise<boolean> {
    try {
      this.set({ ...(await action()), [field]: undefined });
      return true;
    } catch (e) {
      this.set({ [field]: errorText(e) });
      return false;
    }
  }

  /**
   * Fetches the list. Every fetch, the first and each refresh, makes the list `loading` while it is pending; the
   * list stops loading only once no fetch is pending, whether each one worked or failed. Of overlapping fetches the
   * newest outcome to land is shown and an older one landing after it is dropped (`Fetches`).
   */
  async load(): Promise<void> {
    const ticket = this.fetches.begin();
    this.set({ loading: true });
    let outcome: Partial<KeysState>;
    try {
      outcome = { keys: await this.api.list(), error: undefined };
    } catch (e) {
      outcome = { error: errorText(e) };
    }
    const shown = this.fetches.end(ticket) ? outcome : {};
    this.set({ ...shown, loading: this.fetches.busy });
  }

  /**
   * Reads the list again for a live event: the keys stay on screen (no `loading`), the one-time key box and an open form
   * are untouched, and a failed read keeps the list and says why in the inline error. Newest outcome wins (`Fetches`).
   */
  async refresh(): Promise<void> {
    const ticket = this.fetches.begin(true);
    let outcome: Partial<KeysState>;
    try {
      outcome = { keys: await this.api.list(), error: undefined };
    } catch (e) {
      outcome = { error: errorText(e) };
    }
    const shown = this.fetches.end(ticket) ? outcome : {};
    this.set({ ...shown, loading: this.fetches.busy });
  }

  /** Makes the key, keeps it for the page to show once, and reloads the list. Whether it worked; on a refusal `formError` says why. */
  async create(name: string, expiresIn: string): Promise<boolean> {
    const ok = await this.run(async () => ({ created: await this.api.create(name.trim(), expiresIn.trim()) }), 'formError');
    if (ok) await this.load();
    return ok;
  }

  /** Forgets a refusal, for a form that is opened again. */
  clearFormError(): void {
    this.set({ formError: undefined });
  }

  async revoke(name: string): Promise<boolean> {
    const ok = await this.run(async () => {
      await this.api.revoke(name);
      return { created: undefined };
    });
    if (ok) await this.load();
    return ok;
  }

  /** Drops the one-time key from the page. */
  dismiss(): void {
    this.set({ created: undefined });
  }
}

/** Whether a live event can change the key list: a key created or revoked, by anyone. */
export function affectsKeys(event: { type: string }): boolean {
  return event.type.startsWith('key_');
}
