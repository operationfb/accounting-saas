package banking

// explain.go
// =============================================================================
// "Explaining" a bank transaction — the reconcile WRITE path. An explanation
// assigns an accounting treatment (a type + category/entity + VAT) to all or PART
// of a line; the recompute trigger keeps the line's status + unexplained_amount in
// sync. A line can be SPLIT across several explanations summing to its amount.
//
// Type → what to supply (the type's entity_link, validated here):
//   NONE / CAPITAL_ASSET — a category OFFERED for the type (Payment, Sales, …)
//   BANK_ACCOUNT         — another bank account in the org (Transfer)
//   USER                 — an active member + the user-account category (Money to/from User)
// The 6 future-entity types (Bill/Invoice/Credit Note/HP) are NOT yet supported
// (categories.SupportedEntityLinks is the shared whitelist).
//
// MONEY: the portion amount crosses the API as a POSITIVE pound string; the service
// signs it to the line's direction (money-out line → negative gross). VAT is
// EXTRACTED from the portion gross (money.ComputeFixedVAT). Owner/admin only.
// =============================================================================

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/shopspring/decimal"

	banking "github.com/operationfb/accounting-saas/db/banking"
	billsdb "github.com/operationfb/accounting-saas/db/bills"
	categoriesdb "github.com/operationfb/accounting-saas/db/categories"
	invoicesdb "github.com/operationfb/accounting-saas/db/invoices"
	categories "github.com/operationfb/accounting-saas/internal/categories"
	"github.com/operationfb/accounting-saas/internal/fx"
	"github.com/operationfb/accounting-saas/internal/kernel"
	"github.com/operationfb/accounting-saas/internal/ledger"
	"github.com/operationfb/accounting-saas/money"
)

// Local constants for the Invoice Receipt flow. typeInvoiceReceipt is the
// transaction_types code; invoiceStatusSent mirrors invoices.status = 'SENT' (only a
// sent invoice can be paid). Kept local so the banking package doesn't import the
// internal/invoices package just for two strings.
const (
	typeInvoiceReceipt = "INVOICE_RECEIPT"
	invoiceStatusSent  = "SENT"
	typeBillPayment    = "BILL_PAYMENT"
)

// =============================================================================
// DTOs
// =============================================================================

// CreateExplanationRequest is the body for adding/editing one explanation (a whole
// line or a split portion). Amount is a POSITIVE pound string; the service signs it
// to the line's direction. Which of category/transfer/user is required depends on
// the chosen type's entity_link. Reused for create and update.
type CreateExplanationRequest struct {
	Type                  string  `json:"type" binding:"required"`
	Amount                string  `json:"amount" binding:"required"` // pounds, positive (this portion)
	CategoryID            *string `json:"category_id"`               // category types + user payments
	TransferBankAccountID *string `json:"transfer_bank_account_id"`  // transfers
	PaidUserID            *string `json:"paid_user_id"`              // user payments
	PaidInvoiceID         *string `json:"paid_invoice_id"`           // invoice receipts
	PaidBillID            *string `json:"paid_bill_id"`              // bill payments
	VATRateID             *string `json:"vat_rate_id"`               // optional
	VATAmount             *string `json:"vat_amount"`                // manual (non-fixed) rate only
	Description           *string `json:"description"`
	DatedOn               *string `json:"dated_on"` // YYYY-MM-DD, defaults to the transaction's date
}

// ExplanationResponse is one explanation in the API shape. Amount + VATValue are
// positive pound strings (the direction is the line's). The resolved display names
// come from the detailed JOIN query.
type ExplanationResponse struct {
	ID                    string  `json:"id"`
	Type                  string  `json:"type"`
	Amount                string  `json:"amount"`
	CategoryID            *string `json:"category_id,omitempty"`
	CategoryName          *string `json:"category_name,omitempty"`
	CategoryNominalCode   *string `json:"category_nominal_code,omitempty"`
	TransferBankAccountID *string `json:"transfer_bank_account_id,omitempty"`
	TransferAccountName   *string `json:"transfer_account_name,omitempty"`
	PaidUserID            *string `json:"paid_user_id,omitempty"`
	PaidUserName          *string `json:"paid_user_name,omitempty"`
	PaidInvoiceID         *string `json:"paid_invoice_id,omitempty"`
	InvoiceReference      *string `json:"invoice_reference,omitempty"`
	PaidBillID            *string `json:"paid_bill_id,omitempty"`
	BillReference         *string `json:"bill_reference,omitempty"`
	VATRateID             *string `json:"vat_rate_id,omitempty"`
	VATRate               *string `json:"vat_rate,omitempty"` // e.g. "20%"
	VATValue              string  `json:"vat_value"`          // pounds
	Description           *string `json:"description,omitempty"`
	DatedOn               string  `json:"dated_on"`
	MarkedForReview       bool    `json:"marked_for_review"`
}

// TransactionExplanationsResponse is the GET + every-mutation response: the line's
// recomputed reconcile state (from the trigger) plus its live explanations.
// UnexplainedAmount is the pounds remaining to explain.
type TransactionExplanationsResponse struct {
	TransactionID     string                 `json:"transaction_id"`
	Status            string                 `json:"status"`
	UnexplainedAmount string                 `json:"unexplained_amount"`
	Explanations      []*ExplanationResponse `json:"explanations"`
}

// =============================================================================
// READ
// =============================================================================

// ListExplanations returns a line's explanations + its reconcile state. Any active
// member may read (the explain panel needs it).
func (s *Service) ListExplanations(ctx context.Context, authUserID, authOrgID uuid.UUID, accountID, txnID string) (*TransactionExplanationsResponse, error) {
	accountUUID, txnUUID, err := parseAccountAndTxn(accountID, txnID)
	if err != nil {
		return nil, err
	}
	if _, err := s.authorize(ctx, authUserID, authOrgID); err != nil {
		return nil, err
	}
	if _, err := s.loadTransactionForAccount(ctx, authOrgID, accountUUID, txnUUID); err != nil {
		return nil, err
	}
	return s.buildExplanationsResponse(ctx, authOrgID, txnUUID)
}

// =============================================================================
// MUTATIONS (owner/admin)
// =============================================================================

