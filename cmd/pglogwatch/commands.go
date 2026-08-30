package main

// commands is the subcommand table. Entries are filled in by the files that
// implement them, so adding a subcommand touches one file.
var commands = map[string]command{}

// grepOptions holds grep's own flags; declared here so options can carry them
// without commands.go depending on the order files are compiled in.
type grepOptions struct {
	pattern     string
	before      int
	after       int
	ignoreCase  bool
	invertMatch bool
	fieldsOnly  bool
}
