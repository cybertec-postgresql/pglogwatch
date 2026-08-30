# Test fixtures

Small, hand-written inputs for edge cases that a generated corpus cannot
reliably produce (TST-004, DAT-002). Budget: 1 MB total for everything here
except `csv/huge-statement.csv`, which exists to exercise buffer growth.

These files are **byte-exact**. `.gitattributes` disables end-of-line
normalisation for the whole directory, because several fixtures carry CRLF,
invalid UTF-8 or a byte order mark deliberately. Do not reformat them, do not
open them in an editor that trims trailing whitespace, and do not let a linter
near them.

Real-world samples may be added, but must be scrubbed of usernames, hostnames,
IP addresses and query parameters first (TST-005, COM-002).
