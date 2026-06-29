package main

// gl_payroll_rules_test.go
// =============================================================================
// Proves the seeded PAYROLL_COMPLETED mapping is a BALANCED double-entry, applying
// the employee_filter (STAFF/DIRECTOR/ALL) the way the future poster will: each leg
// sums its amount_basis over the payslips matching its filter. Covers an all-staff
// run, an all-director run, and a MIXED run (the director-vs-staff split), each
// asserting Σ = 0; the mixed case also checks the costs land on the right side.
// =============================================================================

import (
	"context"
	"testing"

	dbledger "github.com/operationfb/accounting-saas/db/ledger"
)

// payslip is a sample set of computed payslip figures (pence) + whether the employee
// is a director (nic_calculation != 'employee').
type payslip struct {
	gross, paye, eeNI, erNI, eePension, erPension, studentLoan, other int64
	director                                                          bool
}

func (p payslip) net() int64 {
	return p.gross - p.paye - p.eeNI - p.eePension - p.studentLoan - p.other
}

// component returns the pence value for a gl_posting_rules.amount_basis.
func (p payslip) component(basis string) (int64, bool) {
	switch basis {
	case "GROSS_PAY":
		return p.gross, true
	case "PAYE":
		return p.paye, true
	case "EMPLOYEE_NI":
		return p.eeNI, true
	case "EMPLOYER_NI":
		return p.erNI, true
	case "EMPLOYEE_PENSION":
		return p.eePension, true
	case "EMPLOYER_PENSION":
		return p.erPension, true
	case "STUDENT_LOAN":
		return p.studentLoan, true
	case "OTHER_DEDUCTIONS":
		return p.other, true
	case "NET_PAY":
		return p.net(), true
	default:
		return 0, false
	}
}

// matches reports whether a payslip feeds a leg with the given employee_filter.
func matchesFilter(filter string, director bool) bool {
	switch filter {
	case "ALL":
		return true
	case "DIRECTOR":
		return director
	case "STAFF":
		return !director
	default:
		return false
	}
}

func TestPayrollAccrualRulesBalance(t *testing.T) {
	ts := newTestServer(t)
	ctx := context.Background()
	q := dbledger.New(ts.pool)

	legs, err := q.ListPostingRulesForEvent(ctx, dbledger.ListPostingRulesForEventParams{
		EventCode:   "PAYROLL_COMPLETED",
		CompanyType: "limited",
	})
	if err != nil {
		t.Fatalf("load PAYROLL_COMPLETED rules: %v", err)
	}
	if len(legs) == 0 {
		t.Fatal("no PAYROLL_COMPLETED legs seeded")
	}

	staff := payslip{gross: 300000, paye: 40000, eeNI: 20000, erNI: 25000}
	director := payslip{gross: 500000, paye: 90000, eeNI: 30000, erNI: 45000, erPension: 12000, director: true}

	// legAmount sums a leg's basis over the payslips its employee_filter matches.
	legAmount := func(leg dbledger.ListPostingRulesForEventRow, run []payslip) int64 {
		var total int64
		for _, p := range run {
			if !matchesFilter(leg.EmployeeFilter, p.director) {
				continue
			}
			amt, ok := p.component(leg.AmountBasis)
			if !ok {
				t.Fatalf("leg %d uses amount_basis %q with no payslip component", leg.LegNo, leg.AmountBasis)
			}
			total += amt
		}
		return total
	}

	cases := map[string][]payslip{
		"all staff":    {staff},
		"all director": {director},
		"mixed":        {staff, director},
	}

	for name, run := range cases {
		t.Run(name, func(t *testing.T) {
			var sum, drTotal, crTotal int64
			for _, leg := range legs {
				amt := legAmount(leg, run)
				if amt == 0 {
					continue // empty group / zero component → dropped at post time
				}
				switch leg.Direction {
				case "DR":
					sum += amt
					drTotal += amt
				case "CR":
					sum -= amt
					crTotal += amt
				default:
					t.Fatalf("leg %d has bad direction %q", leg.LegNo, leg.Direction)
				}
			}
			if sum != 0 {
				t.Errorf("payroll journal does not balance: Σ = %d (Dr %d, Cr %d)", sum, drTotal, crTotal)
			}
			if drTotal == 0 || crTotal == 0 {
				t.Errorf("payroll journal is one-sided (Dr %d, Cr %d)", drTotal, crTotal)
			}
		})
	}

	// The mixed run must route costs to the right side: the DIRECTOR gross/NI legs see
	// only the director's figures, the STAFF legs only the staff's.
	mixed := []payslip{staff, director}
	for _, leg := range legs {
		switch leg.AccountRole {
		case "PAYROLL_DIRECTOR_GROSS_EXPENSE":
			if got := legAmount(leg, mixed); got != director.gross {
				t.Errorf("director gross leg = %d, want %d", got, director.gross)
			}
		case "PAYROLL_GROSS_EXPENSE":
			if got := legAmount(leg, mixed); got != staff.gross {
				t.Errorf("staff gross leg = %d, want %d", got, staff.gross)
			}
		case "PAYROLL_DIRECTOR_EMPLOYER_NI_EXPENSE":
			if got := legAmount(leg, mixed); got != director.erNI {
				t.Errorf("director employer-NI leg = %d, want %d", got, director.erNI)
			}
		}
	}
}
