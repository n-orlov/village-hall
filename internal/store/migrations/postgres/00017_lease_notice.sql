-- +goose Up
-- leases.notice (SPEC 4.4): true for a notice lease, a lease that broadcasts an
-- outage, freeze or reboot to every agent and view until it ends or expires;
-- false for an ordinary lease. Every lease that exists before this migration is
-- ordinary.
ALTER TABLE leases ADD COLUMN notice BOOLEAN NOT NULL DEFAULT FALSE;

-- +goose Down
ALTER TABLE leases DROP COLUMN notice;
