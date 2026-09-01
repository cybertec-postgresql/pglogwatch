package main

import (
	"cmp"
	"flag"
	"maps"
	"slices"
	"time"

	"github.com/cybertec-postgresql/pglogwatch"
)

func init() {
	commands["peaks"] = command{
		summary: "busiest time buckets, ten minutes by default",
		flags: func(fs *flag.FlagSet, o *options) {
			fs.DurationVar(&o.bucket, "bucket", 10*time.Minute,
				"time bucket width, e.g. 1m or 1h")
		},
		run: runPeaks,
	}
}

// bucketStat is one time bucket's counts.
type bucketStat struct {
	start    time.Time
	records  int64
	problems int64
	slow     int64
}

// runPeaks reports when the log was busiest.
//
// The unit is records per bucket, which is a proxy for load rather than a
// measure of it: a server logging only errors produces few records under heavy
// use. The report is for finding WHEN something happened -- the ten minutes
// where errors spiked -- rather than for capacity planning, and the problem
// and slow columns are what make that usable.
func runPeaks(o *options) error {
	if o.bucket <= 0 {
		o.bucket = 10 * time.Minute
	}
	buckets := make(map[int64]*bucketStat)
	var undated int64

	err := o.eachRecord(func(r *pglogwatch.Record) error {
		if r.Time.IsZero() {
			// A record with no timestamp cannot be placed in time.
			// Counting it in some bucket would move activity to a
			// moment it did not happen; counting it separately says
			// so.
			undated++
			return nil
		}
		key := r.Time.UnixNano() / int64(o.bucket)
		b, ok := buckets[key]
		if !ok {
			b = &bucketStat{start: time.Unix(0, key*int64(o.bucket)).UTC()}
			buckets[key] = b
		}
		b.records++
		if r.Severity.IsProblem() {
			b.problems++
		}
		if r.Flags&pglogwatch.FlagHasDuration != 0 {
			b.slow++
		}
		return nil
	})
	if err != nil {
		return err
	}

	all := slices.Collect(maps.Values(buckets))
	// Busiest first, then by time so equal buckets read chronologically and
	// the output is stable between runs.
	slices.SortFunc(all, func(a, b *bucketStat) int {
		return cmp.Or(cmp.Compare(b.records, a.records), a.start.Compare(b.start))
	})
	if len(all) > o.top {
		all = all[:o.top]
	}

	if o.jsonOut {
		return peaksJSON(o, all, undated)
	}
	peaksText(o, all, undated)
	return nil
}

func peaksText(o *options, all []*bucketStat, undated int64) {
	t := newTable(o.stdout, "bucket start", "records", "problems", "slow")
	for _, b := range all {
		t.add(b.start.Format("2006-01-02 15:04:05"),
			itoa(b.records), itoa(b.problems), itoa(b.slow))
	}
	t.flush()
	if undated > 0 {
		t2 := newTable(o.stdout, "note", "count")
		o.stdout.Write([]byte("\n")) //nolint:errcheck // report output
		t2.add("records with no timestamp", itoa(undated))
		t2.flush()
	}
}

func peaksJSON(o *options, all []*bucketStat, undated int64) error {
	j := newJSONWriter(o.stdout)
	j.begin()
	j.strS("report", "peaks")
	j.strS("bucket", o.bucket.String())
	j.numAlways("buckets", int64(len(all)))
	j.numAlways("undated_records", undated)
	j.end()

	for _, b := range all {
		j.begin()
		j.strS("report", "peaks.bucket")
		j.time("start", b.start)
		j.numAlways("records", b.records)
		j.numAlways("problems", b.problems)
		j.numAlways("slow", b.slow)
		j.end()
	}
	return j.flush()
}
