package bambu

import "fmt"

// Health Management System entries. The printer latches faults into
// print.hms[] as an {attr, code} pair of 32-bit words and leaves them
// there until they clear, so the array is the machine's current complaint
// list rather than an event log.
//
// Only the identifier and severity are derived here. Resolving a code to
// its wording is deliberately not attempted: Bambu's catalogue lives at
// https://e.bambulab.com/query.php (4884 entries, keyed by the code with
// the underscores stripped) and is a poor fit for this exporter. As of
// August 2026 it serves only Polish — every other lang value returns an
// empty set — 61 entries carry no description at all, and the attr half
// varies by which instance of a module reported, so an exact-match lookup
// misses more often than it hits. This printer's live entry
// (0500_0600_0002_0070) is absent from the catalogue while the otherwise
// identical 0500_0300_0002_0070 is present with an empty description.
// Emitting the code and letting a human search it is the honest option.

// HMSCode renders an entry as the printer's own four-group identifier,
// e.g. "0500_0600_0002_0070" — the form its display shows and the form
// Bambu's catalogue keys on once the underscores are removed.
//
// attr identifies the module and which instance of it reported; code
// identifies the fault, with the severity in its high group.
func HMSCode(attr, code *Num) string {
	if attr == nil || code == nil {
		return ""
	}
	a, c := uint32(attr.Int()), uint32(code.Int())
	return fmt.Sprintf("%04X_%04X_%04X_%04X", a>>16, a&0xffff, c>>16, c&0xffff)
}

// HMSSeverity maps the high group of code to the printer's own wording.
// Bambu's catalogue uses only 1-3 (of 4884 entries: 1518 fatal, 3094
// serious, 271 common), so anything else is reported as unknown rather
// than guessed at — the alert rules match on these strings.
func HMSSeverity(code *Num) string {
	if code == nil {
		return ""
	}
	switch uint32(code.Int()) >> 16 {
	case 1:
		return "fatal"
	case 2:
		return "serious"
	case 3:
		return "common"
	}
	return "unknown"
}
