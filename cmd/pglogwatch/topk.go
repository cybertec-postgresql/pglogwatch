package main

import (
	"sort"
	"strconv"
)

// Top-N counting.
//
// Every report that says "the ten most common X" faces the same problem: the
// number of DISTINCT values is unbounded. A log with a million distinct error
// messages -- which is what any server logging query text produces, since the
// parameters differ every time -- would put a million strings in a map to
// answer a question about ten of them.
//
// PERF-028 requires these aggregations to be O(K) in memory rather than
// O(distinct). counter is that bound; T133 is where it stops being a plain map.

// counted is one aggregated group.
type counted struct {
	key   string
	count int64

	// total and worst are carried for the reports that summarise a value
	// as well as counting occurrences -- slow statements need both the
	// number of executions and the slowest one.
	total int64
	worst int64

	// sample is one representative record's text, kept so a report can show
	// what a group looks like without storing every member.
	sample string
}

// counter aggregates by key, keeping at most a bounded number of groups.
type counter struct {
	limit  int
	groups map[string]*counted

	// dropped counts groups evicted for exceeding the bound, so a report
	// can say its top-N is drawn from a truncated set rather than implying
	// it saw everything.
	dropped int64
}

// newCounter returns a counter that tracks at most limit*trackingFactor groups.
func newCounter(limit int) *counter {
	return &counter{limit: limit, groups: make(map[string]*counted, limit*trackingFactor)}
}

// trackingFactor is how many groups are tracked per row eventually reported.
//
// Tracking exactly N would make the result depend on arrival order: a group
// that becomes the most frequent late would already have been evicted. Keeping
// a multiple gives late-rising groups room to establish themselves while still
// bounding memory, which is the trade PERF-028 asks for.
const trackingFactor = 32

// add records one occurrence of key.
func (c *counter) add(key string, value int64, sample string) {
	if g, ok := c.groups[key]; ok {
		g.count++
		g.total += value
		if value > g.worst {
			g.worst = value
		}
		return
	}
	if len(c.groups) >= c.limit*trackingFactor {
		c.evictSmallest()
	}
	c.groups[key] = &counted{key: key, count: 1, total: value, worst: value, sample: sample}
}

// evictSmallest removes the least frequent group.
//
// A linear scan over a bounded map, run only when the map is full, which for a
// default limit of 10 means 320 comparisons on the rare occasions it happens.
// A heap would be asymptotically better and slower in practice at this size.
func (c *counter) evictSmallest() {
	var victim string
	var least int64 = -1
	for k, g := range c.groups {
		if least < 0 || g.count < least {
			victim, least = k, g.count
		}
	}
	if victim != "" {
		delete(c.groups, victim)
		c.dropped++
	}
}

// top returns the n most frequent groups, most frequent first.
//
// Ties break on the key so the output is deterministic: a report whose row
// order changes between runs over the same log cannot be diffed, and diffing
// two reports is most of what these are used for.
func (c *counter) top(n int) []*counted {
	out := make([]*counted, 0, len(c.groups))
	for _, g := range c.groups {
		out = append(out, g)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].count != out[j].count {
			return out[i].count > out[j].count
		}
		return out[i].key < out[j].key
	})
	if len(out) > n {
		out = out[:n]
	}
	return out
}

// bySlowest returns the n groups with the largest worst value.
func (c *counter) bySlowest(n int) []*counted {
	out := make([]*counted, 0, len(c.groups))
	for _, g := range c.groups {
		out = append(out, g)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].worst != out[j].worst {
			return out[i].worst > out[j].worst
		}
		return out[i].key < out[j].key
	})
	if len(out) > n {
		out = out[:n]
	}
	return out
}

func itoa(v int64) string { return strconv.FormatInt(v, 10) }
