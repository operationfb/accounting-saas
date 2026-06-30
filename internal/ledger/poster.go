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
	"errors"
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
	// PostEntry writes the journal entry for an event. If a prior (effective) entry
	// exists for the same source it is REVERSED first (append-only — never deleted),
	// then the fresh entry is posted.
	PostEntry(ctx context.Context, tx pgx.Tx, ec EntryContext) error
	// ReverseEntry posts a REVERSING entry (is_reversal = TRUE) that mirrors the effective
	// entry's lines with negated amounts, leaving BOTH as an audit trail. Used to UNDO an
	// event (invoice reopen, receipt delete) or crystallise an FX revaluation. No-op when
	// there is no effective entry to reverse.
	ReverseEntry(ctx context.Context, tx pgx.Tx, orgID uuid.UUID, sourceType string, sourceID uuid.UUID, asOf pgtype.Date, narrative string, createdBy uuid.UUID) error
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

	// Serialise concurrent posters for this SAME source (the append-only ledger has no
	// source uniqueness index). Held until the caller's tx ends; different sources don't block.
	if err := p.lockSource(ctx, lq, ec.OrganisationID, ec.SourceType, ec.SourceID); err != nil {
		return err
	}

	resolver := NewAccounts(p.cats.WithTx(tx), lq, p.auth.WithTx(tx))

	// Append-only: supersede any prior entry for this source by REVERSING it (never a
	// delete), then post the fresh entry below. The reversal is dated at the new entry's
	// date, so a prior period is never retroactively mutated.
	if ec.SourceID != uuid.Nil {
		if err := p.reverseLive(ctx, lq, ec.OrganisationID, ec.SourceType, ec.SourceID, ec.EntryDate, "Superseded (re-posted)", ec.CreatedBy); err != nil {
			return err
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

func (p *poster) ReverseEntry(ctx context.Context, tx pgx.Tx, orgID uuid.UUID, sourceType string, sourceID uuid.UUID, asOf pgtype.Date, narrative string, createdBy uuid.UUID) error {
	if sourceID == uuid.Nil {
		return nil
	}
	lq := p.ledger.WithTx(tx)
	if err := p.lockSource(ctx, lq, orgID, sourceType, sourceID); err != nil {
		return err
	}
	return p.reverseLive(ctx, lq, orgID, sourceType, sourceID, asOf, narrative, createdBy)
}

// lockSource takes a transaction-scoped advisory lock on a source identity so two
// transactions can never double-post the same source. Skips the no-source (MANUAL)
// case. Held until the caller's tx ends. Each poster call locks exactly its own source
// — a given business flow posts its sources in a deterministic order, so concurrent
// identical flows lock in the same order (no deadlock, just serialisation).
func (p *poster) lockSource(ctx context.Context, lq *ledgerdb.Queries, orgID uuid.UUID, sourceType string, sourceID uuid.UUID) error {
	if sourceID == uuid.Nil {
		return nil
	}
	if err := lq.LockSource(ctx, fmt.Sprintf("gl:%s:%s:%s", orgID, sourceType, sourceID)); err != nil {
		return kernel.ErrInternal(err)
	}
	return nil
}

// reverseLive posts a reversing entry for the EFFECTIVE (not-yet-reversed) entry of a
// source — negated lines, is_reversal = TRUE, pointing at the original — leaving both as
// an audit trail. No-op when there's nothing live to reverse. Shared by ReverseEntry
// (undo) and PostEntry (supersede-before-repost). The poster never deletes.
func (p *poster) reverseLive(ctx context.Context, lq *ledgerdb.Queries, orgID uuid.UUID, sourceType string, sourceID uuid.UUID, asOf pgtype.Date, narrative string, createdBy uuid.UUID) error {
	live, err := lq.GetJournalEntryForSource(ctx, ledgerdb.GetJournalEntryForSourceParams{
		OrganisationID: orgID,
		SourceType:     sourceType,
		SourceID:       pgUUID(sourceID),
	})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil // nothing effective to reverse
		}
		return kernel.ErrInternal(err)
	}

	lines, err := lq.ListLinesForEntry(ctx, live.ID)
	if err != nil {
		return kernel.ErrInternal(err)
	}
	if len(lines) == 0 {
		return nil
	}

	revID, err := lq.CreateReversalEntry(ctx, ledgerdb.CreateReversalEntryParams{
		OrganisationID:  orgID,
		EntryDate:       asOf,
		BaseCurrency:    live.BaseCurrency,
		Narrative:       pgText(narrative),
		SourceType:      sourceType,
		SourceID:        pgUUID(sourceID),
		CreatedByUserID: pgUUID(createdBy),
		ReversesEntryID: pgUUID(live.ID),
	})
	if err != nil {
		return kernel.ErrInternal(err)
	}

	// Mirror each line with negated amounts (the reversal still sums to zero).
	for _, ln := range lines {
		if err := lq.CreateJournalLine(ctx, ledgerdb.CreateJournalLineParams{
			JournalEntryID:  revID,
			OrganisationID:  orgID,
			AccountID:       ln.AccountID,
			Currency:        ln.Currency,
			AmountMinor:     -ln.AmountMinor,
			BaseAmountMinor: -ln.BaseAmountMinor,
			ExchangeRate:    ln.ExchangeRate,
			ContactID:       ln.ContactID,
			ProjectID:       ln.ProjectID,
			UserID:          ln.UserID,
			Description:     ln.Description,
		}); err != nil {
			return kernel.ErrInternal(err)
		}
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
