package pglogwatch

// TemplateRulesVersion changes whenever a rule change can change a template.
//
// It is part of the input to [PatternID], so an upgrade that splits or merges
// templates gives new pattern ids instead of putting two definitions under
// one id. A consumer that stores ids can compare this value to tell old ids
// from new ones.
const TemplateRulesVersion = 1

// AppendTemplate appends the masked template of r.Message to dst and returns
// the extended slice.
//
// The template is the message with every value (numbers, quoted literals,
// key=value values, ids, paths) replaced by $1, $2, and so on, while words
// and identifiers stay. Two records that differ only by values give the same
// template, which is what lets a consumer group and count log events.
//
// r.Message is borrowed from the parser and becomes invalid on the next call
// to [Parser.Next]. The returned template is written into dst and never
// aliases r.Message, so it stays valid after that call. AppendTemplate
// performs no heap allocation when dst has enough capacity; a caller that
// reuses one buffer, for example dst[:0], pays for the allocation once.
//
// Only r.Message is read for the template; r.SQLState decides how
// double-quoted tokens are treated. Other fields are ignored.
func AppendTemplate(dst []byte, r *Record) []byte {
	// The masking rules come in later; until then the template is the
	// message itself.
	return append(dst, r.Message...)
}

// PatternID returns the pattern identity of a template made by
// [AppendTemplate] for r.
//
// The id covers r.Severity, r.SQLState, [TemplateRulesVersion] and the
// template, so the same message text under a different severity or SQLSTATE
// is a different pattern. It is a fixed-size array so that it can be a map
// key and costs no allocation. template may be the borrowed result of
// AppendTemplate; PatternID does not retain it.
func PatternID(r *Record, template []byte) [8]byte {
	// The hash comes in later; until then every pattern has the zero id.
	_, _ = r, template
	return [8]byte{}
}
