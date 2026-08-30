package pglogwatch

// localeSeverity maps one localised severity spelling to its English meaning.
type localeSeverity struct {
	text     string
	severity Severity
}

// severityLocales holds the localised severity spellings for the lc_messages
// values pgwatch supports, keyed by the two-character prefix of lc_messages
// that pgwatch's tryDetermineLogSettings query already extracts.
//
// The entries are ported from pgwatch's pgSeveritiesLocale table so that
// migrating pgwatch onto this module cannot change which records it counts
// (CON-007). Where pgwatch's table is demonstrably wrong the correct spelling
// is added alongside rather than replacing it, so behaviour is a superset:
//
//   - fr lists PANIK, which is German. PANIQUE is added.
//   - several tables carry a bare DEBUG, which PostgreSQL never emits on its
//     own; ParseSeverity maps it to DEBUG1 so those entries still resolve.
//
// PostgreSQL writes severities in lc_messages, so a localised log contains
// only localised spellings. Each table is therefore self-contained: a spelling
// absent from it yields SeverityUnknown, which is what E13 requires when the
// configured language does not match the log.
var severityLocales = map[string][]localeSeverity{
	"C.": {
		{"DEBUG", SeverityDebug1}, {"LOG", SeverityLog}, {"INFO", SeverityInfo},
		{"NOTICE", SeverityNotice}, {"WARNING", SeverityWarning}, {"ERROR", SeverityError},
		{"FATAL", SeverityFatal}, {"PANIC", SeverityPanic},
	},
	"de": {
		{"DEBUG", SeverityDebug1}, {"LOG", SeverityLog}, {"INFO", SeverityInfo},
		{"HINWEIS", SeverityNotice}, {"WARNUNG", SeverityWarning}, {"FEHLER", SeverityError},
		{"FATAL", SeverityFatal}, {"PANIK", SeverityPanic},
	},
	"fr": {
		{"DEBUG", SeverityDebug1}, {"LOG", SeverityLog}, {"INFO", SeverityInfo},
		{"NOTICE", SeverityNotice}, {"ATTENTION", SeverityWarning}, {"ERREUR", SeverityError},
		{"FATAL", SeverityFatal}, {"PANIK", SeverityPanic}, {"PANIQUE", SeverityPanic},
	},
	"it": {
		{"DEBUG", SeverityDebug1}, {"LOG", SeverityLog}, {"INFO", SeverityInfo},
		{"NOTIFICA", SeverityNotice}, {"ATTENZIONE", SeverityWarning}, {"ERRORE", SeverityError},
		{"FATALE", SeverityFatal}, {"PANICO", SeverityPanic},
	},
	"ko": {
		{"디버그", SeverityDebug1}, {"로그", SeverityLog}, {"정보", SeverityInfo},
		{"알림", SeverityNotice}, {"경고", SeverityWarning}, {"오류", SeverityError},
		{"치명적오류", SeverityFatal}, {"손상", SeverityPanic},
	},
	"pl": {
		{"DEBUG", SeverityDebug1}, {"DZIENNIK", SeverityLog}, {"INFORMACJA", SeverityInfo},
		{"UWAGA", SeverityNotice}, {"OSTRZEŻENIE", SeverityWarning}, {"BŁĄD", SeverityError},
		{"KATASTROFALNY", SeverityFatal}, {"PANIKA", SeverityPanic},
	},
	"ru": {
		{"ОТЛАДКА", SeverityDebug1}, {"СООБЩЕНИЕ", SeverityLog}, {"ИНФОРМАЦИЯ", SeverityInfo},
		{"ЗАМЕЧАНИЕ", SeverityNotice}, {"ПРЕДУПРЕЖДЕНИЕ", SeverityWarning}, {"ОШИБКА", SeverityError},
		{"ВАЖНО", SeverityFatal}, {"ПАНИКА", SeverityPanic},
	},
	"sv": {
		{"DEBUG", SeverityDebug1}, {"LOGG", SeverityLog}, {"INFO", SeverityInfo},
		{"NOTIS", SeverityNotice}, {"VARNING", SeverityWarning}, {"FEL", SeverityError},
		{"FATALT", SeverityFatal}, {"PANIK", SeverityPanic},
	},
	"tr": {
		{"DEBUG", SeverityDebug1}, {"LOG", SeverityLog}, {"BİLGİ", SeverityInfo},
		{"NOT", SeverityNotice}, {"UYARI", SeverityWarning}, {"HATA", SeverityError},
		{"ÖLÜMCÜL (FATAL)", SeverityFatal}, {"KRİTİK", SeverityPanic},
	},
	"zh": {
		{"调试", SeverityDebug1}, {"日志", SeverityLog}, {"信息", SeverityInfo},
		{"注意", SeverityNotice}, {"警告", SeverityWarning}, {"错误", SeverityError},
		{"致命错误", SeverityFatal}, {"比致命错误还过分的错误", SeverityPanic},
	},
}

// severityResolver resolves a raw severity to a [Severity]. It is chosen once
// per parser from Config.MessagesLang and then called per record, so the map
// lookup above happens at configuration time and never on the hot path.
type severityResolver struct {
	// table is nil for English and for any language this package does not
	// know, in which case resolution falls through to ParseSeverity.
	table []localeSeverity
}

// newSeverityResolver picks the table for lang. An unrecognised language is
// not an error: FMT-007 requires falling back to pass-through, so callers get
// English resolution and nothing is reported.
func newSeverityResolver(lang string) severityResolver {
	if lang == "" || lang == "en" {
		return severityResolver{}
	}
	return severityResolver{table: severityLocales[lang]}
}

// resolve maps raw severity bytes to a Severity without allocating.
//
// string(raw) == s is compiled to a length check plus memequal with no copy of
// raw, so the comparison is free. The table has at most nine entries and a
// mismatched length is rejected before any byte is touched, which is cheap
// enough not to warrant generating a switch per language -- and it keeps the
// tables auditable as data against PostgreSQL's own .po files.
func (sr severityResolver) resolve(raw []byte) Severity {
	if sr.table == nil {
		return ParseSeverity(raw)
	}
	for i := range sr.table {
		if string(raw) == sr.table[i].text {
			return sr.table[i].severity
		}
	}
	return SeverityUnknown
}
