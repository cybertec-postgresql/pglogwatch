package pglogwatch

import (
	"bytes"
	"strings"
	"testing"
)

// Message template benchmarks (spec/message-template.md, section 6).
//
// The corpus is parsed once into owned records before the timer starts, so
// the loop measures AppendTemplate and PatternID alone, not the parser. Each
// benchmark reports "ns/record" next to ns/op, because one op covers the
// whole corpus and its size changes whenever a fixture is added.

// templateCorpus lists the fixtures the template benchmarks read. stderr
// fixtures use the line prefix they were written with.
var templateCorpus = []struct {
	file   string
	format Format
	prefix string
}{
	{file: "csv/pg12-basic.csv", format: FormatCSV},
	{file: "csv/pg13-basic.csv", format: FormatCSV},
	{file: "csv/pg14-basic.csv", format: FormatCSV},
	{file: "csv/quotes-newlines-commas.csv", format: FormatCSV},
	{file: "csv/crlf.csv", format: FormatCSV},
	{file: "csv/crlf-multiline.csv", format: FormatCSV},
	{file: "json/basic.json", format: FormatJSON},
	{file: "json/escapes.json", format: FormatJSON},
	{file: "stderr/basic.log", format: FormatStderr},
	{file: "stderr/multiline.log", format: FormatStderr, prefix: multilinePrefix},
}

// templateRecords parses every corpus fixture and returns owned copies of
// its records.
func templateRecords(b *testing.B) []*OwnedRecord {
	b.Helper()
	var recs []*OwnedRecord
	for _, c := range templateCorpus {
		in, err := readFixture(c.file)
		if err != nil {
			b.Fatal(err)
		}
		p := New(bytes.NewReader(in), Config{Format: c.format, LinePrefix: c.prefix})
		for p.Next() {
			recs = append(recs, p.Record().Clone())
		}
		if err := p.Err(); err != nil {
			b.Fatalf("%s: %v", c.file, err)
		}
	}
	if len(recs) == 0 {
		b.Fatal("template corpus is empty")
	}
	return recs
}

// statementRecord is a duration line with a long SELECT that carries string
// and numeric literals and an IN list, the shape log_min_duration_statement
// produces most.
func statementRecord() *Record {
	var sb strings.Builder
	sb.WriteString("duration: 1234.567 ms  statement: SELECT o.id, o.total, c.name FROM orders o ")
	sb.WriteString("JOIN customers c ON c.id = o.customer_id WHERE o.created_at > '2026-08-30 10:11:12' ")
	sb.WriteString("AND c.email = 'someone@example.com' AND o.total > 99.95 AND o.status IN (")
	for i := range 64 {
		if i > 0 {
			sb.WriteString(", ")
		}
		sb.WriteString("'status_")
		sb.WriteByte(byte('a' + i%26))
		sb.WriteString("'")
	}
	sb.WriteString(") AND o.id IN (")
	for i := range 64 {
		if i > 0 {
			sb.WriteString(", ")
		}
		sb.WriteString("100")
		sb.WriteByte(byte('0' + i%10))
	}
	sb.WriteString(") ORDER BY o.created_at DESC LIMIT 50 OFFSET 100")
	return &Record{Severity: SeverityLog, Message: []byte(sb.String())}
}

// hugeRecord is a 1 MiB statement. AppendTemplate stops reading once the
// template passes 1,024 bytes, so its cost is close to that of a short
// statement, not proportional to the message.
func hugeRecord() *Record {
	const size = 1 << 20
	msg := make([]byte, 0, size)
	msg = append(msg, "statement: SELECT "...)
	for len(msg) < size-len("1 FROM t") {
		msg = append(msg, "column_name, 'literal', 42, "...)
	}
	msg = append(msg[:size-len("1 FROM t")], "1 FROM t"...)
	return &Record{Severity: SeverityLog, Message: msg}
}

// templateDst is larger than any template, so the loop never grows it.
const templateDst = 2048

// BenchmarkAppendTemplate templates every record of the fixture corpus per
// op, plus two synthetic messages that bound the cost from above.
func BenchmarkAppendTemplate(b *testing.B) {
	b.Run("corpus", func(b *testing.B) {
		recs := templateRecords(b)
		dst := make([]byte, 0, templateDst)
		b.ReportAllocs()
		for b.Loop() {
			for _, o := range recs {
				dst = AppendTemplate(dst[:0], &o.Record)
			}
		}
		sink(int64(len(dst)))
		reportPerRecord(b, len(recs))
	})
	b.Run("statement", func(b *testing.B) {
		benchOneTemplate(b, statementRecord())
	})
	b.Run("1MiB", func(b *testing.B) {
		benchOneTemplate(b, hugeRecord())
	})
}

// BenchmarkPatternID hashes the corpus templates, which are made once
// before the timer starts.
func BenchmarkPatternID(b *testing.B) {
	recs := templateRecords(b)
	tmpls := make([][]byte, len(recs))
	for i, o := range recs {
		tmpls[i] = AppendTemplate(nil, &o.Record)
	}
	var id [8]byte
	b.ReportAllocs()
	for b.Loop() {
		for i, o := range recs {
			id = PatternID(&o.Record, tmpls[i])
		}
	}
	sink(int64(id[0]))
	reportPerRecord(b, len(recs))
}

// BenchmarkTemplateAndID is the per-record cost a caller that groups records
// by pattern pays: one AppendTemplate and one PatternID.
func BenchmarkTemplateAndID(b *testing.B) {
	recs := templateRecords(b)
	dst := make([]byte, 0, templateDst)
	var id [8]byte
	b.ReportAllocs()
	for b.Loop() {
		for _, o := range recs {
			dst = AppendTemplate(dst[:0], &o.Record)
			id = PatternID(&o.Record, dst)
		}
	}
	sink(int64(id[0]))
	reportPerRecord(b, len(recs))
}

func benchOneTemplate(b *testing.B, r *Record) {
	dst := make([]byte, 0, templateDst)
	b.ReportAllocs()
	for b.Loop() {
		dst = AppendTemplate(dst[:0], r)
	}
	sink(int64(len(dst)))
	reportPerRecord(b, 1)
}

// reportPerRecord adds a ns/record metric for an op that covers n records.
func reportPerRecord(b *testing.B, n int) {
	b.Helper()
	if b.N > 0 {
		b.ReportMetric(float64(b.Elapsed().Nanoseconds())/float64(b.N*n), "ns/record")
	}
}
