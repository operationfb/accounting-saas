package banking

// statement_detect.go
// =============================================================================
// CSV format auto-detection (the heuristic engine).
//
// The fixed-template importer (statement_import.go) needs OUR exact columns; real
// bank exports vary in three ways:
//   - column NAMES        ("Transaction Date" vs "Date"; "Details" vs "Description")
//   - AMOUNT SHAPE        one signed "Amount" column, OR a money-in / money-out pair
//   - DATE FORMAT         DD/MM/YYYY vs ISO YYYY-MM-DD vs "02 Jun 2026" …
//
// This file is the PURE, dependency-free heuristic that proposes a ColumnMapping from a
// file's header + sample rows. It writes nothing and knows nothing about HTTP or the DB,
// so it unit-tests fast. The "detect → confirm → commit" flow uses it to pre-fill the
// mapping the user confirms; validateMapping is the gate the commit path runs.
// =============================================================================

import (
	"strings"
	"time"

	"github.com/operationfb/accounting-saas/internal/kernel"
)

// ColumnMapping says which 0-based column feeds each field, plus how amounts and dates
// are encoded. Columns are *int (nil = unassigned) so a proposed mapping can leave a
// field blank for the user to pick, and the confirm-screen dropdowns round-trip cleanly
// (a dropdown's value is "a column index, or nothing").
type ColumnMapping struct {
	DateColumn        *int   `json:"date_column"`
	DescriptionColumn *int   `json:"description_column"`
	AmountFormat      string `json:"amount_format"`             // "signed" | "split"
	AmountColumn      *int   `json:"amount_column,omitempty"`   // signed shape: one column, + in / - out
	MoneyInColumn     *int   `json:"money_in_column,omitempty"` // split shape
	MoneyOutColumn    *int   `json:"money_out_column,omitempty"`
	BalanceColumn     *int   `json:"balance_column,omitempty"` // optional → balance_minor
	MemoColumn        *int   `json:"memo_column,omitempty"`    // optional → bank_memo
	DateFormat        string `json:"date_format"`              // a Go layout from dateFormatAllowlist
}

// The two amount shapes a bank CSV can encode money in.
const (
	amountFormatSigned = "signed" // one column, sign carries direction (+ in, - out)
	amountFormatSplit  = "split"  // two columns, magnitude only (which column carries direction)
)

// dateLayoutOption pairs a human label (for the confirm-screen dropdown) with the Go
// reference layout we actually parse with. The list is ALSO the validation allowlist — a
// confirmed mapping's DateFormat must be one of these Layouts (we never eval an arbitrary
// user-supplied layout string).
type dateLayoutOption struct {
	Label  string `json:"label"`
	Layout string `json:"layout"`
}

// dateFormatAllowlist is ORDERED BY PREFERENCE: UK DD/MM first (this is a UK product), so
// when a date is genuinely ambiguous (every sample has day AND month ≤ 12, e.g. 05/06)
// the UK reading wins. The >12 disambiguation falls out for free: a sample like 25/06
// fails to parse as MM/DD (month 25), so only DD/MM survives; 06/25 fails as DD/MM, so
// MM/DD survives. US MM/DD is therefore last — only chosen when the data forces it.
var dateFormatAllowlist = []dateLayoutOption{
	{"DD/MM/YYYY", "02/01/2006"},
	{"YYYY-MM-DD", "2006-01-02"},
	{"DD-MM-YYYY", "02-01-2006"},
	{"DD.MM.YYYY", "02.01.2006"},
	{"DD MMM YYYY", "02 Jan 2006"},     // 02 Jun 2026
	{"DD MMMM YYYY", "02 January 2006"}, // 02 June 2026
	{"DD/MM/YY", "02/01/06"},
	{"MM/DD/YYYY", "01/02/2006"}, // US — last so the UK reading wins genuine ties
}

// isAllowedDateLayout reports whether layout is one we offer — used to reject a confirmed
// mapping that names a layout outside the allowlist (422).
func isAllowedDateLayout(layout string) bool {
	for _, o := range dateFormatAllowlist {
		if o.Layout == layout {
			return true
		}
	}
	return false
}

