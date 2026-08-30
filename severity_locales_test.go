package pglogwatch

import (
	"testing"

	"github.com/cybertec-postgresql/pglogwatch/internal/allocs"
	"github.com/stretchr/testify/assert"
)

func TestSeverityLocales(t *testing.T) {
	cases := []struct {
		lang string
		raw  string
		want Severity
	}{
		// AC-006 and E12 name the Russian spellings specifically.
		{"ru", "ОШИБКА", SeverityError},
		{"ru", "ПРЕДУПРЕЖДЕНИЕ", SeverityWarning},
		{"ru", "ПАНИКА", SeverityPanic},
		{"ru", "СООБЩЕНИЕ", SeverityLog},

		{"de", "FEHLER", SeverityError},
		{"de", "WARNUNG", SeverityWarning},
		{"de", "HINWEIS", SeverityNotice},
		{"fr", "ERREUR", SeverityError},
		{"fr", "ATTENTION", SeverityWarning},
		{"it", "ERRORE", SeverityError},
		{"pl", "BŁĄD", SeverityError},
		{"sv", "FEL", SeverityError},
		{"tr", "HATA", SeverityError},
		{"tr", "ÖLÜMCÜL (FATAL)", SeverityFatal},
		{"ko", "오류", SeverityError},
		{"zh", "错误", SeverityError},
		{"zh", "比致命错误还过分的错误", SeverityPanic},
		{"C.", "ERROR", SeverityError},

		// E13: the right spelling under the wrong language is unknown,
		// and the record still gets emitted (checked in the parser
		// tests). Nothing here may resolve by accident.
		{"de", "ОШИБКА", SeverityUnknown},
		{"ru", "FEHLER", SeverityUnknown},
		{"ru", "ERROR", SeverityUnknown},
	}
	for _, c := range cases {
		r := newSeverityResolver(c.lang)
		assert.Equal(t, c.want, r.resolve([]byte(c.raw)), "lang %s, raw %q", c.lang, c.raw)
	}
}

func TestSeverityLocaleFallback(t *testing.T) {
	// FMT-007: an unknown language passes through to English resolution
	// rather than failing or resolving to nothing.
	for _, lang := range []string{"", "en", "xx", "klingon"} {
		r := newSeverityResolver(lang)
		assert.Equal(t, SeverityError, r.resolve([]byte("ERROR")), "lang %q", lang)
	}
}

func TestSeverityLocaleTablesAreComplete(t *testing.T) {
	// Every table must cover the eight severities pgwatch's
	// server_log_event_counts measurement reports, or migrating a source in
	// that locale would silently start producing zeros (CON-007).
	wanted := []Severity{
		SeverityDebug1, SeverityLog, SeverityInfo, SeverityNotice,
		SeverityWarning, SeverityError, SeverityFatal, SeverityPanic,
	}
	for lang, table := range severityLocales {
		present := make(map[Severity]bool, len(table))
		for _, e := range table {
			present[e.severity] = true
		}
		for _, s := range wanted {
			assert.True(t, present[s], "locale %q has no spelling for %s", lang, s)
		}
	}
}

func TestSeverityLocaleFrenchPanicSuperset(t *testing.T) {
	// pgwatch's fr table maps the German PANIK. Keeping it while adding the
	// French PANIQUE is what makes the port a strict superset.
	r := newSeverityResolver("fr")
	assert.Equal(t, SeverityPanic, r.resolve([]byte("PANIK")))
	assert.Equal(t, SeverityPanic, r.resolve([]byte("PANIQUE")))
}

// TestAllocSeverityLocale is the PERF-005 gate for the localised path: the map
// lookup happens in newSeverityResolver, never per record.
func TestAllocSeverityLocale(t *testing.T) {
	r := newSeverityResolver("ru")
	raw := []byte("ОШИБКА")
	allocs.Zero(t, 100, func() {
		_ = r.resolve(raw)
	})
}
