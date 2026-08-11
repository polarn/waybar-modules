package bambu

import "fmt"

// Health Management System entries. The printer reports print.hms[] as a
// list of {attr, code} 32-bit word pairs, each with ts_boot and ts_unix
// event timestamps.
//
// Whether that list means "wrong right now" or "seen since boot" is NOT
// established — the timestamps suggest event records, and an entry has been
// observed persisting for days while the printer worked normally. Do not
// build anything that treats presence as a live fault without settling this
// first. The cheap experiment: note an entry, power-cycle the printer, and
// see whether it comes back.
//
// Only the identifier and severity are derived here. Resolving a code to
// its wording is deliberately not attempted, and the reasons are worth
// keeping because they look like something worth fixing until you check.
// Bambu's catalogue is at https://e.bambulab.com/query.php (4884 entries,
// keyed by the code with underscores stripped). As of August 2026 it serves
// only Polish — every other lang value returns an empty set — there is no
// working per-code query, since ?e=<code> returns an empty result for real
// and invented codes alike, and only the full dump works.
//
// More to the point, the catalogue does not cover what this printer emits.
// Its live entry 0500_0600_0002_0070 is absent under all 24 orderings of
// the four groups, and the near-identical 0500_0300_0002_0070 is present
// with an empty description. That is systematic, not an accident: 53 of the
// 62 undescribed entries in the whole catalogue are in the 0500
// camera/media family, 13 of those in the 0500_0600 sub-device. Bambu does
// not publish wording for that family, which is exactly why such an entry
// shows up nowhere in Handy or on the printer's own LCD.
//
// So: emit the code, let a human search it, and do not pretend a lookup
// table would help.

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
