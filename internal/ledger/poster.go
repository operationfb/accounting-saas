package ledger

// poster.go
// =============================================================================
// The general-ledger POSTER: the generic interpreter that turns a source event into
// a balanced, multi-currency journal entry by reading gl_posting_rules and resolving
// each leg's account via ledger.Accounts. The Dr/Cr recipe lives in DATA, not here —
// adding an event is a seed change, not a poster change.
//
// It runs on the CALLER's transaction (the source service's kernel.WithTx), so the
// journal entry commits atomically with the business write. Replace semantics: any
// prior entry for the same (source_type, source_id) is deleted first, so a re-post
// (edit / re-issue) is idempotent.
//
// SCOPE (Iteration 7): the generic, non-fan-out, multi-currency path — enough for
// INVOICE_SENT. The payroll fan-out (per_employee / employee_filter) and the realized
// FX residual are clearly-marked extension points for the payroll / settlement slices.
// =============================================================================

import (
	"context"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/operationfb/accounting-saas/db/auth"
	"github.com/operationfb/accounting-saas/db/categories"
	ledgerdb "github.com/operationfb/accounting-saas/db/ledger"
	"github.com/operationfb/accounting-saas/internal/kernel"
)

// Poster writes a balanced journal entry for an economic event. Source services hold
// one (nil-guarded) and call PostEntry inside their own transaction.
type Poster interface {
	PostEntry(ctx context.Context, tx pgx.Tx, ec EntryContext) error
	// RemoveEntry deletes the live journal entry for a source event (its lines
	// cascade). Used when an event is undone — e.g. an invoice reopened out of SENT.
	RemoveEntry(ctx context.Context, tx pgx.Tx, orgID uuid.UUID, sourceType string, sourceID uuid.UUID) error
}

// Amount is one money component in BOTH the transaction currency and the org's base
// (home) currency. The poster never re-derives these — the source row provides them.
//
// Currency / ExchangeRate are optional PER-LEG overrides: on a single entry whose legs
// live in different currencies (an invoice receipt — bank-ccy cash, invoice-ccy debtor
// relief, home-ccy realised FX), each leg stamps its own. When unset they fall back to
// the entry-level ec.TxnCurrency / ec.ExchangeRate, so every existing single-currency
// caller (INVOICE_SENT, payroll, a same-currency receipt) is unaffected.
type Amount struct {
	Txn          int64          // in this LEG's transaction currency minor units
	Base         int64          // in the entry's base_currency minor units (the balancing figure)
	Currency     string         // optional per-leg txn currency; "" ⇒ ec.TxnCurrency
	ExchangeRate pgtype.Numeric // optional per-leg rate; !Valid ⇒ ec.ExchangeRate
}

// EntryContext is everything the poster needs from a source event. `Amounts` is keyed
// by gl_posting_rules.amount_basis (GROSS / NET / VAT / payroll components).
type EntryContext struct {
	OrganisationID uuid.UUID
	CompanyType    string // for gl_account_roles scope
	CountryCode    string // for gl_account_roles scope

	BaseCurrency string         // org native currency, e.g. "GBP"
	TxnCurrency  string         // the event's transaction currency, e.g. "EUR"
	ExchangeRate pgtype.Numeric // rate stamped on each line (NULL when txn == base)

	EventCode  string // gl_posting_rules.event_code, e.g. "INVOICE_SENT"
	SourceType string // gl_journal_entries.source_type, e.g. "INVOICE"
	SourceID   uuid.UUID
	EntryDate  pgtype.Date
	Narrative  string
	CreatedBy  uuid.UUID

	Amounts map[string]Amount

	// Resolver links (set whichever the event's roles need).
	CategoryID            *uuid.UUID
	BankAccountID         *uuid.UUID
	TransferBankAccountID *uuid.UUID
	UserID                *uuid.UUID
}

type poster struct {
	ledger *ledgerdb.Queries
	cats   *categories.Queries
	auth   *auth.Queries
}

// NewPoster wires the poster from the pool-backed query sets; PostEntry rebinds them
// to the caller's tx so the entry commits with the business write.
func NewPoster(ledgerQ *ledgerdb.Queries, catsQ *categories.Queries, authQ *auth.Queries) Poster {
	return &poster{ledger: ledgerQ, cats: catsQ, auth: authQ}
}

type builtLine struct {
	accountID uuid.UUID
	txn, base int64          // signed (DR +, CR −)
	currency  string         // this leg's txn currency (falls back to ec.TxnCurrency)
	rate      pgtype.Numeric // this leg's exchange rate (falls back to ec.ExchangeRate)
}