// CreateExplanation adds one explanation (a whole line or a split portion) and
// returns the line's refreshed reconcile state + explanations.
func (s *Service) CreateExplanation(ctx context.Context, authUserID, authOrgID uuid.UUID, accountID, txnID string, req CreateExplanationRequest) (*TransactionExplanationsResponse, error) {
	accountUUID, txnUUID, err := parseAccountAndTxn(accountID, txnID)
	if err != nil {
		return nil, err
	}
	if err := s.requireAdmin(ctx, authUserID, authOrgID); err != nil {
		return nil, err
	}
	txn, err := s.loadTransactionForAccount(ctx, authOrgID, accountUUID, txnUUID)
	if err != nil {
		return nil, err
	}
	params, err := s.resolveExplanationFields(ctx, authUserID, authOrgID, accountUUID, txn, req, nil)
	if err != nil {
		return nil, err
	}
	// Filed-period lock: refuse a new explanation dated inside an already-filed VAT return.
	if err := s.assertNotFiled(ctx, authOrgID, params.DatedOn); err != nil {
		return nil, err
	}
	// Write the explanation and (for an invoice receipt) re-sync the invoice's paid
	// value in ONE transaction, so invoices.paid_value_minor can never drift from the
	// explanations that drive it.
	err = kernel.WithTx(ctx, s.pool, func(tx pgx.Tx) error {
		qtx := s.queries.WithTx(tx)
		created, err := qtx.CreateExplanation(ctx, params)
		if err != nil {
			return kernel.ErrInternal(err)
		}
		if err := s.resyncInvoicePaid(ctx, qtx, s.invoiceQueries.WithTx(tx), authOrgID, params.PaidInvoiceID); err != nil {
			return err
		}
		if err := s.resyncBillPaid(ctx, qtx, s.billQueries.WithTx(tx), authOrgID, params.PaidBillID); err != nil {
			return err
		}
		// GL: an invoice receipt posts Dr Bank / Cr Debtors (+ realised FX). Re-post all of
		// the invoice's receipts so the cumulative debtor relief stays exact.
		if created.Type == typeInvoiceReceipt {
			return s.repostAndRevalueInvoice(ctx, tx, authUserID, authOrgID, params.PaidInvoiceID)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return s.buildExplanationsResponse(ctx, authOrgID, txnUUID)
}

// UpdateExplanation edits one explanation (re-categorise, fix the amount/VAT).
func (s *Service) UpdateExplanation(ctx context.Context, authUserID, authOrgID uuid.UUID, accountID, txnID, explID string, req CreateExplanationRequest) (*TransactionExplanationsResponse, error) {
	accountUUID, txnUUID, err := parseAccountAndTxn(accountID, txnID)
	if err != nil {
		return nil, err
	}
	explUUID, err := uuid.Parse(explID)
	if err != nil {
		return nil, kernel.ErrValidation("explanation id is not a valid UUID", err)
	}
	if err := s.requireAdmin(ctx, authUserID, authOrgID); err != nil {
		return nil, err
	}
	txn, err := s.loadTransactionForAccount(ctx, authOrgID, accountUUID, txnUUID)
	if err != nil {
		return nil, err
	}
	existing, err := s.loadExplanationForTxn(ctx, authOrgID, txnUUID, explUUID)
	if err != nil {
		return nil, err
	}
	// Editing: existing is passed through so its old portion is "given back" before the
	// over-explain check AND the invoice overpayment cap.
	params, err := s.resolveExplanationFields(ctx, authUserID, authOrgID, accountUUID, txn, req, &existing)
	if err != nil {
		return nil, err
	}
	// Filed-period lock: block the edit if the explanation's OLD or NEW date is in a filed period.
	if err := s.assertNotFiled(ctx, authOrgID, existing.DatedOn, params.DatedOn); err != nil {
		return nil, err
	}
	// Update the explanation and re-sync the affected invoice(s) in one transaction.
	// Re-pointing a receipt (or changing its type away from INVOICE_RECEIPT) must
	// recompute BOTH the new link and the old one, so paid moves correctly between them.
	err = kernel.WithTx(ctx, s.pool, func(tx pgx.Tx) error {
		qtx := s.queries.WithTx(tx)
		itx := s.invoiceQueries.WithTx(tx)
		btx := s.billQueries.WithTx(tx)
		if _, err := qtx.UpdateExplanation(ctx, banking.UpdateExplanationParams{
			ID:                    explUUID,
			OrganisationID:        authOrgID,
			DatedOn:               params.DatedOn,
			Description:           params.Description,
			Type:                  params.Type,
			GrossValueMinor:       params.GrossValueMinor,
			CategoryID:            params.CategoryID,
			SalesTaxStatus:        params.SalesTaxStatus,
			SalesTaxRateID:        params.SalesTaxRateID,
			SalesTaxValueMinor:    params.SalesTaxValueMinor,
			IsManualSalesTax:      params.IsManualSalesTax,
			EcStatus:              params.EcStatus,
			PlaceOfSupply:         params.PlaceOfSupply,
			TransferBankAccountID: params.TransferBankAccountID,
			PaidUserID:            params.PaidUserID,
			PaidInvoiceID:         params.PaidInvoiceID,
			PaidBillID:            params.PaidBillID,
			MarkedForReview:       params.MarkedForReview,
			ChequeNumber:          params.ChequeNumber,
			ReceiptReference:      params.ReceiptReference,
		}); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return kernel.ErrNotFound("explanation", explID)
			}
			return kernel.ErrInternal(err)
		}
		if err := s.resyncInvoicePaid(ctx, qtx, itx, authOrgID, params.PaidInvoiceID); err != nil {
			return err
		}
		if existing.PaidInvoiceID.Valid && existing.PaidInvoiceID != params.PaidInvoiceID {
			if err := s.resyncInvoicePaid(ctx, qtx, itx, authOrgID, existing.PaidInvoiceID); err != nil {
				return err
			}
		}
		if err := s.resyncBillPaid(ctx, qtx, btx, authOrgID, params.PaidBillID); err != nil {
			return err
		}
		if existing.PaidBillID.Valid && existing.PaidBillID != params.PaidBillID {
			if err := s.resyncBillPaid(ctx, qtx, btx, authOrgID, existing.PaidBillID); err != nil {
				return err
			}
		}
		// GL: keep the receipt entries in step. Re-posting an invoice's receipts replaces
		// each live entry (handles amount/rate changes) and reassigns the residual relief.
		switch {
		case params.Type == typeInvoiceReceipt:
			// Re-point: also fix the OLD invoice's residual (this receipt left its list).
			if existing.Type == typeInvoiceReceipt && existing.PaidInvoiceID.Valid && existing.PaidInvoiceID != params.PaidInvoiceID {
				if err := s.repostAndRevalueInvoice(ctx, tx, authUserID, authOrgID, existing.PaidInvoiceID); err != nil {
					return err
				}
			}
			return s.repostAndRevalueInvoice(ctx, tx, authUserID, authOrgID, params.PaidInvoiceID)
		case existing.Type == typeInvoiceReceipt:
			// Type changed away from receipt: drop this entry, then re-post the old invoice's
			// remaining receipts so their cumulative relief closes correctly without it.
			if err := s.removeReceiptGL(ctx, tx, authOrgID, explUUID); err != nil {
				return err
			}
			return s.repostAndRevalueInvoice(ctx, tx, authUserID, authOrgID, existing.PaidInvoiceID)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return s.buildExplanationsResponse(ctx, authOrgID, txnUUID)
}

// DeleteExplanation removes one explanation (un-explain part of a line).
func (s *Service) DeleteExplanation(ctx context.Context, authUserID, authOrgID uuid.UUID, accountID, txnID, explID string) (*TransactionExplanationsResponse, error) {
	accountUUID, txnUUID, err := parseAccountAndTxn(accountID, txnID)
	if err != nil {
		return nil, err
	}
	explUUID, err := uuid.Parse(explID)
	if err != nil {
		return nil, kernel.ErrValidation("explanation id is not a valid UUID", err)
	}
	if err := s.requireAdmin(ctx, authUserID, authOrgID); err != nil {
		return nil, err
	}
	if _, err := s.loadTransactionForAccount(ctx, authOrgID, accountUUID, txnUUID); err != nil {
		return nil, err
	}
	existing, err := s.loadExplanationForTxn(ctx, authOrgID, txnUUID, explUUID)
	if err != nil {
		return nil, err
	}
	// Filed-period lock: refuse to delete an explanation dated inside a filed VAT return.
	if err := s.assertNotFiled(ctx, authOrgID, existing.DatedOn); err != nil {
		return nil, err
	}
	// Soft-delete the explanation and, if it settled an invoice, re-sync that invoice's
	// paid value (it drops by this receipt's portion) in the same transaction.
	err = kernel.WithTx(ctx, s.pool, func(tx pgx.Tx) error {
		qtx := s.queries.WithTx(tx)
		if err := qtx.SoftDeleteExplanation(ctx, banking.SoftDeleteExplanationParams{ID: explUUID, OrganisationID: authOrgID}); err != nil {
			return kernel.ErrInternal(err)
		}
		if err := s.resyncInvoicePaid(ctx, qtx, s.invoiceQueries.WithTx(tx), authOrgID, existing.PaidInvoiceID); err != nil {
			return err
		}
		if err := s.resyncBillPaid(ctx, qtx, s.billQueries.WithTx(tx), authOrgID, existing.PaidBillID); err != nil {
			return err
		}
		// GL: remove this receipt's journal entry (lines cascade), then re-post the invoice's
		// remaining receipts so their cumulative debtor relief closes correctly without it.
		if existing.Type == typeInvoiceReceipt {
			if err := s.removeReceiptGL(ctx, tx, authOrgID, explUUID); err != nil {
				return err
			}
			return s.repostAndRevalueInvoice(ctx, tx, authUserID, authOrgID, existing.PaidInvoiceID)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return s.buildExplanationsResponse(ctx, authOrgID, txnUUID)
}

// =============================================================================
// GENERAL LEDGER (invoice receipt)
// =============================================================================

// repostAndRevalueInvoice re-posts an invoice's receipt entries (realised FX, 390) and
// then keeps its UNREALISED revaluation (391) in step in the SAME transaction: a partial
// receipt re-revalues the reduced due, a full settlement crystallises with an explicit
// reversal. The revaluer is nil-guarded, so a deployment without it just re-posts receipts.
func (s *Service) repostAndRevalueInvoice(ctx context.Context, tx pgx.Tx, authUserID, orgID uuid.UUID, invoiceID pgtype.UUID) error {
	if err := s.repostInvoiceReceipts(ctx, tx, orgID, invoiceID); err != nil {
		return err
	}
	if s.invoiceRevaluer == nil || !invoiceID.Valid {
		return nil
	}
	return s.invoiceRevaluer.OnInvoiceReceiptChanged(ctx, tx, orgID, uuid.UUID(invoiceID.Bytes), time.Now(), authUserID)
}

// repostInvoiceReceipts re-derives and re-posts the GL journal entry for EVERY live
// INVOICE_RECEIPT explanation settling one invoice, in deterministic order. It runs inside
// the caller's transaction after a receipt is created/edited/deleted, so the entries stay
// in step with paid_value_minor. Each receipt's entry is:
//
//	DR Bank   (750-x)  gross, bank ccy            — home value B (at receipt rate)
//	CR Debtors (681)   settled, invoice ccy       — home value D (debtor relief at booking rate)
//	CR/DR realised FX (390)                        — G = B − D (gain CR / loss DR), home ccy
//
// The debtor relief D is the DIFFERENCE OF CUMULATIVE APPORTIONMENTS of the invoice's
// native (home) total against the running settled total — so the rounding crumb is absorbed
// and Σ D = native_total exactly once the invoice is fully paid, closing the home receivable
// to zero. The poster drops the zero FX leg, so a home/same-currency receipt collapses to the
// original 2-leg Dr Bank / Cr Debtors entry. PostEntry replaces any prior entry for the same
// (INVOICE_RECEIPT, explanation.id), making the re-post idempotent. A removed/re-pointed
// receipt is handled by the caller (removeReceiptGL); it is simply absent from this list.
func (s *Service) repostInvoiceReceipts(ctx context.Context, tx pgx.Tx, orgID uuid.UUID, invoiceID pgtype.UUID) error {
	if s.poster == nil || !invoiceID.Valid {
		return nil
	}
	invUUID, err := uuid.FromBytes(invoiceID.Bytes[:])
	if err != nil {
		return kernel.ErrInternal(err)
	}
	org, err := s.authQueries.GetOrganisation(ctx, orgID)
	if err != nil {
		return kernel.ErrInternal(err)
	}
	inv, err := s.invoiceQueries.WithTx(tx).GetInvoice(ctx, invoicesdb.GetInvoiceParams{ID: invUUID, OrganisationID: orgID})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil // invoice vanished (concurrently deleted) — nothing to post
		}
		return kernel.ErrInternal(err)
	}
	// List within the tx so the just-written/updated explanation is visible.
	receipts, err := s.queries.WithTx(tx).ListInvoiceReceiptsForInvoice(ctx, banking.ListInvoiceReceiptsForInvoiceParams{
		PaidInvoiceID:  invoiceID,
		OrganisationID: orgID,
	})
	if err != nil {
		return kernel.ErrInternal(err)
	}

	homeCcy := org.NativeCurrency
	var cumBefore int64 // running settled total, in the invoice's currency
	for _, r := range receipts {
		settled := r.GrossValueMinor // home/legacy receipts: settled == gross (invoice ccy == bank ccy)
		if r.SettledInvoiceMinor.Valid {
			settled = r.SettledInvoiceMinor.Int64
		}
		base := r.GrossValueMinor // home receipts: base == gross
		if r.BaseValueMinor.Valid {
			base = r.BaseValueMinor.Int64
		}

		cumAfter := cumBefore + settled
		// Debtor relief at the ORIGINAL booking rate = the cumulative-apportionment delta.
		relief := money.Apportion(inv.NativeTotalValueMinor, cumAfter, inv.TotalValueMinor) -
			money.Apportion(inv.NativeTotalValueMinor, cumBefore, inv.TotalValueMinor)
		cumBefore = cumAfter

		g := fx.RealisedGainLoss(base, relief) // B − D
		var gain, loss int64
		if g > 0 {
			gain = g
		} else {
			loss = -g
		}

		bankCcy := homeCcy
		if r.Currency.Valid {
			bankCcy = r.Currency.String
		}
		bankID := r.BankAccountID
		createdBy := uuid.Nil
		if r.CreatedByUserID.Valid {
			if u, e := uuid.FromBytes(r.CreatedByUserID.Bytes[:]); e == nil {
				createdBy = u
			}
		}

		amounts := map[string]ledger.Amount{
			"GROSS":         {Txn: r.GrossValueMinor, Base: base, Currency: bankCcy, ExchangeRate: r.ExchangeRate},
			"DEBTOR_RELIEF": {Txn: settled, Base: relief, Currency: inv.Currency, ExchangeRate: inv.ExchangeRate},
			"FX_GAIN":       {Txn: gain, Base: gain, Currency: homeCcy},
			"FX_LOSS":       {Txn: loss, Base: loss, Currency: homeCcy},
		}
		if err := s.poster.PostEntry(ctx, tx, ledger.EntryContext{
			OrganisationID: orgID,
			CompanyType:    org.CompanyType.String,
			CountryCode:    org.CountryCode,
			BaseCurrency:   homeCcy,
			TxnCurrency:    homeCcy, // entry-level fallback; every leg stamps its own currency
			EventCode:      typeInvoiceReceipt,
			SourceType:     typeInvoiceReceipt,
			SourceID:       r.ID,
			EntryDate:      r.DatedOn,
			Narrative:      "Invoice receipt",
			CreatedBy:      createdBy,
			Amounts:        amounts,
			BankAccountID:  &bankID,
		}); err != nil {
			if errors.Is(err, ledger.ErrChartNotProvisioned) {
				return nil // org has no chart of accounts — skip GL (paid_value sync still ran)
			}
			return err
		}
	}
	return nil
}

// removeReceiptGL deletes the receipt's journal entry (lines cascade). Nil-guarded.
func (s *Service) removeReceiptGL(ctx context.Context, tx pgx.Tx, orgID, explID uuid.UUID) error {
	if s.poster == nil {
		return nil
	}
	return s.poster.RemoveEntry(ctx, tx, orgID, typeInvoiceReceipt, explID)
}

// =============================================================================
// VALIDATION / RESOLUTION
// =============================================================================

// resolveExplanationFields validates the request against the chosen type and the
// line, computes the signed gross + VAT, and returns the DB-ready insert params.
// existing is non-nil on UPDATE (the explanation being edited) — its portion is
// returned to the remaining pool before the over-explain check, and (for an invoice
// receipt) before the invoice's overpayment cap.
func (s *Service) resolveExplanationFields(ctx context.Context, authUserID, orgID, accountUUID uuid.UUID, txn banking.BankTransaction, req CreateExplanationRequest, existing *banking.BankTransactionExplanation) (banking.CreateExplanationParams, error) {
	var zero banking.CreateExplanationParams

	// 1. type: must exist, be supported in v1, and match the line's direction.
	typeCode := strings.TrimSpace(req.Type)
	tt, err := s.catQueries.GetTransactionType(ctx, typeCode)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return zero, kernel.ErrValidation("unknown transaction type", nil)
		}
		return zero, kernel.ErrInternal(err)
	}
	if !categories.SupportedEntityLinks[tt.EntityLink] {
		return zero, kernel.ErrValidation("that transaction type isn't explainable yet", nil)
	}
	lineDir := "in"
	if txn.AmountMinor < 0 {
		lineDir = "out"
	}
	if tt.Direction != lineDir {
		return zero, kernel.ErrValidation("the chosen type is money-"+tt.Direction+", but this is a money-"+lineDir+" transaction", nil)
	}

	// 2. amount → signed gross (matches the line's direction).
	amtMinor, err := money.PoundsToMinor(strings.TrimSpace(req.Amount))
	if err != nil {
		return zero, kernel.ErrValidation("amount must be a valid amount", err)
	}
	if amtMinor <= 0 {
		return zero, kernel.ErrValidation("amount must be greater than zero", nil)
	}
	grossMinor := amtMinor
	if lineDir == "out" {
		grossMinor = -amtMinor
	}

	// 3. over-explain guard: this portion can't exceed what's left to explain.
	remaining := txn.AmountMinor
	if txn.UnexplainedAmountMinor.Valid {
		remaining = txn.UnexplainedAmountMinor.Int64
	}
	if existing != nil {
		remaining += existing.GrossValueMinor // editing: give the old portion back first
	}
	if absInt64(grossMinor) > absInt64(remaining) {
		return zero, kernel.ErrValidation("that's more than the amount left to explain on this transaction", nil)
	}

	// 3b. dated_on (the receipt date) defaults to the transaction's date. Computed before
	// the entity switch because an invoice receipt's FX rate is looked up on this date.
	dated := txn.DatedOn
	if req.DatedOn != nil && strings.TrimSpace(*req.DatedOn) != "" {
		t, perr := time.Parse("2006-01-02", strings.TrimSpace(*req.DatedOn))
		if perr != nil {
			return zero, kernel.ErrValidation("dated_on must be in YYYY-MM-DD format", perr)
		}
		dated = pgtype.Date{Time: t, Valid: true}
	}

	// 4. category / entity, by the type's entity_link.
	var categoryID, transferAccountID, paidUserID, paidInvoiceID, paidBillID pgtype.UUID
	var fxv receiptFX // populated for an INVOICE receipt; zero (NULLs) otherwise
	switch tt.EntityLink {
	case "BANK_ACCOUNT": // Transfer to/from Another Account
		transferAccountID, err = s.resolveTransferAccount(ctx, orgID, accountUUID, req.TransferBankAccountID)
		if err != nil {
			return zero, err
		}
	case "USER": // Money Paid/Received to/from User
		paidUserID, err = s.resolveUser(ctx, orgID, req.PaidUserID)
		if err != nil {
			return zero, err
		}
		categoryID, err = s.requireOfferedCategory(ctx, orgID, typeCode, req.CategoryID)
		if err != nil {
			return zero, err
		}
	case "INVOICE": // Invoice Receipt — settle a sent sales invoice (no category, no VAT)
		paidInvoiceID, fxv, err = s.resolveInvoice(ctx, orgID, accountUUID, req.PaidInvoiceID, grossMinor, dated, existing)
		if err != nil {
			return zero, err
		}
	case "BILL": // Bill Payment — settle an unpaid purchase bill (no category, no VAT)
		paidBillID, err = s.resolveBill(ctx, orgID, req.PaidBillID, grossMinor, existing)
		if err != nil {
			return zero, err
		}
	default: // NONE, CAPITAL_ASSET → a plain category pick
		categoryID, err = s.requireOfferedCategory(ctx, orgID, typeCode, req.CategoryID)
		if err != nil {
			return zero, err
		}
	}

	// 5. VAT: optional rate → a fixed rate extracts the VAT from the portion gross;
	// a manual (non-fixed) rate stores the client-entered amount. An invoice receipt
	// carries no VAT of its own (the VAT lived on the invoice/bill), so it is skipped.
	var vatRateID pgtype.UUID
	var vatValueMinor int64
	var isManualVAT bool
	if tt.EntityLink != "INVOICE" && tt.EntityLink != "BILL" {
		vatRateID, vatValueMinor, isManualVAT, err = s.resolveExplanationVAT(ctx, req.VATRateID, req.VATAmount, grossMinor)
		if err != nil {
			return zero, err
		}
	}

	return banking.CreateExplanationParams{
		OrganisationID:        orgID,
		BankTransactionID:     txn.ID,
		CreatedByUserID:       pgtype.UUID{Bytes: authUserID, Valid: true},
		DatedOn:               dated,
		Description:           kernel.NullText(req.Description),
		Type:                  typeCode,
		GrossValueMinor:       grossMinor,
		CategoryID:            categoryID,
		SalesTaxStatus:        "TAXABLE",
		SalesTaxRateID:        vatRateID,
		SalesTaxValueMinor:    vatValueMinor,
		IsManualSalesTax:      isManualVAT,
		TransferBankAccountID: transferAccountID,
		PaidUserID:            paidUserID,
		PaidInvoiceID:         paidInvoiceID,
		PaidBillID:            paidBillID,
		MarkedForReview:       false,
		// FX (invoice receipt only; zero-valued NULLs for every other type).
		Currency:            fxv.currency,
		ExchangeRate:        fxv.exchangeRate,
		BaseValueMinor:      fxv.baseValueMinor,
		SettledInvoiceMinor: fxv.settledInvoiceMinor,
	}, nil
}

// requireOfferedCategory validates category_id is present, in the org, and OFFERED
// for the chosen type (per the transaction_type_categories mapping + company_type).
func (s *Service) requireOfferedCategory(ctx context.Context, orgID uuid.UUID, typeCode string, categoryID *string) (pgtype.UUID, error) {
	if categoryID == nil || strings.TrimSpace(*categoryID) == "" {
		return pgtype.UUID{}, kernel.ErrValidation("category_id is required for this type", nil)
	}
	cID, err := uuid.Parse(strings.TrimSpace(*categoryID))
	if err != nil {
		return pgtype.UUID{}, kernel.ErrValidation("category_id is not a valid UUID", err)
	}
	offered, err := s.catQueries.CategoryOfferedForType(ctx, categoriesdb.CategoryOfferedForTypeParams{
		OrganisationID:      orgID,
		TransactionTypeCode: typeCode,
		CompanyType:         s.orgCompanyType(ctx, orgID),
		CategoryID:          cID,
	})
	if err != nil {
		return pgtype.UUID{}, kernel.ErrInternal(err)
	}
	if !offered {
		return pgtype.UUID{}, kernel.ErrValidation("that category isn't valid for the chosen type", nil)
	}
	return pgtype.UUID{Bytes: cID, Valid: true}, nil
}

// resolveTransferAccount validates the transfer target is another live account in
// the org (not this one).
func (s *Service) resolveTransferAccount(ctx context.Context, orgID, accountUUID uuid.UUID, transferID *string) (pgtype.UUID, error) {
	if transferID == nil || strings.TrimSpace(*transferID) == "" {
		return pgtype.UUID{}, kernel.ErrValidation("transfer_bank_account_id is required for a transfer", nil)
	}
	taID, err := uuid.Parse(strings.TrimSpace(*transferID))
	if err != nil {
		return pgtype.UUID{}, kernel.ErrValidation("transfer_bank_account_id is not a valid UUID", err)
	}
	if taID == accountUUID {
		return pgtype.UUID{}, kernel.ErrValidation("can't transfer to the same account", nil)
	}
	if _, err := s.queries.GetBankAccount(ctx, banking.GetBankAccountParams{ID: taID, OrganisationID: orgID}); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return pgtype.UUID{}, kernel.ErrValidation("transfer account not found", nil)
		}
		return pgtype.UUID{}, kernel.ErrInternal(err)
	}
	return pgtype.UUID{Bytes: taID, Valid: true}, nil
}

// resolveUser validates the paid user is an active member of the org.
func (s *Service) resolveUser(ctx context.Context, orgID uuid.UUID, userID *string) (pgtype.UUID, error) {
	if userID == nil || strings.TrimSpace(*userID) == "" {
		return pgtype.UUID{}, kernel.ErrValidation("paid_user_id is required for a user payment", nil)
	}
	uID, err := uuid.Parse(strings.TrimSpace(*userID))
	if err != nil {
		return pgtype.UUID{}, kernel.ErrValidation("paid_user_id is not a valid UUID", err)
	}
	if _, err := kernel.AuthorizeMember(ctx, s.authQueries, uID, orgID); err != nil {
		return pgtype.UUID{}, kernel.ErrValidation("the chosen user isn't an active member of this organisation", nil)
	}
	return pgtype.UUID{Bytes: uID, Valid: true}, nil
}

// receiptFX holds the foreign-currency view of an invoice-receipt portion, stored on the
// explanation so the GL re-post and paid_value sync are currency-coherent. All NULL on a
// home-currency receipt is NOT the case here — we set currency/base/settled even for a
// home receipt (rate == 1, settled == base == gross) so the re-post reads them uniformly;
// only exchange_rate is NULL when the bank account is the home currency.
type receiptFX struct {
	currency            pgtype.Text    // the bank account's currency
	exchangeRate        pgtype.Numeric // home per 1 unit of currency; NULL when currency == home
	baseValueMinor      pgtype.Int8    // gross in HOME at the receipt-date rate (B)
	settledInvoiceMinor pgtype.Int8    // gross expressed in the INVOICE's currency
}

// resolveInvoice validates the Invoice Receipt link, computes the receipt's foreign-currency
// views (so a EUR receipt against a EUR invoice paid from a GBP account stays coherent), and
// returns the invoice id + the receiptFX to store. The invoice must be a live SENT invoice in
// the caller's org (cross-tenant → 422) that still owes money. The receipt portion may not
// exceed the invoice's OUTSTANDING balance IN THE INVOICE'S CURRENCY — overpayment is rejected
// (split the bank line instead). On an EDIT, this explanation's own prior portion (in invoice
// currency) against the SAME invoice is given back to the outstanding first.
func (s *Service) resolveInvoice(ctx context.Context, orgID, accountUUID uuid.UUID, invoiceID *string, grossMinor int64, dated pgtype.Date, existing *banking.BankTransactionExplanation) (pgtype.UUID, receiptFX, error) {
	var zfx receiptFX
	if invoiceID == nil || strings.TrimSpace(*invoiceID) == "" {
		return pgtype.UUID{}, zfx, kernel.ErrValidation("paid_invoice_id is required for an invoice receipt", nil)
	}
	invUUID, err := uuid.Parse(strings.TrimSpace(*invoiceID))
	if err != nil {
		return pgtype.UUID{}, zfx, kernel.ErrValidation("paid_invoice_id is not a valid UUID", err)
	}
	// Org-scoped fetch (soft-delete aware): a cross-tenant or missing id returns no row.
	inv, err := s.invoiceQueries.GetInvoice(ctx, invoicesdb.GetInvoiceParams{ID: invUUID, OrganisationID: orgID})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return pgtype.UUID{}, zfx, kernel.ErrValidation("invoice not found", nil)
		}
		return pgtype.UUID{}, zfx, kernel.ErrInternal(err)
	}
	if inv.Status != invoiceStatusSent {
		return pgtype.UUID{}, zfx, kernel.ErrValidation("only a sent invoice can receive a payment", nil)
	}

	// The bank line is in its account's currency; the invoice + its outstanding balance are
	// in the invoice's currency. Convert this portion into the invoice currency (and home)
	// at the receipt-date rate so the overpayment cap + paid_value compare like with like.
	acct, err := s.queries.GetBankAccount(ctx, banking.GetBankAccountParams{ID: accountUUID, OrganisationID: orgID})
	if err != nil {
		return pgtype.UUID{}, zfx, kernel.ErrInternal(err)
	}
	org, err := s.authQueries.GetOrganisation(ctx, orgID)
	if err != nil {
		return pgtype.UUID{}, zfx, kernel.ErrInternal(err)
	}
	fxv, err := s.computeReceiptFX(ctx, grossMinor, dated, acct.Currency, inv.Currency, org.NativeCurrency)
	if err != nil {
		return pgtype.UUID{}, zfx, err
	}
	settled := fxv.settledInvoiceMinor.Int64 // this portion in the invoice's currency

	link := pgtype.UUID{Bytes: invUUID, Valid: true}
	// Outstanding = total - paid, both in the INVOICE'S currency.
	outstanding := inv.TotalValueMinor - inv.PaidValueMinor
	if existing != nil && existing.Type == typeInvoiceReceipt && existing.PaidInvoiceID == link {
		// editing the same invoice: give the old portion (invoice ccy) back first
		outstanding += settledInvoiceOf(existing)
	}
	if outstanding <= 0 {
		return pgtype.UUID{}, zfx, kernel.ErrValidation("that invoice is already fully paid", nil)
	}
	if settled > outstanding {
		return pgtype.UUID{}, zfx, kernel.ErrValidation("that's more than the invoice's outstanding balance — split the transaction instead", nil)
	}
	return link, fxv, nil
}

