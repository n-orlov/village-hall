package store

import (
	"fmt"
	"strings"
)

// The notification kinds an alert can have, and the words of VH_NOTIFY_ON.
const (
	NotifyStranded = "stranded"
	NotifyAsking   = "asking"
	NotifyDone     = "done"
)

// NotifyKinds lists the kinds in the order VH_NOTIFY_ON defaults to.
var NotifyKinds = []string{NotifyStranded, NotifyAsking, NotifyDone}

// ParseNotifyOn reads a VH_NOTIFY_ON value: empty means every kind, and
// otherwise a comma list of the kinds' words. Any other word, an empty word or a
// list that names nothing is an error.
func ParseNotifyOn(v string) ([]string, error) {
	v = strings.TrimSpace(v)
	if v == "" {
		return append([]string(nil), NotifyKinds...), nil
	}
	var kinds []string
	for _, word := range strings.Split(v, ",") {
		word = strings.TrimSpace(word)
		if !knownNotifyKind(word) {
			return nil, fmt.Errorf("VH_NOTIFY_ON=%q: %q is not one of stranded, asking, done", v, word)
		}
		kinds = append(kinds, word)
	}
	return kinds, nil
}

func knownNotifyKind(word string) bool {
	for _, k := range NotifyKinds {
		if k == word {
			return true
		}
	}
	return false
}

// SetNotify turns alert enqueueing on for the kinds, or off for none. serve
// calls it once, before it takes a request, when VH_NOTIFY_URL is set.
func (s *Store) SetNotify(kinds []string) {
	set := make(map[string]bool, len(kinds))
	for _, k := range kinds {
		set[k] = true
	}
	s.notify.Store(&set)
}

// Notifies reports whether a write that triggers an alert of the kind enqueues
// one: alerts are on and VH_NOTIFY_ON names the kind.
func (s *Store) Notifies(kind string) bool {
	set := s.notify.Load()
	return set != nil && (*set)[kind]
}

// SetPublicURL records VH_PUBLIC_URL, the base the links in an alert are built
// from. serve calls it once, before it takes a request.
func (s *Store) SetPublicURL(u string) { s.publicURL.Store(&u) }

// PublicURL returns what SetPublicURL recorded, or "" when it was never called.
func (s *Store) PublicURL() string {
	if u := s.publicURL.Load(); u != nil {
		return *u
	}
	return ""
}

// SetNotifyWake registers what wakes the notification worker once a write that
// enqueued an alert has committed; nil clears it. The worker registers itself
// while it runs.
func (s *Store) SetNotifyWake(f func()) {
	if f == nil {
		s.notifyWake.Store(nil)
		return
	}
	s.notifyWake.Store(&f)
}

// WakeNotify calls what SetNotifyWake registered, if anything.
func (s *Store) WakeNotify() {
	if f := s.notifyWake.Load(); f != nil {
		(*f)()
	}
}
