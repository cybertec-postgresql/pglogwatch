//go:build !purego && !race

package allocs

// Meaningful reports whether allocation counting says anything about the
// production build.
//
// It is false under two build configurations:
//
//   - purego, where safe.go's copying conversions allocate by design
//     (PKG-007), so a zero result is impossible rather than merely unlikely;
//   - race, whose instrumentation allocates inside the runtime, which would
//     make every gate fail for reasons unrelated to the parser.
//
// The gates skip in those builds instead of being relaxed for everyone, so the
// production configuration is still held to zero.
const Meaningful = true