// settledInvoiceOf returns an existing explanation's portion expressed in the invoice's
// currency: settled_invoice_minor when set, else gross_value_minor (a home/same-currency
// receipt written before this column existed, or one where bank == invoice currency).
func settledInvoiceOf(e *banking.BankTransactionExplanation) int64 {
	if e.SettledInvoiceMinor.Valid {
		return e.SettledInvoiceMinor.Int64
	}
	return e.GrossValueMinor
}

// computeReceiptFX converts a receipt portion (grossMinor, in the BANK account's currency)
// into its HOME value (base_value_minor) and its INVOICE-currency value
// (settled_invoice_minor), using the stored rates on the receipt date. Rates are home per 1
// unit of the currency (home itself = 1). A foreign leg with no stored rate → clean 422.
func (s *Service) computeReceiptFX(ctx context.Context, grossMinor int64, dated pgtype.Date, bankCcy, invoiceCcy, homeCcy string) (receiptFX, error) {
	bankExp, err := s.currencyExp(ctx, bankCcy)
	if err != nil {
		return receiptFX{}, err
	}
	invoiceExp, err := s.currencyExp(ctx, invoiceCcy)
	if err != nil {
		return receiptFX{}, err
	}
	homeExp, err := s.currencyExp(ctx, homeCcy)
	if err != nil {
		return receiptFX{}, err
	}

	one := decimal.NewFromInt(1)
	bankIsHome := strings.EqualFold(bankCcy, homeCcy)
	rateBank := one
	if !bankIsHome {
		rateBank, err = s.rateFor(ctx, bankCcy, dated)
		if err != nil {
			return receiptFX{}, err
		}
	}
	rateInvoice := one
	if !strings.EqualFold(invoiceCcy, homeCcy) {
		rateInvoice, err = s.rateFor(ctx, invoiceCcy, dated)
		if err != nil {
			return receiptFX{}, err
		}
	}

	base := fx.ConvertVia(grossMinor, bankExp, homeExp, rateBank, one)               // bank → home
	settled := fx.ConvertVia(grossMinor, bankExp, invoiceExp, rateBank, rateInvoice) // bank → invoice

	out := receiptFX{
		currency:            pgtype.Text{String: strings.ToUpper(strings.TrimSpace(bankCcy)), Valid: true},
		baseValueMinor:      pgtype.Int8{Int64: base, Valid: true},
		settledInvoiceMinor: pgtype.Int8{Int64: settled, Valid: true},
	}
	if !bankIsHome {
		out.exchangeRate = numericFromDecimal(rateBank)
	}
	return out, nil
}

