// mutationlanes_test.go pins the balance of the Jenkinsfile's full-mutation
// units (task 012, R2): the three FULL_MUTATION units, summed over the
// per-lane minutes of testdata/mutation_lane_minutes.json, each predict at most
// the table's limit (90 minutes, under the 95-minute budget and the 110-minute
// bound), every lane of ci/quality.json's mutation_go.lanes sits in exactly one
// unit, Stryker rides with lane base, and no two parts of internal/server share
// a unit (so an incremental run with internal/server changed runs its three
// parts side by side). The prediction is a pure function of the Jenkinsfile
// text and the table, fed the committed file and doctored copies.
package repocheck

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"testing"
)

type laneSpeed struct {
	Task   string  `json:"task"`
	Factor float64 `json:"factor"`
	Basis  string  `json:"basis"`
}

type laneMinutes struct {
	Baseline float64     `json:"baseline_minutes"`
	Speed    []laneSpeed `json:"speed"`
}

type laneTable struct {
	Calibration float64                `json:"calibration"`
	FixedShare  float64                `json:"fixed_share"`
	Limit       float64                `json:"limit_minutes"`
	Lanes       map[string]laneMinutes `json:"lanes"`
}

// predicted is a lane's minutes: calibration x baseline x (the share that does
// not scale with test time + the rest times every measured speed factor).
func (tb laneTable) predicted(lane string) float64 {
	l := tb.Lanes[lane]
	scale := 1.0
	for _, s := range l.Speed {
		scale *= s.Factor
	}
	return tb.Calibration * l.Baseline * (tb.FixedShare + (1-tb.FixedShare)*scale)
}

// strykerLane is the table's row for Stryker, which ci/mutation.sh runs at the
// end of the unit holding lane base and which is no lane of quality.json.
const strykerLane = "stryker"

// unitMinutes sums a unit's lanes, plus Stryker when it holds lane base.
func (tb laneTable) unitMinutes(unit []string) float64 {
	var sum float64
	for _, l := range unit {
		sum += tb.predicted(l)
		if l == "base" {
			sum += tb.predicted(strykerLane)
		}
	}
	return sum
}

// laneBalanceProblems: exactly 3 units, every lane of quality.json in exactly
// one of them, the table covering exactly those lanes (and Stryker), each
// unit's predicted minutes within the limit, and no two internal/server parts
// in one unit.
func laneBalanceProblems(units [][]string, lanes map[string][]string, tb laneTable) []string {
	problems := fullLaneProblems(units, lanes)
	if tb.Limit <= 0 || tb.Calibration <= 0 || tb.FixedShare < 0 || tb.FixedShare >= 1 {
		problems = append(problems, "the lane table has no usable limit, calibration or fixed share")
	}
	for l := range lanes {
		if _, ok := tb.Lanes[l]; !ok {
			problems = append(problems, "the lane table has no minutes for lane "+l)
		}
	}
	for l, m := range tb.Lanes {
		if _, ok := lanes[l]; !ok && l != strykerLane {
			problems = append(problems, "the lane table has minutes for lane "+l+", which ci/quality.json does not define")
		}
		if m.Baseline <= 0 {
			problems = append(problems, "the lane table has no baseline minutes for lane "+l)
		}
		for _, s := range m.Speed {
			if s.Factor <= 0 || s.Factor > 1 || s.Task == "" || s.Basis == "" {
				problems = append(problems, "lane "+l+" has a speed factor without a task, a basis or a value in (0, 1]")
			}
		}
	}
	if _, ok := tb.Lanes[strykerLane]; !ok {
		problems = append(problems, "the lane table has no minutes for Stryker")
	}
	if len(problems) != 0 {
		sort.Strings(problems)
		return problems
	}
	isServerPart := func(l string) bool {
		for _, p := range lanes[l] {
			if strings.HasPrefix(p, "internal/server#") {
				return true
			}
		}
		return false
	}
	for i, u := range units {
		parts := 0
		for _, l := range u {
			if isServerPart(l) {
				parts++
			}
		}
		if parts > 1 {
			problems = append(problems, fmt.Sprintf("unit %d (%s) holds %d parts of internal/server, want at most 1", i+1, strings.Join(u, ","), parts))
		}
		if m := tb.unitMinutes(u); m > tb.Limit {
			problems = append(problems, fmt.Sprintf("unit %d (%s) is predicted at %.1f minutes, over the %.0f limit", i+1, strings.Join(u, ","), m, tb.Limit))
		}
	}
	sort.Strings(problems)
	return problems
}

func readLaneTable(t *testing.T) laneTable {
	t.Helper()
	var tb laneTable
	if err := json.Unmarshal([]byte(readRepoFile(t, "ci", "repocheck", "testdata", "mutation_lane_minutes.json")), &tb); err != nil {
		t.Fatal(err)
	}
	return tb
}