func (p *poster) PostEntry(ctx context.Context, tx pgx.Tx, ec EntryContext) error {
	lq := p.ledger.WithTx(tx)
	resolver := NewAccounts(p.cats.WithTx(tx), lq, p.auth.WithTx(tx))

	// Replace any prior entry for this source event (lines cascade).
	if ec.SourceID != uuid.Nil {
		if err := lq.DeleteJournalEntryForSource(ctx, ledgerdb.DeleteJournalEntryForSourceParams{
			OrganisationID: ec.OrganisationID,
			SourceType:     ec.SourceType,
			SourceID:       pgUUID(ec.SourceID),
		}); err != nil {
			return kernel.ErrInternal(err)
		}
	}

	legs, err := lq.ListPostingRulesForEvent(ctx, ledgerdb.ListPostingRulesForEventParams{
		EventCode:   ec.EventCode,
		CompanyType: ec.CompanyType,
	})
	if err != nil {
		return kernel.ErrInternal(err)
	}

	var lines []builtLine
	var baseSum int64
	for _, leg := range legs {
		// Extension point: payroll fan-out + the director/staff filter land with the
		// payroll slice. Fail loudly rather than silently mis-post.
		if leg.PerEmployee || leg.EmployeeFilter != "ALL" {
			return kernel.ErrInternal(fmt.Errorf("ledger: event %s leg %d uses payroll fan-out (per_employee/employee_filter), not yet supported by the poster", ec.EventCode, leg.LegNo))
		}

		amt, ok := ec.Amounts[leg.AmountBasis]
		if !ok {
			return kernel.ErrInternal(fmt.Errorf("ledger: event %s leg %d needs amount_basis %q which the source did not supply", ec.EventCode, leg.LegNo, leg.AmountBasis))
		}
		if amt.Base == 0 {
			continue // drop a zero leg (e.g. the VAT leg on a zero-rated invoice)
		}

		accountID, rerr := resolver.Resolve(ctx, leg.AccountRole, ResolveInput{
			OrganisationID:        ec.OrganisationID,
			CompanyType:           ec.CompanyType,
			CountryCode:           ec.CountryCode,
			PickedCategoryID:      ec.CategoryID,
			UserID:                ec.UserID,
			BankAccountID:         ec.BankAccountID,
			TransferBankAccountID: ec.TransferBankAccountID,
		})
		if rerr != nil {
			return rerr
		}

		sign := int64(1)
		if leg.Direction == "CR" {
			sign = -1
		}
		// Per-leg currency/rate override (multi-currency entry) with entry-level fallback.
		legCurrency := ec.TxnCurrency
		if amt.Currency != "" {
			legCurrency = amt.Currency
		}
		legRate := ec.ExchangeRate
		if amt.ExchangeRate.Valid {
			legRate = amt.ExchangeRate
		}
		lines = append(lines, builtLine{
			accountID: accountID,
			txn:       sign * amt.Txn,
			base:      sign * amt.Base,
			currency:  legCurrency,
			rate:      legRate,
		})
		baseSum += sign * amt.Base
	}

	if len(lines) == 0 {
		return nil // nothing to post (e.g. a zero-value invoice)
	}
	if baseSum != 0 {
		// Belt-and-braces with the DB trigger; should never fire for a correct mapping.
		return kernel.ErrInternal(fmt.Errorf("ledger: %s entry does not balance: Σ base_amount_minor = %d", ec.EventCode, baseSum))
	}

	entryID, err := lq.CreateJournalEntry(ctx, ledgerdb.CreateJournalEntryParams{
		OrganisationID:  ec.OrganisationID,
		EntryDate:       ec.EntryDate,
		BaseCurrency:    ec.BaseCurrency,
		Narrative:       pgText(ec.Narrative),
		SourceType:      ec.SourceType,
		SourceID:        pgUUID(ec.SourceID),
		CreatedByUserID: pgUUID(ec.CreatedBy),
	})
	if err != nil {
		return kernel.ErrInternal(err)
	}

	for _, ln := range lines {
		if err := lq.CreateJournalLine(ctx, ledgerdb.CreateJournalLineParams{
			JournalEntryID:  entryID,
			OrganisationID:  ec.OrganisationID,
			AccountID:       ln.accountID,
			Currency:        ln.currency,
			AmountMinor:     ln.txn,
			BaseAmountMinor: ln.base,
			ExchangeRate:    ln.rate,
		}); err != nil {
			return kernel.ErrInternal(err)
		}
	}
	return nil
}

func (p *poster) RemoveEntry(ctx context.Context, tx pgx.Tx, orgID uuid.UUID, sourceType string, sourceID uuid.UUID) error {
	if sourceID == uuid.Nil {
		return nil
	}
	if err := p.ledger.WithTx(tx).DeleteJournalEntryForSource(ctx, ledgerdb.DeleteJournalEntryForSourceParams{
		OrganisationID: orgID,
		SourceType:     sourceType,
		SourceID:       pgUUID(sourceID),
	}); err != nil {
		return kernel.ErrInternal(err)
	}
	return nil
}

func pgUUID(u uuid.UUID) pgtype.UUID {
	if u == uuid.Nil {
		return pgtype.UUID{}
	}
	return pgtype.UUID{Bytes: u, Valid: true}
}

func pgText(s string) pgtype.Text {
	if s == "" {
		return pgtype.Text{}
	}
	return pgtype.Text{String: s, Valid: true}
}