// rateFor returns the stored rate (home per 1 unit of ccy) on/before `dated`. A missing
// rate source or no stored rate is a clean validation error, not a 500.
func (s *Service) rateFor(ctx context.Context, ccy string, dated pgtype.Date) (decimal.Decimal, error) {
	if s.rates == nil {
		return decimal.Decimal{}, kernel.ErrValidation("no exchange-rate source is configured, so a foreign-currency receipt can't be settled", nil)
	}
	r, ok, err := s.rates.RateOnOrBefore(ctx, ccy, dated.Time)
	if err != nil {
		return decimal.Decimal{}, kernel.ErrInternal(err)
	}
	if !ok {
		return decimal.Decimal{}, kernel.ErrValidation("no exchange rate available for "+ccy+" on "+dated.Time.Format("2006-01-02"), nil)
	}
	return r, nil
}

// currencyExp returns a currency's minor_unit (decimal places), via the invoices query set
// (the currencies table is global). Used to size FX conversions.
func (s *Service) currencyExp(ctx context.Context, code string) (int, error) {
	c, err := s.invoiceQueries.GetCurrency(ctx, strings.ToUpper(strings.TrimSpace(code)))
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return 0, kernel.ErrValidation("unknown currency "+code, nil)
		}
		return 0, kernel.ErrInternal(err)
	}
	return int(c.MinorUnit), nil
}

