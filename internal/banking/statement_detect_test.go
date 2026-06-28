package banking

// statement_detect_test.go
// =============================================================================
// PURE unit tests for the CSV format detector — no Postgres, no GCS, no HTTP, so they
// run in milliseconds under `go test ./internal/banking/...`. They pin the three things
// the heuristic decides: which column feeds each field (synonyms), the amount SHAPE
// (signed vs split), and the date FORMAT (incl. the UK-vs-US disambiguation).
// =============================================================================

import (
	"testing"

	"github.com/operationfb/accounting-saas/internal/kernel"
)

func ptr(i int) *int { return &i }

// eqPtr compares an *int column against an expected value (or nil = "must be unassigned").
func eqPtr(got, want *int) bool {
	if got == nil || want == nil {
		return got == nil && want == nil
	}
	return *got == *want
}

func TestDetectFormat(t *testing.T) {
	tests := []struct {
		name       string
		records    [][]string
		wantDate   *int
		wantDesc   *int
		wantFormat string // "signed" | "split" | "" (none)
		wantAmount *int   // signed shape
		wantIn     *int   // split shape
		wantOut    *int   // split shape
		wantBal    *int
		wantMemo   *int
		wantLayout string
	}{
		{
			name: "our own template — signed amount, DD/MM",
			records: [][]string{
				{"date", "description", "amount"},
				{"11/06/2026", "Salary", "16413.59"},
				{"12/06/2026", "Coffee", "-10.08"},
			},
			wantDate: ptr(0), wantDesc: ptr(1), wantFormat: amountFormatSigned, wantAmount: ptr(2),
			wantLayout: "02/01/2006",
		},
		{
			name: "Monzo-style — signed amount, ISO date, 'Name' description",
			records: [][]string{
				{"Date", "Name", "Amount"},
				{"2026-06-22", "Tesco", "-12.50"},
				{"2026-06-23", "Acme Ltd", "2500.00"},
			},
			wantDate: ptr(0), wantDesc: ptr(1), wantFormat: amountFormatSigned, wantAmount: ptr(2),
			wantLayout: "2006-01-02",
		},
		{
			name: "Barclays-style — Debit/Credit split + Balance",
			records: [][]string{
				{"Date", "Description", "Debit", "Credit", "Balance"},
				{"22/06/2026", "Tesco", "12.50", "", "987.50"},
				{"23/06/2026", "Acme", "", "2500.00", "3487.50"},
			},
			wantDate: ptr(0), wantDesc: ptr(1), wantFormat: amountFormatSplit,
			wantOut: ptr(2), wantIn: ptr(3), wantBal: ptr(4), wantLayout: "02/01/2006",
		},
		{
			name: "Lloyds-style — 'Transaction Date' / 'Details' / 'Paid out' / 'Paid in'",
			records: [][]string{
				{"Transaction Date", "Details", "Paid out", "Paid in"},
				{"22/06/2026", "ACME LTD", "", "2500.00"},
			},
			wantDate: ptr(0), wantDesc: ptr(1), wantFormat: amountFormatSplit,
			wantOut: ptr(2), wantIn: ptr(3), wantLayout: "02/01/2006",
		},
		{
			name: "memo column detected (bank_memo)",
			records: [][]string{
				{"date", "description", "amount", "bank_memo"},
				{"11/06/2026", "Salary", "100.00", "FPS CREDIT"},
			},
			wantDate: ptr(0), wantDesc: ptr(1), wantFormat: amountFormatSigned, wantAmount: ptr(2),
			wantMemo: ptr(3), wantLayout: "02/01/2006",
		},
		{
			name: "no amount column — shape left empty for the user to fix",
			records: [][]string{
				{"date", "description"},
				{"11/06/2026", "x"},
			},
			wantDate: ptr(0), wantDesc: ptr(1), wantFormat: "", wantLayout: "02/01/2006",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m, _ := detectFormat(tt.records)
			if !eqPtr(m.DateColumn, tt.wantDate) {
				t.Errorf("DateColumn = %v, want %v", m.DateColumn, tt.wantDate)
			}
			if !eqPtr(m.DescriptionColumn, tt.wantDesc) {
				t.Errorf("DescriptionColumn = %v, want %v", m.DescriptionColumn, tt.wantDesc)
			}
			if m.AmountFormat != tt.wantFormat {
				t.Errorf("AmountFormat = %q, want %q", m.AmountFormat, tt.wantFormat)
			}
			if !eqPtr(m.AmountColumn, tt.wantAmount) {
				t.Errorf("AmountColumn = %v, want %v", m.AmountColumn, tt.wantAmount)
			}
			if !eqPtr(m.MoneyInColumn, tt.wantIn) {
				t.Errorf("MoneyInColumn = %v, want %v", m.MoneyInColumn, tt.wantIn)
			}
			if !eqPtr(m.MoneyOutColumn, tt.wantOut) {
				t.Errorf("MoneyOutColumn = %v, want %v", m.MoneyOutColumn, tt.wantOut)
			}
			if !eqPtr(m.BalanceColumn, tt.wantBal) {
				t.Errorf("BalanceColumn = %v, want %v", m.BalanceColumn, tt.wantBal)
			}
			if !eqPtr(m.MemoColumn, tt.wantMemo) {
				t.Errorf("MemoColumn = %v, want %v", m.MemoColumn, tt.wantMemo)
			}
			if m.DateFormat != tt.wantLayout {
				t.Errorf("DateFormat = %q, want %q", m.DateFormat, tt.wantLayout)
			}
		})
	}
}

