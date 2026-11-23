<script lang="ts">
  import AnswerForm from './AnswerForm.svelte';
  import Dialog from './Dialog.svelte';
  import InitiativeConfirm from './InitiativeConfirm.svelte';
  import NoteForm from './NoteForm.svelte';
  import TaskBody, { type TaskBodyActions } from './TaskBody.svelte';
  import TaskCloseForm from './TaskCloseForm.svelte';
  import TaskDepForm from './TaskDepForm.svelte';
  import TaskEditForm from './TaskEditForm.svelte';
  import TaskNoteForm from './TaskNoteForm.svelte';
  import Toast from './Toast.svelte';
  import { type Ask } from './lib/asks';
  import { copyText } from './lib/clipboard';
  import { type NoteForm as PostForm } from './lib/notes';
  import { currentBrowser } from './lib/session';
  import { toTaskEditForm, type TaskEditForm as EditForm } from './lib/taskEdit';
  import { ACK_BODY, affectsTaskPage, forceDoneBody, leaseHolders, TaskPageStore, type TaskPageState } from './lib/taskPage';
  import { TasksApi, type Resolution } from './lib/tasks';
  import { tagEditor } from './lib/taskTags';
  import { live } from './lib/live';

  let { csrf, param }: { csrf: string; param: string } = $props();

  let view: TaskPageState = $state({ task: undefined, events: [], expired: [], live: [], loading: true, error: undefined, actionError: undefined, candidates: [], asks: [], notes: [], bodies: {} });
  let dialog: 'edit' | 'close' | 'dep' | 'milestone' | 'check' | 'note' | { endLease: string } | { answer: Ask } | { withdraw: string } | { ack: string } | 'force-done' | undefined = $state(undefined);
  let editInitial: EditForm | undefined = $state(undefined);
  let toast = $state('');

  const api = new TasksApi(currentBrowser(), () => csrf);
  const store = new TaskPageStore(api, (s) => (view = s));
  // The tags typing a new one suggests; a failed read leaves no suggestion.
  let inUse: string[] = $state([]);
  $effect(() => {
    void api.tagsInUse().then(
      (tags) => (inUse = tags),
      () => undefined,
    );
  });
  // A closed task takes no tag change (the server refuses it), so its chips have no controls.
  const tags = $derived(view.task === undefined || view.task.status === 'closed' ? undefined : tagEditor(api, view.task.ref, view.task.tag_marks, inUse, () => store.refresh(param)));

  $effect(() => {
    void store.load(param);
    // An event that concerns the task reads the page again in place: no skeleton, so an open dialog keeps what was typed.
    return live().subscribe(affectsTaskPage(param), () => store.refresh(param));
  });

  // The refs of the task page are written in either letter case; the API answers with the canonical ref.
  const ref = $derived(view.task?.ref ?? param);

  function openEdit(initial: EditForm) {
    store.clearActionError();
    editInitial = initial;
    dialog = 'edit';
  }

  function openClose() {
    store.clearActionError();
    dialog = 'close';
  }

  function openDep() {
    store.clearActionError();
    dialog = 'dep';
    void store.loadCandidates();
  }

  function openNote(kind: 'milestone' | 'check' | 'note' | 'force-done' | { endLease: string } | { answer: Ask } | { withdraw: string } | { ack: string }) {
    store.clearActionError();
    dialog = kind;
  }

  async function finish(done: Promise<boolean>, message: string) {
    if (await done) {
      dialog = undefined;
      toast = message;
    }
  }

  const done = (force = false) => finish(store.done(ref, force), `Task ${ref} marked done`);
  // Done over another actor's live lease asks first and then sends `force`; a task nobody else holds is marked done at once.
  const pressDone = () => (leaseHolders(view.task ?? { live_leases: [] }).length > 0 ? openNote('force-done') : done());
  const reopen = () => finish(store.reopen(ref), `Task ${ref} reopened`);
  const save = (form: EditForm, kind: string) => finish(store.edit(ref, form, editInitial ?? form, kind), `Task ${ref} saved`);
  const closeIt = (resolution: Resolution) => finish(store.close(ref, resolution), `Task ${ref} closed as ${resolution}`);

  const addDep = (blocker: string) => finish(store.addDep(ref, blocker), `Task ${ref} is now blocked by ${blocker}`);
  const removeDep = (blocker: string) => finish(store.removeDep(ref, blocker), `Task ${ref} no longer waits for ${blocker}`);
  const milestone = (text: string) => finish(store.milestone(ref, text), `Milestone recorded on ${ref}`);
  const check = (text: string) => finish(store.recordCheck(ref, text), `Check recorded on ${ref}`);
  const endLease = (lease: string, outcome: string) => finish(store.endLease(ref, lease, outcome), `Lease ${lease} ended`);
  const answer = (ask: Ask, text: string) => finish(store.answer(ref, ask.ref, text), `Answered ${ask.ref}`);
  const withdraw = (askRef: string, text: string) => finish(store.withdraw(ref, askRef, text), `Withdrew ${askRef}`);
  const postNote = (form: PostForm) => finish(store.postNote(ref, form), `Note posted on ${ref}`);

  const ack = (lease: string) => finish(store.ackLease(ref, lease), `Lease ${lease} acknowledged`);

  async function copyWayBack(holder: string, wayBack: string) {
    const ok = await copyText(navigator.clipboard, wayBack);
    toast = ok ? `Copied the way back of ${holder}` : 'Copying failed. Select the way back and copy it by hand.';
  }

  // The body's buttons open the page's dialogs; the timeline's preview draws the same body without them.
  const on: TaskBodyActions = {
    edit: (task) => openEdit(toTaskEditForm(task)),
    done: () => void pressDone(),
    reopen: () => void reopen(),
    close: openClose,
    copyWayBack: (holder, wayBack) => void copyWayBack(holder, wayBack),
    ack: (lease) => openNote({ ack: lease }),
    endLease: (lease) => openNote({ endLease: lease }),
    answer: (ask) => openNote({ answer: ask }),
    withdraw: (askRef) => openNote({ withdraw: askRef }),
    addDep: openDep,
    removeDep: (blocker) => void removeDep(blocker),
    note: () => openNote('note'),
    milestone: () => openNote('milestone'),
    check: () => openNote('check'),
  };
  const back = 'inline-flex min-h-11 items-center text-sm font-medium text-accent hover:underline';