// numericFromDecimal maps a decimal to pgtype.Numeric for storage in exchange_rate
// (NUMERIC(18,6)); an unparseable value becomes SQL NULL rather than erroring.
func numericFromDecimal(d decimal.Decimal) pgtype.Numeric {
	var n pgtype.Numeric
	if err := n.Scan(d.String()); err != nil {
		return pgtype.Numeric{}
	}
	return n
}

// resolveBill validates the Bill Payment link and returns the bill's id to store. The
// bill must be a live bill in the caller's org (cross-tenant → 422) that still owes
// money. Unlike invoices there is NO status check (bills have no lifecycle). The payment
// portion may not exceed the bill's OUTSTANDING balance — overpayment is rejected (split
// the bank line instead). On an EDIT, this explanation's own prior portion against the
// SAME bill is given back to the outstanding first. NB grossMinor is NEGATIVE here (a
// money-out line), so we compare its MAGNITUDE (absInt64) to the outstanding.
func (s *Service) resolveBill(ctx context.Context, orgID uuid.UUID, billID *string, grossMinor int64, existing *banking.BankTransactionExplanation) (pgtype.UUID, error) {
	if billID == nil || strings.TrimSpace(*billID) == "" {
		return pgtype.UUID{}, kernel.ErrValidation("paid_bill_id is required for a bill payment", nil)
	}
	bUUID, err := uuid.Parse(strings.TrimSpace(*billID))
	if err != nil {
		return pgtype.UUID{}, kernel.ErrValidation("paid_bill_id is not a valid UUID", err)
	}
	// Org-scoped fetch (soft-delete aware): a cross-tenant or missing id returns no row.
	bill, err := s.billQueries.GetBill(ctx, billsdb.GetBillParams{ID: bUUID, OrganisationID: orgID})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return pgtype.UUID{}, kernel.ErrValidation("bill not found", nil)
		}
		return pgtype.UUID{}, kernel.ErrInternal(err)
	}

	link := pgtype.UUID{Bytes: bUUID, Valid: true}
	// Outstanding = total - paid (what the generated due_value_minor column holds).
	outstanding := bill.TotalValueMinor - bill.PaidValueMinor
	if existing != nil && existing.Type == typeBillPayment && existing.PaidBillID == link {
		outstanding += absInt64(existing.GrossValueMinor) // editing the same bill: give the old portion back
	}
	if outstanding <= 0 {
		return pgtype.UUID{}, kernel.ErrValidation("that bill is already fully paid", nil)
	}
	if absInt64(grossMinor) > outstanding {
		return pgtype.UUID{}, kernel.ErrValidation("that's more than the bill's outstanding balance — split the transaction instead", nil)
	}
	return link, nil
}