// fieldSynonyms maps each logical role to the normalised header strings that signal it.
// Matching runs on a NORMALISED header (see normaliseHeader), so "Paid In", "PAID_IN" and
// "paid-in (£)" all reduce to "paid in" before comparison.
var fieldSynonyms = map[string][]string{
	"date":        {"date", "transaction date", "posting date", "posted", "value date", "date posted"},
	"description": {"description", "details", "narrative", "reference", "payee", "particulars", "name", "transaction"},
	"amount":      {"amount", "value", "transaction amount"},
	"money_in":    {"money in", "paid in", "credit", "credits", "deposit", "deposits", "receipts", "in"},
	"money_out":   {"money out", "paid out", "debit", "debits", "withdrawal", "withdrawals", "payment", "payments", "out"},
	"balance":     {"balance", "running balance", "balance gbp"},
	"memo":        {"bank memo", "memo", "notes", "note"},
}

// bomPrefix is the UTF-8 byte-order mark some exporters prepend to the file — stripped
// from the first header cell so column 0 matches its synonyms.
const bomPrefix = string(rune(0xFEFF))

// normaliseHeader lower-cases, strips a leading BOM, turns any run of non-alphanumeric
// characters into a single space, and trims — so header punctuation/spacing/casing don't
// affect synonym matching ("Paid-In (£)" → "paid in", "Money_Out" → "money out").
func normaliseHeader(h string) string {
	h = strings.ToLower(strings.TrimPrefix(h, bomPrefix))
	var b strings.Builder
	lastSpace := false
	for _, r := range h {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			b.WriteRune(r)
			lastSpace = false
		} else if !lastSpace {
			b.WriteByte(' ')
			lastSpace = true
		}
	}
	return strings.TrimSpace(b.String())
}

// scoreHeader rates how well a normalised header matches a role: 2 = exact synonym match,
// 1 = the header CONTAINS a synonym as a whole word ("transaction amount gbp" ~ "amount"),
// 0 = no match. Exact beats partial so a column literally named "amount" wins the amount
// role over "amount in account". Whole-word containment (space-padded) stops "in" from
// matching inside "main".
func scoreHeader(norm, role string) int {
	best := 0
	padded := " " + norm + " "
	for _, syn := range fieldSynonyms[role] {
		if norm == syn {
			return 2
		}
		if strings.Contains(padded, " "+syn+" ") && best < 1 {
			best = 1
		}
	}
	return best
}

// detectFormat inspects the parsed CSV records (row 0 = header) and proposes a ColumnMapping
// plus human-readable warnings. It NEVER errors on a missing column — it leaves that field
// nil and adds a warning, so the preview can still show the user something to correct.
// Turning a still-incomplete mapping into a 422 is validateMapping's job (the commit path).
func detectFormat(records [][]string) (ColumnMapping, []string) {
	var warnings []string
	if len(records) == 0 {
		return ColumnMapping{DateFormat: dateFormatAllowlist[0].Layout}, []string{"the file has no rows"}
	}
	header := records[0]
	norms := make([]string, len(header))
	for i, h := range header {
		norms[i] = normaliseHeader(h)
	}

	// assignRole claims the highest-scoring not-yet-used column for a role (ties → leftmost),
	// marking it used so two roles can't grab the same column.
	used := make([]bool, len(header))
	assignRole := func(role string) *int {
		bestIdx, bestScore := -1, 0
		for i, norm := range norms {
			if used[i] {
				continue
			}
			if sc := scoreHeader(norm, role); sc > bestScore {
				bestIdx, bestScore = i, sc
			}
		}
		if bestIdx < 0 {
			return nil
		}
		used[bestIdx] = true
		idx := bestIdx
		return &idx
	}

	m := ColumnMapping{}
	// Claim the structured roles BEFORE the loose "description" role, so a generic text
	// column can't steal a column a money/date role wants.
	m.DateColumn = assignRole("date")
	moneyIn := assignRole("money_in")
	moneyOut := assignRole("money_out")
	amount := assignRole("amount")
	m.BalanceColumn = assignRole("balance")
	m.MemoColumn = assignRole("memo")
	m.DescriptionColumn = assignRole("description")

	// Amount shape: a money-in/out PAIR is the most specific signal and wins; otherwise a
	// lone signed "Amount" column; otherwise a half-detected pair (user fills the gap).
	switch {
	case moneyIn != nil && moneyOut != nil:
		m.AmountFormat = amountFormatSplit
		m.MoneyInColumn, m.MoneyOutColumn = moneyIn, moneyOut
	case amount != nil:
		m.AmountFormat = amountFormatSigned
		m.AmountColumn = amount
	case moneyIn != nil || moneyOut != nil:
		m.AmountFormat = amountFormatSplit
		m.MoneyInColumn, m.MoneyOutColumn = moneyIn, moneyOut
		warnings = append(warnings, "only one of money-in / money-out was detected — pick the other column (or switch to a single signed amount column)")
	default:
		warnings = append(warnings, "couldn't find an amount column — pick the column that holds the transaction amount")
	}

	// Fallback: if nothing matched the description role but there IS a memo/notes column, use
	// it as the description (which is required) — a statement whose only free-text column is
	// "Memo" should still import. The column then feeds the description, not bank_memo (using
	// it for both would just duplicate the text onto every row).
	if m.DescriptionColumn == nil && m.MemoColumn != nil {
		m.DescriptionColumn = m.MemoColumn
		m.MemoColumn = nil
	}

	if m.DateColumn == nil {
		warnings = append(warnings, "couldn't find a date column — pick the column that holds the transaction date")
	}
	if m.DescriptionColumn == nil {
		warnings = append(warnings, "couldn't find a description column — pick the column with the transaction description")
	}

	// Date format: sniff the date column's values; default to UK DD/MM/YYYY if unsure.
	m.DateFormat = dateFormatAllowlist[0].Layout
	if m.DateColumn != nil {
		if layout, ok := sniffDateFormat(records[1:], *m.DateColumn); ok {
			m.DateFormat = layout
		} else {
			warnings = append(warnings, "couldn't confidently detect the date format — defaulting to DD/MM/YYYY; change it below if the dates look wrong")
		}
	}
	return m, warnings
}