func readMutationLanes(t *testing.T) map[string][]string {
	t.Helper()
	var cfg struct {
		MutationGo struct {
			Lanes map[string][]string `json:"lanes"`
		} `json:"mutation_go"`
	}
	if err := json.Unmarshal([]byte(readRepoFile(t, "ci", "quality.json")), &cfg); err != nil {
		t.Fatal(err)
	}
	return cfg.MutationGo.Lanes
}

// TestFullMutationUnitsAreBalancedFromTheLaneTable: the committed
// FULL_MUTATION predicts at most 90 minutes a unit; the same check fails on a
// Jenkinsfile that puts server-core and server-ops in one unit, on a unit
// holding all the heavy lanes, and on a table that drops a lane or grows one.
func TestFullMutationUnitsAreBalancedFromTheLaneTable(t *testing.T) {
	jf := readRepoFile(t, "Jenkinsfile")
	lanes := readMutationLanes(t)
	tb := readLaneTable(t)
	units := fullMutationLanes(jf)
	for _, p := range laneBalanceProblems(units, lanes, tb) {
		t.Error(p)
	}
	for i, u := range units {
		t.Logf("unit %d %v: predicted %.1f minutes", i+1, u, tb.unitMinutes(u))
	}

	// A table that has not been read as a whole proves nothing about the
	// units: the units' own lane lists are read from the Jenkinsfile text.
	merged := replaceOnce(t,
		replaceOnce(t, jf, "lanes: 'server-core,logic'", "lanes: 'server-core,server-ops,logic'"),
		"lanes: 'server-ops,cli,cli-core'", "lanes: 'cli,cli-core'")
	if got := laneBalanceProblems(fullMutationLanes(merged), lanes, tb); len(got) == 0 {
		t.Error("server-core and server-ops in one unit passes the balance check")
	} else if !strings.Contains(strings.Join(got, "\n"), "parts of internal/server") {
		t.Errorf("server-core and server-ops in one unit is not named as two server parts: %v", got)
	}
	overloaded := replaceOnce(t,
		replaceOnce(t, jf, "lanes: 'server-core,logic'", "lanes: 'server-core,logic,cli,cli-core'"),
		"lanes: 'server-ops,cli,cli-core'", "lanes: 'server-ops'")
	if got := laneBalanceProblems(fullMutationLanes(overloaded), lanes, tb); !strings.Contains(strings.Join(got, "\n"), "over the 90 limit") {
		t.Errorf("a unit over the limit is not named: %v", got)
	}
	for name, doctored := range map[string]func(laneTable) laneTable{
		"a lane without minutes": func(x laneTable) laneTable {
			y := laneTable{Calibration: x.Calibration, FixedShare: x.FixedShare, Limit: x.Limit, Lanes: map[string]laneMinutes{}}
			for l, m := range x.Lanes {
				if l != "cli" {
					y.Lanes[l] = m
				}
			}
			return y
		},
		"Stryker without minutes": func(x laneTable) laneTable {
			y := laneTable{Calibration: x.Calibration, FixedShare: x.FixedShare, Limit: x.Limit, Lanes: map[string]laneMinutes{}}
			for l, m := range x.Lanes {
				if l != strykerLane {
					y.Lanes[l] = m
				}
			}
			return y
		},
		"a heavier Stryker": func(x laneTable) laneTable {
			y := laneTable{Calibration: x.Calibration, FixedShare: x.FixedShare, Limit: x.Limit, Lanes: map[string]laneMinutes{}}
			for l, m := range x.Lanes {
				y.Lanes[l] = m
			}
			y.Lanes[strykerLane] = laneMinutes{Baseline: 80}
			return y
		},
		"a speed factor of more than 1": func(x laneTable) laneTable {
			y := laneTable{Calibration: x.Calibration, FixedShare: x.FixedShare, Limit: x.Limit, Lanes: map[string]laneMinutes{}}
			for l, m := range x.Lanes {
				y.Lanes[l] = m
			}
			y.Lanes["cli"] = laneMinutes{Baseline: 37, Speed: []laneSpeed{{Task: "024", Factor: 1.4, Basis: "x"}}}
			return y
		},
	} {
		if got := laneBalanceProblems(units, lanes, doctored(tb)); len(got) == 0 {
			t.Errorf("%s passes the balance check", name)
		}
	}
	for name, doctored := range map[string]string{
		"a lane left out": replaceOnce(t, jf, "lanes: 'server-core,logic'", "lanes: 'server-core'"),
		"a fourth unit":   replaceOnce(t, jf, "  [name: 'Mutation streams+base+Stryker', dir: 'base', lanes: 'server-streams,base'],", "  [name: 'Mutation streams+Stryker', dir: 'base', lanes: 'server-streams'],\n  [name: 'Mutation base', dir: 'b', lanes: 'base'],"),
	} {
		if len(laneBalanceProblems(fullMutationLanes(doctored), lanes, tb)) == 0 {
			t.Errorf("%s passes the balance check", name)
		}
	}
}