// resolveExplanationVAT turns an optional vat_rate_id (+ optional vat_amount) into the
// stored rate id, VAT value, and the is_manual flag — mirroring expenses.resolveVAT:
//   - no rate              → (NULL, 0, false)
//   - fixed-ratio rate     → VAT EXTRACTED from the inclusive gross (money.ComputeFixedVAT);
//     any client amount is ignored; is_manual = false
//   - non-fixed-ratio rate → value = the client vat_amount (0 if omitted); is_manual = true
func (s *Service) resolveExplanationVAT(ctx context.Context, vatRateID, vatAmount *string, grossMinor int64) (pgtype.UUID, int64, bool, error) {
	if vatRateID == nil || strings.TrimSpace(*vatRateID) == "" {
		return pgtype.UUID{}, 0, false, nil
	}
	rID, err := uuid.Parse(strings.TrimSpace(*vatRateID))
	if err != nil {
		return pgtype.UUID{}, 0, false, kernel.ErrValidation("vat_rate_id is not a valid UUID", err)
	}
	rate, err := s.catQueries.GetVatRate(ctx, rID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return pgtype.UUID{}, 0, false, kernel.ErrValidation("vat_rate_id not found", nil)
		}
		return pgtype.UUID{}, 0, false, kernel.ErrInternal(err)
	}
	id := pgtype.UUID{Bytes: rID, Valid: true}

	if rate.IsFixedRatio {
		// Rate-locked: extract the VAT from the inclusive gross; ignore any client amount.
		// A single explanation's gross fits int32 (the £21.4m ceiling); clamp as a guard.
		vat := money.ComputeFixedVAT(money.ClampToInt32(absInt64(grossMinor)), rate.RateBps)
		return id, int64(vat), false, nil
	}

	// Manual (non-fixed) rate: store the client-entered amount as-is (0 if omitted).
	var valueMinor int64
	if vatAmount != nil && strings.TrimSpace(*vatAmount) != "" {
		minor, err := money.PoundsToMinor(strings.TrimSpace(*vatAmount))
		if err != nil {
			return pgtype.UUID{}, 0, false, kernel.ErrValidation("vat_amount is not a valid amount", err)
		}
		valueMinor = minor
	}
	return id, valueMinor, true, nil
}