// TestSniffDateFormat pins the layout sniff, especially the UK-first disambiguation: an
// ambiguous date reads as DD/MM, a day>12 forces DD/MM, a "month">12 forces US MM/DD.
func TestSniffDateFormat(t *testing.T) {
	tests := []struct {
		name    string
		samples []string
		want    string
		wantOK  bool
	}{
		{"ambiguous → UK DD/MM", []string{"05/06/2026", "07/08/2026"}, "02/01/2006", true},
		{"day>12 forces DD/MM", []string{"25/06/2026", "05/06/2026"}, "02/01/2006", true},
		{"month-position>12 forces US MM/DD", []string{"06/25/2026", "06/05/2026"}, "01/02/2006", true},
		{"ISO", []string{"2026-06-25", "2026-12-01"}, "2006-01-02", true},
		{"DD-MM-YYYY dashes", []string{"25-06-2026"}, "02-01-2006", true},
		{"named month", []string{"02 Jun 2026", "15 Dec 2026"}, "02 Jan 2006", true},
		{"unparseable → not ok", []string{"not-a-date"}, "", false},
		{"mixed/inconsistent → not ok", []string{"25/06/2026", "2026-06-25"}, "", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// One column (index 0) holding each sample on its own row.
			rows := make([][]string, len(tt.samples))
			for i, s := range tt.samples {
				rows[i] = []string{s}
			}
			got, ok := sniffDateFormat(rows, 0)
			if ok != tt.wantOK || got != tt.want {
				t.Errorf("sniffDateFormat(%v) = (%q, %v), want (%q, %v)", tt.samples, got, ok, tt.want, tt.wantOK)
			}
		})
	}
}

func TestNormaliseHeader(t *testing.T) {
	tests := map[string]string{
		"Paid-In (£)": "paid in",
		"Money_Out":   "money out",
		"  DATE  ":    "date",
		"Transaction  Date": "transaction date",
		string(rune(0xFEFF)) + "date": "date", // BOM-prefixed first cell
	}
	for in, want := range tests {
		if got := normaliseHeader(in); got != want {
			t.Errorf("normaliseHeader(%q) = %q, want %q", in, got, want)
		}
	}
}

// TestValidateMapping pins the commit-time gate: required columns present + in range, a
// known amount format with its column(s), and an allowlisted date layout.
func TestValidateMapping(t *testing.T) {
	good := ColumnMapping{
		DateColumn: ptr(0), DescriptionColumn: ptr(1),
		AmountFormat: amountFormatSigned, AmountColumn: ptr(2),
		DateFormat: "02/01/2006",
	}
	if err := validateMapping(good, 3); err != nil {
		t.Fatalf("valid signed mapping rejected: %v", err)
	}
	goodSplit := ColumnMapping{
		DateColumn: ptr(0), DescriptionColumn: ptr(1),
		AmountFormat: amountFormatSplit, MoneyInColumn: ptr(2), MoneyOutColumn: ptr(3),
		DateFormat: "2006-01-02",
	}
	if err := validateMapping(goodSplit, 4); err != nil {
		t.Fatalf("valid split mapping rejected: %v", err)
	}

	// Each of these must be a 422 (ErrCodeValidation).
	bad := map[string]ColumnMapping{
		"no date":          {DescriptionColumn: ptr(1), AmountFormat: amountFormatSigned, AmountColumn: ptr(2), DateFormat: "02/01/2006"},
		"no description":   {DateColumn: ptr(0), AmountFormat: amountFormatSigned, AmountColumn: ptr(2), DateFormat: "02/01/2006"},
		"signed no amount": {DateColumn: ptr(0), DescriptionColumn: ptr(1), AmountFormat: amountFormatSigned, DateFormat: "02/01/2006"},
		"split missing side": {DateColumn: ptr(0), DescriptionColumn: ptr(1), AmountFormat: amountFormatSplit, MoneyInColumn: ptr(2), DateFormat: "02/01/2006"},
		"unknown format":   {DateColumn: ptr(0), DescriptionColumn: ptr(1), AmountFormat: "weird", AmountColumn: ptr(2), DateFormat: "02/01/2006"},
		"out of range":     {DateColumn: ptr(0), DescriptionColumn: ptr(1), AmountFormat: amountFormatSigned, AmountColumn: ptr(9), DateFormat: "02/01/2006"},
		"bad date layout":  {DateColumn: ptr(0), DescriptionColumn: ptr(1), AmountFormat: amountFormatSigned, AmountColumn: ptr(2), DateFormat: "Mon Jan 2"},
	}
	for name, m := range bad {
		t.Run(name, func(t *testing.T) {
			err := validateMapping(m, 3)
			if err == nil {
				t.Fatalf("expected a validation error for %q", name)
			}
			if got := kernel.AsAppError(err).Code; got != kernel.ErrCodeValidation {
				t.Errorf("expected ErrCodeValidation, got %q (err: %v)", got, err)
			}
		})
	}
}
