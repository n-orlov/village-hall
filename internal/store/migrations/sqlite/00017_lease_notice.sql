-- +goose Up
-- leases.notice (SPEC 4.4): 1 for a notice lease, a lease that broadcasts an
-- outage, freeze or reboot to every agent and view until it ends or expires; 0
-- for an ordinary lease. Every lease that exists before this migration is
-- ordinary. SQLite has no boolean type and stores 0 or 1.
ALTER TABLE leases ADD COLUMN notice INTEGER NOT NULL DEFAULT 0 CHECK (notice IN (0, 1));

-- +goose Down
ALTER TABLE leases DROP COLUMN notice;