// orgCompanyType reads the org's company_type (” if unset → only the 'ALL'
// mapping rows match). Best-effort.
func (s *Service) orgCompanyType(ctx context.Context, orgID uuid.UUID) string {
	org, err := s.authQueries.GetOrganisation(ctx, orgID)
	if err != nil || !org.CompanyType.Valid {
		return ""
	}
	return org.CompanyType.String
}

// resyncInvoicePaid recomputes ONE invoice's paid_value_minor = Σ(its live
// INVOICE_RECEIPT explanations) and writes it via UpdateInvoicePaidValue, inside the
// caller's transaction (qtx/itx are the banking + invoices query sets bound to that
// tx). A no-op when invoiceID is NULL (the explanation isn't an invoice receipt).
// Recomputing from the Σ — rather than incrementing — keeps the figure drift-free
// across split / edit / re-point / delete. Tolerates a vanished invoice (concurrently
// soft-deleted) so an unrelated edit can't fail on it.
func (s *Service) resyncInvoicePaid(ctx context.Context, qtx *banking.Queries, itx *invoicesdb.Queries, orgID uuid.UUID, invoiceID pgtype.UUID) error {
	if !invoiceID.Valid {
		return nil
	}
	sum, err := qtx.SumInvoiceReceiptsForInvoice(ctx, banking.SumInvoiceReceiptsForInvoiceParams{
		PaidInvoiceID:  invoiceID,
		OrganisationID: orgID,
	})
	if err != nil {
		return kernel.ErrInternal(err)
	}
	invUUID, err := uuid.FromBytes(invoiceID.Bytes[:])
	if err != nil {
		return kernel.ErrInternal(err)
	}
	if _, err := itx.UpdateInvoicePaidValue(ctx, invoicesdb.UpdateInvoicePaidValueParams{
		ID:             invUUID,
		OrganisationID: orgID,
		PaidValueMinor: sum,
	}); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil // invoice no longer live — nothing to update
		}
		return kernel.ErrInternal(err)
	}
	return nil
}