</script>

{#snippet nav()}
  <div class="flex flex-wrap items-center gap-x-6">
    <a class={back} href="#/tasks" data-testid="back-link">← Tasks</a>
    {#if view.task !== undefined}
      <a class="{back} max-w-full wrap-anywhere" href="#/i/{view.task.initiative}" data-testid="task-initiative">{view.task.initiative}</a>
    {/if}
  </div>
{/snippet}

<TaskBody
  {view}
  headingId="task-heading"
  testid="task-page"
  wide
  {on}
  {nav}
  {tags}
  actionError={dialog === undefined ? view.actionError : undefined}
  onexpand={(noteRef) => void store.loadBody(noteRef)}
/>


<Toast message={toast} onclear={() => (toast = '')} />

{#if dialog !== undefined && view.task !== undefined}
  {@const task = view.task}
  {#if dialog === 'edit' && editInitial !== undefined}
    <Dialog label="Edit task" onclose={() => (dialog = undefined)} initialFocus="input[name=title]">
      <TaskEditForm initial={editInitial} watch={task.kind === 'watch'} error={view.actionError} onsave={(form) => save(form, task.kind)} oncancel={() => (dialog = undefined)} />
    </Dialog>
  {:else if dialog === 'dep'}
    <Dialog label="Add dependency" onclose={() => (dialog = undefined)} initialFocus="input[name=blocker_search]">
      <TaskDepForm {task} candidates={view.candidates} error={view.actionError} onpick={addDep} oncancel={() => (dialog = undefined)} />
    </Dialog>
  {:else if dialog === 'milestone'}
    <Dialog label="Record milestone" onclose={() => (dialog = undefined)} initialFocus="input[name=text]">
      <TaskNoteForm heading="Record milestone" label="Summary" hint="What happened, in up to 200 characters." submitLabel="Record milestone" error={view.actionError} onsubmit={milestone} oncancel={() => (dialog = undefined)} />
    </Dialog>
  {:else if dialog === 'check'}
    <Dialog label="Record check" onclose={() => (dialog = undefined)} initialFocus="input[name=text]">
      <TaskNoteForm heading="Record check" label="Summary" hint="What the check found, in up to 200 characters. The next check is due one cadence from now." submitLabel="Record check" error={view.actionError} onsubmit={check} oncancel={() => (dialog = undefined)} />
    </Dialog>
  {:else if dialog === 'note'}
    <Dialog label="Post note" onclose={() => (dialog = undefined)} initialFocus="input[name=summary]">
      <NoteForm heading="Post a note on {task.ref}" error={view.actionError} onsubmit={postNote} oncancel={() => (dialog = undefined)} />
    </Dialog>
  {:else if typeof dialog === 'object' && 'answer' in dialog}
    {@const target = dialog.answer}
    <Dialog label="Answer ask" onclose={() => (dialog = undefined)} initialFocus="textarea[name=answer]">
      <AnswerForm ask={target} error={view.actionError} onsubmit={(text) => answer(target, text)} oncancel={() => (dialog = undefined)} />
    </Dialog>
  {:else if typeof dialog === 'object' && 'withdraw' in dialog}
    {@const askRef = dialog.withdraw}
    <Dialog label="Withdraw ask" onclose={() => (dialog = undefined)} initialFocus="input[name=text]">
      <TaskNoteForm heading="Withdraw {askRef}" label="Reason (optional)" hint="Why the ask is no longer needed, in up to 200 characters." submitLabel="Withdraw" error={view.actionError} onsubmit={(text) => withdraw(askRef, text)} oncancel={() => (dialog = undefined)} />
    </Dialog>
  {:else if dialog === 'force-done'}
    {@const holders = leaseHolders(view.task ?? { live_leases: [] })}
    <Dialog label="Mark {ref} done" onclose={() => (dialog = undefined)}>
      <InitiativeConfirm heading="Mark {ref} done" body={forceDoneBody(holders)} confirm="Mark done anyway" danger error={view.actionError} onconfirm={() => done(true)} oncancel={() => (dialog = undefined)} />
    </Dialog>
  {:else if typeof dialog === 'object' && 'ack' in dialog}
    {@const lease = dialog.ack}
    <Dialog label="Acknowledge lease {lease}" onclose={() => (dialog = undefined)}>
      <InitiativeConfirm heading="Acknowledge {lease}" body={ACK_BODY} confirm="Acknowledge lease" error={view.actionError} onconfirm={() => ack(lease)} oncancel={() => (dialog = undefined)} />
    </Dialog>
  {:else if typeof dialog === 'object'}
    {@const lease = dialog.endLease}
    <Dialog label="End lease" onclose={() => (dialog = undefined)} initialFocus="input[name=text]">
      <TaskNoteForm heading="End lease {lease}" label="Outcome (optional)" hint="How it went, in up to 200 characters." submitLabel="End lease" error={view.actionError} onsubmit={(text) => endLease(lease, text)} oncancel={() => (dialog = undefined)} />
    </Dialog>
  {:else if dialog === 'close'}
    <Dialog label="Close task" onclose={() => (dialog = undefined)} initialFocus="select[name=resolution]">
      <TaskCloseForm ref={task.ref} error={view.actionError} onconfirm={closeIt} oncancel={() => (dialog = undefined)} />
    </Dialog>
  {/if}
{/if}
