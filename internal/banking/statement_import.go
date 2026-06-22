package banking

// statement_import.go
// =============================================================================
// CSV statement import: bulk-add bank transactions from an uploaded file.
//
// v1 is a FIXED-TEMPLATE CSV (no per-bank column mapping, no dependency — stdlib
// encoding/csv + crypto/sha256). Columns are matched by HEADER NAME (order-free):
//   date         required, DD/MM/YYYY
//   description  required
//   money_in     decimal pounds — set on a credit (money in)
//   money_out    decimal pounds — set on a debit (money out)
//   bank_memo    optional raw bank narrative
// Exactly one of money_in / money_out must be a positive amount per row; the
// service signs it into amount_minor (+ in / - out).
//
// Imported rows are source='statement', status='unexplained', transaction_type
// defaulted by sign, created_by = the uploader. DEDUPE: each line gets a stable
// external_id ("csv:" + sha256 over date|amount|description|occurrence), so the
// existing (bank_account_id, external_id) unique index makes re-importing the same
// file idempotent. The occurrence counter keeps within-file identical lines distinct.
//
// Validation is ALL-OR-NOTHING: any bad row fails the whole import with a 422
// (nothing is inserted) — re-upload after fixing is safe thanks to the dedupe.
// =============================================================================

import (
	"context"
	"crypto/sha256"
	"encoding/csv"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	banking "github.com/operationfb/accounting-saas/db/banking"
	"github.com/operationfb/accounting-saas/internal/kernel"
	"github.com/operationfb/accounting-saas/money"
)

// maxStatementUploadBytes caps the upload before parsing (statements are small).
const maxStatementUploadBytes = 5 << 20 // 5 MiB

// StatementImportResponse reports the import outcome plus the refreshed statement.
type StatementImportResponse struct {
	Imported          int                        `json:"imported"`
	SkippedDuplicates int                        `json:"skipped_duplicates"`
	Total             int                        `json:"total"`
	Account           *BankAccountResponse       `json:"account"`
	Transactions      []*BankTransactionResponse `json:"transactions"`
}

// parsedLine is one validated CSV row, ready to insert.
type parsedLine struct {
	DatedOn         pgtype.Date
	AmountMinor     int64 // signed: + money in, - money out
	Description     string
	BankMemo        *string
	ExternalID      string // stable dedupe key
	TransactionType string // CREDIT | DEBIT
}

// ImportStatement parses an uploaded CSV, dedupes against the account's existing
// imported lines, and inserts the new ones (owner/admin). Returns the counts plus
// the refreshed statement. Atomic: a single bad row fails the whole import.
func (s *Service) ImportStatement(ctx context.Context, authUserID, authOrgID uuid.UUID, accountID string, content io.Reader) (*StatementImportResponse, error) {
	accountUUID, err := uuid.Parse(accountID)
	if err != nil {
		return nil, kernel.ErrValidation("id is not a valid UUID", err)
	}
	if err := s.requireAdmin(ctx, authUserID, authOrgID); err != nil {
		return nil, err
	}
	// 404 if the account isn't in this org.
	if _, err := s.queries.GetBankAccount(ctx, banking.GetBankAccountParams{ID: accountUUID, OrganisationID: authOrgID}); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, kernel.ErrNotFound("bank account", accountID)
		}
		return nil, kernel.ErrInternal(err)
	}

	lines, err := parseStatementCSV(content)
	if err != nil {
		return nil, err
	}

	// Fetch the account's existing dedupe keys once.
	existingIDs, err := s.queries.ListBankTransactionExternalIDs(ctx, accountUUID)
	if err != nil {
		return nil, kernel.ErrInternal(err)
	}
	seen := make(map[string]struct{}, len(existingIDs)+len(lines))
	for _, e := range existingIDs {
		if e.Valid {
			seen[e.String] = struct{}{}
		}
	}

	imported, skipped := 0, 0
	err = kernel.WithTx(ctx, s.pool, func(tx pgx.Tx) error {
		qtx := s.queries.WithTx(tx)
		for _, ln := range lines {
			if _, dup := seen[ln.ExternalID]; dup {
				skipped++
				continue
			}
			if _, e := qtx.CreateBankTransaction(ctx, banking.CreateBankTransactionParams{
				OrganisationID:  authOrgID,
				BankAccountID:   accountUUID,
				CreatedByUserID: pgtype.UUID{Bytes: authUserID, Valid: true},
				DatedOn:         ln.DatedOn,
				AmountMinor:     ln.AmountMinor,
				Description:     pgtype.Text{String: ln.Description, Valid: true},
				BankMemo:        kernel.NullText(ln.BankMemo),
				Status:          "unexplained",
				Source:          "statement",
				ExternalID:      pgtype.Text{String: ln.ExternalID, Valid: true},
				TransactionType: pgtype.Text{String: ln.TransactionType, Valid: true},
			}); e != nil {
				return e
			}
			seen[ln.ExternalID] = struct{}{} // guard against a within-batch repeat
			imported++
		}
		return nil
	})
	if err != nil {
		return nil, kernel.ErrInternal(err)
	}

	statement, err := s.buildStatement(ctx, authOrgID, accountUUID)
	if err != nil {
		return nil, err
	}
	return &StatementImportResponse{
		Imported:          imported,
		SkippedDuplicates: skipped,
		Total:             len(lines),
		Account:           statement.Account,
		Transactions:      statement.Transactions,
	}, nil
}

