-- +goose Up
-- The indexes the hot reads need (SPEC 4.10), so each plans as a search with no
-- scan of events or tasks and no temporary B-tree for its ORDER BY. Indexes only:
-- no row changes. The DDL is the same in both dialects.
--   events(task_id, n)             the newest event of one task (MAX(n) per task)
--   events(task_id, type, n)       the newest event of one type of one task
--   events(at, n)                  events since a time and the markers in a window, in time order
--   events(actor_id, type, n)      the newest event of one type by one actor
--   tasks(status, id)              the task list by status, in id order
CREATE INDEX events_task_n ON events (task_id, n);
CREATE INDEX events_task_type_n ON events (task_id, type, n);
CREATE INDEX events_at_n ON events (at, n);
CREATE INDEX events_actor_type_n ON events (actor_id, type, n);
CREATE INDEX tasks_status_id ON tasks (status, id);

-- +goose Down
DROP INDEX tasks_status_id;
DROP INDEX events_actor_type_n;
DROP INDEX events_at_n;
DROP INDEX events_task_type_n;
DROP INDEX events_task_n;