// resyncBillPaid recomputes ONE bill's paid_value_minor = Σ(its live BILL_PAYMENT
// explanations) and writes it via UpdateBillPaidValue, inside the caller's transaction
// (qtx/btx are the banking + bills query sets bound to that tx). The money-out gross is
// negative, so SumBillPaymentsForBill NEGATES it → a POSITIVE paid value. A no-op when
// billID is NULL (the explanation isn't a bill payment). Recomputing from the Σ — rather
// than incrementing — keeps the figure drift-free across split/edit/re-point/delete.
// Tolerates a vanished bill (concurrently soft-deleted).
func (s *Service) resyncBillPaid(ctx context.Context, qtx *banking.Queries, btx *billsdb.Queries, orgID uuid.UUID, billID pgtype.UUID) error {
	if !billID.Valid {
		return nil
	}
	sum, err := qtx.SumBillPaymentsForBill(ctx, banking.SumBillPaymentsForBillParams{
		PaidBillID:     billID,
		OrganisationID: orgID,
	})
	if err != nil {
		return kernel.ErrInternal(err)
	}
	bUUID, err := uuid.FromBytes(billID.Bytes[:])
	if err != nil {
		return kernel.ErrInternal(err)
	}
	if _, err := btx.UpdateBillPaidValue(ctx, billsdb.UpdateBillPaidValueParams{
		ID:             bUUID,
		OrganisationID: orgID,
		PaidValueMinor: sum,
	}); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil // bill no longer live — nothing to update
		}
		return kernel.ErrInternal(err)
	}
	return nil
}

// =============================================================================
// LOADERS / RESPONSE
// =============================================================================

// loadTransactionForAccount fetches a transaction org-scoped (404) and enforces it
// belongs to the given account (404). Unlike loadManualTransaction, ANY line may be
// explained (feed/statement lines too).
func (s *Service) loadTransactionForAccount(ctx context.Context, orgID, accountUUID, txnUUID uuid.UUID) (banking.BankTransaction, error) {
	t, err := s.queries.GetBankTransaction(ctx, banking.GetBankTransactionParams{ID: txnUUID, OrganisationID: orgID})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return banking.BankTransaction{}, kernel.ErrNotFound("bank transaction", txnUUID.String())
		}
		return banking.BankTransaction{}, kernel.ErrInternal(err)
	}
	if t.BankAccountID != accountUUID {
		return banking.BankTransaction{}, kernel.ErrNotFound("bank transaction", txnUUID.String())
	}
	return t, nil
}

// loadExplanationForTxn fetches an explanation org-scoped (404) and enforces it
// belongs to the given transaction (404).
func (s *Service) loadExplanationForTxn(ctx context.Context, orgID, txnUUID, explUUID uuid.UUID) (banking.BankTransactionExplanation, error) {
	e, err := s.queries.GetExplanation(ctx, banking.GetExplanationParams{ID: explUUID, OrganisationID: orgID})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return banking.BankTransactionExplanation{}, kernel.ErrNotFound("explanation", explUUID.String())
		}
		return banking.BankTransactionExplanation{}, kernel.ErrInternal(err)
	}
	if e.BankTransactionID != txnUUID {
		return banking.BankTransactionExplanation{}, kernel.ErrNotFound("explanation", explUUID.String())
	}
	return e, nil
}

// buildExplanationsResponse loads the (recomputed) transaction + its detailed
// explanations into the API shape. Shared by the read + every mutation.
func (s *Service) buildExplanationsResponse(ctx context.Context, orgID, txnUUID uuid.UUID) (*TransactionExplanationsResponse, error) {
	txn, err := s.queries.GetBankTransaction(ctx, banking.GetBankTransactionParams{ID: txnUUID, OrganisationID: orgID})
	if err != nil {
		return nil, kernel.ErrInternal(err)
	}
	rows, err := s.queries.ListExplanationsForTransactionDetailed(ctx, banking.ListExplanationsForTransactionDetailedParams{
		BankTransactionID: txnUUID,
		OrganisationID:    orgID,
	})
	if err != nil {
		return nil, kernel.ErrInternal(err)
	}
	out := make([]*ExplanationResponse, 0, len(rows))
	for _, r := range rows {
		out = append(out, explanationRowToResponse(r))
	}
	unexplained := txn.AmountMinor
	if txn.UnexplainedAmountMinor.Valid {
		unexplained = txn.UnexplainedAmountMinor.Int64
	}
	return &TransactionExplanationsResponse{
		TransactionID:     txn.ID.String(),
		Status:            txn.Status,
		UnexplainedAmount: money.MinorToPounds(unexplained),
		Explanations:      out,
	}, nil
}

// explanationRowToResponse maps one detailed (JOINed) row into the API shape. The
// stored gross is signed; the response exposes its positive magnitude.
func explanationRowToResponse(r banking.ListExplanationsForTransactionDetailedRow) *ExplanationResponse {
	resp := &ExplanationResponse{
		ID:                  r.ID.String(),
		Type:                r.Type,
		Amount:              money.MinorToPounds(absInt64(r.GrossValueMinor)),
		CategoryName:        kernel.NullTextToPtr(r.CategoryName),
		CategoryNominalCode: kernel.NullTextToPtr(r.CategoryNominalCode),
		TransferAccountName: kernel.NullTextToPtr(r.TransferAccountName),
		VATValue:            money.MinorToPounds(r.SalesTaxValueMinor),
		Description:         kernel.NullTextToPtr(r.Description),
		DatedOn:             r.DatedOn.Time.Format("2006-01-02"),
		MarkedForReview:     r.MarkedForReview,
	}
	resp.CategoryID = uuidPtr(r.CategoryID)
	resp.TransferBankAccountID = uuidPtr(r.TransferBankAccountID)
	if r.PaidUserID.Valid {
		resp.PaidUserID = uuidPtr(r.PaidUserID)
		resp.PaidUserName = fullNamePtr(r.PaidUserFirstName, r.PaidUserLastName)
	}
	if r.PaidInvoiceID.Valid {
		resp.PaidInvoiceID = uuidPtr(r.PaidInvoiceID)
		resp.InvoiceReference = kernel.NullTextToPtr(r.InvoiceReference)
	}
	if r.PaidBillID.Valid {
		resp.PaidBillID = uuidPtr(r.PaidBillID)
		resp.BillReference = kernel.NullTextToPtr(r.BillReference)
	}
	resp.VATRateID = uuidPtr(r.SalesTaxRateID)
	if r.VatRateBps.Valid {
		s := money.BpsToPercent(r.VatRateBps.Int32)
		resp.VATRate = &s
	}
	return resp
}

// =============================================================================
// SMALL HELPERS
// =============================================================================

func absInt64(v int64) int64 {
	if v < 0 {
		return -v
	}
	return v
}

// uuidPtr renders a nullable pgtype.UUID as an optional string (nil when NULL).
func uuidPtr(u pgtype.UUID) *string {
	if !u.Valid {
		return nil
	}
	id, err := uuid.FromBytes(u.Bytes[:])
	if err != nil {
		return nil
	}
	s := id.String()
	return &s
}

// fullNamePtr joins a user's first/last name; nil when both are blank.
func fullNamePtr(first, last pgtype.Text) *string {
	name := strings.TrimSpace(strings.TrimSpace(first.String) + " " + strings.TrimSpace(last.String))
	if name == "" {
		return nil
	}
	return &name
}
