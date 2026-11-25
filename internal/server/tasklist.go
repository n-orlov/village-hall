package server

import (
	"context"
	"errors"
	"net/http"

	"github.com/n-orlov/village-hall/internal/attention"
	"github.com/n-orlov/village-hall/internal/leases"
	"github.com/n-orlov/village-hall/internal/tasks"
)

func (p problemResponse) VisitListTasksResponse(w http.ResponseWriter) error { return p.write(w) }

func (p problemResponse) VisitCountTasksResponse(w http.ResponseWriter) error { return p.write(w) }

func orEmpty[T any](list *[]T) []T {
	if list == nil {
		return nil
	}
	return *list
}

// taskQuery is the store's query for the parameters of GET /api/v1/tasks.
func taskQuery(p ListTasksParams, needing []string) tasks.Query {
	q := tasks.Query{
		Refs: orEmpty(p.Ref), Initiatives: orEmpty(p.Initiative), Tags: orEmpty(p.Tag), Active: p.Active, Blocked: p.Blocked, Overdue: p.Overdue,
		Attention: p.Attention, AttentionRefs: needing,
		ClosedWithin: text(p.ClosedWithin), Q: text(p.Q), After: text(p.After), Limit: pageLimit(p.Limit),
	}
	if p.Status != nil {
		q.Status = string(*p.Status)
	}
	if p.Kind != nil {
		q.Kind = string(*p.Kind)
	}
	return q
}

func apiTaskListItem(t tasks.Task, live []leases.Lease) TaskListItem {
	out := TaskListItem{
		Id: t.ID, Ref: t.Ref, Initiative: t.Initiative, Seq: t.Seq, Title: t.Title, Description: t.Description,
		Kind: TaskListItemKind(t.Kind), Status: TaskListItemStatus(t.Status),
		PlannedStart: t.PlannedStart, PlannedEnd: t.PlannedEnd, CadenceSeconds: t.CadenceSeconds, NextCheckAt: t.NextCheckAt,
		ExternalRefs: apiExternalRefs(t.ExternalRefs), Tags: nonNil(t.Tags), TagMarks: apiTagMarks(t.TagMarks),
		CreatedBy: t.CreatedBy, CreatedAt: t.CreatedAt, UpdatedAt: t.UpdatedAt,
		DoneAt: t.DoneAt, DoneBy: optionalText(t.DoneBy), ClosedAt: t.ClosedAt, ClosedBy: optionalText(t.ClosedBy),
		Active: len(live) > 0, Blocked: t.Blocked, Overdue: t.Overdue, LiveLeases: make([]Lease, len(live)),
	}
	if t.Resolution != "" {
		r := TaskListItemResolution(t.Resolution)
		out.Resolution = &r
	}
	for i, l := range live {
		out.LiveLeases[i] = apiLease(l)
	}
	return out
}

const nextTaskCursor = "Leave out 'after' to start at the first page, or send the next_after of the previous page."

// ListTasks lists a page of tasks with their live leases. It expires the leases
// that ran out first, once, so a task whose lease ran out is not active. The
// attention derivation expires them as part of its own read, so the handler
// expires them itself only when it has no derivation; an initiative filter
// scopes the derivation to those initiatives.
func (o taskOps) ListTasks(ctx context.Context, req ListTasksRequestObject) (ListTasksResponseObject, error) {
	if r, off := o.ready(); off {
		return r, nil
	}
	reasons, err := o.expiredAndDerived(ctx, orEmpty(req.Params.Initiative))
	if err != nil {
		return nil, err
	}
	page, err := o.tasks.List(ctx, taskQuery(req.Params, needing(reasons)))
	if errors.Is(err, tasks.ErrBadCursor) {
		return failure(http.StatusBadRequest, err.Error()).withNext(nextTaskCursor), nil
	}
	if err != nil {
		return taskResult[ListTasksResponseObject](err, "there is no task ")
	}
	out, err := o.listed(ctx, page, reasons)
	if err != nil {
		return nil, err
	}
	return ListTasks200JSONResponse(out), nil
}

// expiredAndDerived expires the due leases once and derives the attention of the
// tasks of the initiatives. An initiative that names nothing leaves the
// derivation empty, and the list read refuses it with its own 404.
func (o taskOps) expiredAndDerived(ctx context.Context, initiatives []string) (attention.Derivation, error) {
	if o.reasons == nil {
		_, err := o.leases.ExpireDue(ctx)
		return attention.Derivation{}, err
	}
	d, err := o.derived(ctx, initiatives...)
	if errors.Is(err, attention.ErrNotFound) {
		return attention.Derivation{}, nil
	}
	return d, err
}

// listed is the document for a page: each task with its live leases, read for
// the whole page in one query.
func (o taskOps) listed(ctx context.Context, page tasks.Page, reasons attention.Derivation) (TaskList, error) {
	ids := make([]string, len(page.Items))
	for i, t := range page.Items {
		ids[i] = t.ID
	}
	live, err := o.leases.LiveFor(ctx, ids)
	if err != nil {
		return TaskList{}, err
	}
	out := TaskList{Items: make([]TaskListItem, len(page.Items)), NextAfter: optionalText(page.NextAfter)}
	for i, t := range page.Items {
		out.Items[i] = apiTaskListItem(t, live[t.ID])
		out.Items[i].Attention, out.Items[i].StrandedLease = apiTaskAttention(reasons.Of(t.Ref))
	}
	return out, nil
}

// CountTasks answers how many tasks the filters keep, and no rows: the count form of ListTasks, with its
// expiry and its attention derivation, over the filters that have no page and no text.
func (o taskOps) CountTasks(ctx context.Context, req CountTasksRequestObject) (CountTasksResponseObject, error) {
	if r, off := o.ready(); off {
		return r, nil
	}
	p := req.Params
	reasons, err := o.expiredAndDerived(ctx, orEmpty(p.Initiative))
	if err != nil {
		return nil, err
	}
	q := taskQuery(ListTasksParams{
		Ref: p.Ref, Initiative: p.Initiative, Tag: p.Tag, Active: p.Active, Blocked: p.Blocked, Overdue: p.Overdue,
		Attention: p.Attention, ClosedWithin: p.ClosedWithin,
	}, needing(reasons))
	if p.Status != nil {
		q.Status = string(*p.Status)
	}
	if p.Kind != nil {
		q.Kind = string(*p.Kind)
	}
	n, err := o.tasks.Count(ctx, q)
	if err != nil {
		return taskResult[CountTasksResponseObject](err, "there is no task ")
	}
	return CountTasks200JSONResponse(TaskCount{Count: n}), nil
}

func (p problemResponse) VisitGetTagSourcesResponse(w http.ResponseWriter) error { return p.write(w) }

// GetTagSources answers {tag: [sources]} for the tasks the status keeps: the sources every tag of a view has,
// in one read, where the list would be paged once per tag. It reads no lease state, so it expires nothing.
func (o taskOps) GetTagSources(ctx context.Context, req GetTagSourcesRequestObject) (GetTagSourcesResponseObject, error) {
	if r, off := o.ready(); off {
		return r, nil
	}
	status := ""
	if req.Params.Status != nil {
		status = string(*req.Params.Status)
	}
	got, err := o.tasks.TagSources(ctx, status, orEmpty(req.Params.Tag))
	if err != nil {
		return taskResult[GetTagSourcesResponseObject](err, "there is no task ")
	}
	return GetTagSources200JSONResponse(got), nil
}