// sniffDateFormat returns the first allowlist layout that parses EVERY non-empty sample in
// the date column (allowlist order = UK-first preference, so ambiguous dates read as UK).
// ok=false when no single layout parses all samples — the caller then defaults + warns.
// Samples are capped for speed; a statement's dates are uniform so a few suffice.
func sniffDateFormat(rows [][]string, dateCol int) (string, bool) {
	const maxSamples = 50
	var samples []string
	for _, rec := range rows {
		if dateCol < 0 || dateCol >= len(rec) {
			continue
		}
		if v := strings.TrimSpace(rec[dateCol]); v != "" {
			samples = append(samples, v)
			if len(samples) >= maxSamples {
				break
			}
		}
	}
	if len(samples) == 0 {
		return "", false
	}
	for _, opt := range dateFormatAllowlist {
		allParse := true
		for _, v := range samples {
			if _, err := time.Parse(opt.Layout, v); err != nil {
				allParse = false
				break
			}
		}
		if allParse {
			return opt.Layout, true
		}
	}
	return "", false
}

// validateMapping rejects (422) a mapping that can't drive an import: a required column
// missing or out of range, an unknown amount format, or a date layout we don't offer. It
// guards BOTH the confirm-with-mapping commit path and the no-mapping path (after
// detectFormat) — so an undetectable file fails with the same clear 422 the old hard-coded
// "missing required column" check produced.
func validateMapping(m ColumnMapping, numCols int) error {
	inRange := func(p *int) bool { return p != nil && *p >= 0 && *p < numCols }
	if !inRange(m.DateColumn) {
		return kernel.ErrValidation("no date column is selected", nil)
	}
	if !inRange(m.DescriptionColumn) {
		return kernel.ErrValidation("no description column is selected", nil)
	}
	switch m.AmountFormat {
	case amountFormatSigned:
		if !inRange(m.AmountColumn) {
			return kernel.ErrValidation("no amount column is selected", nil)
		}
	case amountFormatSplit:
		if !inRange(m.MoneyInColumn) || !inRange(m.MoneyOutColumn) {
			return kernel.ErrValidation("both a money-in and a money-out column must be selected", nil)
		}
	default:
		return kernel.ErrValidation(`amount_format must be "signed" or "split"`, nil)
	}
	// Optional columns, when provided, must still be in range.
	for _, p := range []*int{m.BalanceColumn, m.MemoColumn} {
		if p != nil && (*p < 0 || *p >= numCols) {
			return kernel.ErrValidation("a selected column is out of range", nil)
		}
	}
	if !isAllowedDateLayout(m.DateFormat) {
		return kernel.ErrValidation("the selected date format is not supported", nil)
	}
	return nil
}