// parseStatementCSV reads + validates the whole file. On any invalid row it returns
// a single 422 naming the first offending rows (and inserts nothing).
func parseStatementCSV(r io.Reader) ([]parsedLine, error) {
	cr := csv.NewReader(r)
	cr.FieldsPerRecord = -1 // tolerate ragged rows; we index by header name
	cr.TrimLeadingSpace = true
	records, err := cr.ReadAll()
	if err != nil {
		return nil, kernel.ErrValidation("could not read the CSV file", err)
	}
	if len(records) == 0 {
		return nil, kernel.ErrValidation("the CSV file is empty", nil)
	}

	// Header → column index (BOM-stripped, lower-cased, trimmed).
	bom := string(rune(0xFEFF)) // some exporters prepend a UTF-8 byte-order mark
	col := map[string]int{}
	for i, h := range records[0] {
		key := strings.ToLower(strings.TrimSpace(strings.TrimPrefix(h, bom)))
		col[key] = i
	}
	for _, required := range []string{"date", "description", "money_in", "money_out"} {
		if _, ok := col[required]; !ok {
			return nil, kernel.ErrValidation(fmt.Sprintf(
				"CSV is missing the required column %q (expected: date, description, money_in, money_out, and optionally bank_memo)",
				required), nil)
		}
	}
	field := func(rec []string, name string) string {
		idx, ok := col[name]
		if !ok || idx >= len(rec) {
			return ""
		}
		return strings.TrimSpace(rec[idx])
	}

	out := make([]parsedLine, 0, len(records)-1)
	var rowErrs []string
	occ := map[string]int{} // occurrence counter per (date|amount|description)

	for i, rec := range records[1:] {
		rowNum := i + 2 // 1-based incl. the header row
		if isBlankRecord(rec) {
			continue
		}

		dateStr := field(rec, "date")
		desc := field(rec, "description")
		memo := field(rec, "bank_memo")

		t, derr := time.Parse("02/01/2006", dateStr)
		switch {
		case dateStr == "":
			rowErrs = append(rowErrs, fmt.Sprintf("row %d: missing date", rowNum))
			continue
		case derr != nil:
			rowErrs = append(rowErrs, fmt.Sprintf("row %d: date %q must be DD/MM/YYYY", rowNum, dateStr))
			continue
		}
		if desc == "" {
			rowErrs = append(rowErrs, fmt.Sprintf("row %d: missing description", rowNum))
			continue
		}

		inMinor, inErr := parseMoneyField(field(rec, "money_in"))
		outMinor, outErr := parseMoneyField(field(rec, "money_out"))
		if inErr != nil || outErr != nil {
			rowErrs = append(rowErrs, fmt.Sprintf("row %d: money_in/money_out must be a valid positive amount", rowNum))
			continue
		}
		hasIn, hasOut := inMinor > 0, outMinor > 0
		if hasIn == hasOut { // both set, or neither
			rowErrs = append(rowErrs, fmt.Sprintf("row %d: provide exactly one of money_in or money_out", rowNum))
			continue
		}
		amountMinor := inMinor
		if hasOut {
			amountMinor = -outMinor
		}

		// Stable dedupe id; occurrence counter distinguishes identical lines in one file.
		base := fmt.Sprintf("%s\x1f%d\x1f%s", t.Format("2006-01-02"), amountMinor, desc)
		k := occ[base]
		occ[base] = k + 1
		sum := sha256.Sum256([]byte(fmt.Sprintf("%s\x1f%d", base, k)))

		var memoPtr *string
		if memo != "" {
			memoPtr = &memo
		}
		out = append(out, parsedLine{
			DatedOn:         pgtype.Date{Time: t, Valid: true},
			AmountMinor:     amountMinor,
			Description:     desc,
			BankMemo:        memoPtr,
			ExternalID:      "csv:" + hex.EncodeToString(sum[:]),
			TransactionType: transactionTypeForSign(amountMinor),
		})
	}

	if len(rowErrs) > 0 {
		msg := "the CSV has invalid rows: " + strings.Join(firstN(rowErrs, 5), "; ")
		if len(rowErrs) > 5 {
			msg += fmt.Sprintf(" (and %d more)", len(rowErrs)-5)
		}
		return nil, kernel.ErrValidation(msg, nil)
	}
	return out, nil
}

// parseMoneyField parses an optional pounds field → minor units. Empty → 0 (no
// error). Tolerates £, commas and spaces. Rejects negatives (use the other column).
func parseMoneyField(s string) (int64, error) {
	s = strings.NewReplacer("£", "", ",", "", " ", "").Replace(strings.TrimSpace(s))
	if s == "" {
		return 0, nil
	}
	minor, err := money.PoundsToMinor(s)
	if err != nil {
		return 0, err
	}
	if minor < 0 {
		return 0, errors.New("negative amount")
	}
	return minor, nil
}

func isBlankRecord(rec []string) bool {
	for _, f := range rec {
		if strings.TrimSpace(f) != "" {
			return false
		}
	}
	return true
}

func firstN(s []string, n int) []string {
	if len(s) > n {
		return s[:n]
	}
	return s
}
