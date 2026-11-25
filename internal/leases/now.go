package leases

import (
	"context"
	"time"

	"github.com/n-orlov/village-hall/internal/domain"
	"github.com/n-orlov/village-hall/internal/store"
)

// Now is the lease and conflict parts of an actor's check-in (SPEC 6.5): the
// actor's live leases, those of the others, the live notices of everyone, and
// the conflicts. Mine and Others hold ordinary leases only; every live notice,
// the actor's own included, is in Notices.
type Now struct {
	Mine      []Lease
	Others    []Lease
	Notices   []Lease
	Conflicts []domain.Holding
}

// Live lists every live lease whose expires_at is not before now, oldest first.
// Like LiveFor it leaves out the leases that have run out without expiring them.
func (s Store) Live(ctx context.Context) ([]Lease, error) { return s.liveAt(ctx, s.now()) }

// liveAt is Live judged at the instant now.
func (s Store) liveAt(ctx context.Context, now time.Time) ([]Lease, error) {
	return s.readLeases(ctx, selectLease+` WHERE l.state = 'live' AND l.expires_at >= ? ORDER BY l.n`,
		[]any{store.EncodeBound(now)})
}

// CheckIn answers the check-in of the actor with the handle me. It expires the due
// leases first (each with its one lease_expired event), so no lease it lists is
// past its expiry. queried are resources to check before starting: the live
// leases of others that hold one are conflicts too (domain.NowConflicts). Every
// list is empty, never nil.
func (s Store) CheckIn(ctx context.Context, me string, queried []string) (Now, error) {
	now := s.now()
	if _, err := s.expireAt(ctx, now); err != nil {
		return Now{}, err
	}
	live, err := s.liveAt(ctx, now)
	if err != nil {
		return Now{}, err
	}
	out := Now{Mine: []Lease{}, Others: []Lease{}, Notices: []Lease{}}
	holdings := make([]domain.Holding, 0, len(live))
	for _, l := range live {
		if l.Notice {
			out.Notices = append(out.Notices, l)
			continue
		}
		holdings = append(holdings, l.Holding())
		if l.Actor == me {
			out.Mine = append(out.Mine, l)
		} else {
			out.Others = append(out.Others, l)
		}
	}
	out.Conflicts = domain.NowConflicts(me, holdings, queried)
	return out, nil
}
