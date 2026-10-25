# Template privacy corpus

`privacy.txt` holds the messages of `TestTemplatePrivacy` (AC-002 of
`spec/message-template.md`). Each one carries at least one marker that no
template may keep:

- `'SECRET-1'` or `SECRET-1` in a literal, a path or a key=value value;
- the number `424242`;
- `user=SECRET-2`;
- `"SECRET-3"` in SQLSTATE classes 22 and 23, and after a `for type` phrase;
  in class 23 also right after a word other than `constraint`, `index` or
  `table`;
- an e-mail address with the local part `secret4` in a statement, written
  `{email}` (see below).

A marker sits only where a rule masks it. Places where a value stays by
design are left out: a double-quoted identifier outside classes 22 and 23
(REQ-005), the name right after `constraint`, `index` or `table` in those
classes, unless REQ-014 masks it, and the role in
`password authentication failed for user "x"`.

## Format

One record per line:

```
SEVERITY SQLSTATE message
```

The three fields are separated by one space. `SEVERITY` is an English
severity name such as `LOG` or `ERROR`. `SQLSTATE` is a five-character code,
or `-` for a record without one. The message is the rest of the line.

In the message, `\n` is a line break, `\t` a tab and `\\` a backslash; any
other backslash sequence is an error. `{email}` stands for the marker
e-mail address, which the test fills in. `TestFixturesCarryNoRealIdentifiers`
rejects any e-mail address written into a fixture, so the address lives in
`template_test.go`. Blank lines and lines that start with `#` are skipped.
