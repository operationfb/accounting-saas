package banking

// statement_import.go
// =============================================================================
// Statement import: bulk-add bank transactions from an uploaded file. Two formats
// are accepted on the SAME endpoint, auto-detected from the file's contents
// (looksLikeOFX): a fixed-template CSV (below) and OFX (the format banks export —
// see the OFX section at the bottom of this file). Both parse into the same
// []parsedLine and flow through the one insert path in ImportStatement.
//
// The CSV is a FIXED TEMPLATE (no per-bank column mapping, no dependency — stdlib
// encoding/csv + crypto/sha256). Columns are matched by HEADER NAME (order-free):
//   date         required, DD/MM/YYYY
//   description  required
//   amount       required, SIGNED decimal pounds — positive = money in, negative
//                (leading '-', e.g. -54.20) = money out. Stored straight into
//                amount_minor. Must not be empty or zero.
//   bank_memo    optional raw bank narrative
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
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/csv"
	"encoding/hex"
	"errors"
	"fmt"
	"html"
	"io"
	"regexp"
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

	// Read the (handler-capped) upload once, then pick the parser by sniffing the
	// content — so the single import endpoint serves both CSV and OFX uploads.
	raw, err := io.ReadAll(content)
	if err != nil {
		return nil, kernel.ErrValidation("could not read the uploaded file", err)
	}
	var lines []parsedLine
	if looksLikeOFX(raw) {
		lines, err = parseStatementOFX(raw)
	} else {
		lines, err = parseStatementCSV(bytes.NewReader(raw))
	}
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
	for _, required := range []string{"date", "description", "amount"} {
		if _, ok := col[required]; !ok {
			return nil, kernel.ErrValidation(fmt.Sprintf(
				"CSV is missing the required column %q (expected: date, description, amount, and optionally bank_memo)",
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

		// One SIGNED amount column: + money in, - money out. Empty/zero is rejected;
		// the sign goes straight into amount_minor.
		amountStr := field(rec, "amount")
		if amountStr == "" {
			rowErrs = append(rowErrs, fmt.Sprintf("row %d: missing amount", rowNum))
			continue
		}
		amountMinor, aerr := parseSignedAmount(amountStr)
		if aerr != nil {
			rowErrs = append(rowErrs, fmt.Sprintf("row %d: amount %q must be a number; use a leading - for money out (e.g. -54.20)", rowNum, amountStr))
			continue
		}
		if amountMinor == 0 {
			rowErrs = append(rowErrs, fmt.Sprintf("row %d: amount must not be zero", rowNum))
			continue
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

// parseSignedAmount parses the SIGNED amount column → minor units (pence). Tolerates
// £, commas and spaces, but KEEPS a leading '-' (negative = money out). Empty → 0 with
// no error; the caller treats an empty cell as a "missing amount" row error.
func parseSignedAmount(s string) (int64, error) {
	s = strings.NewReplacer("£", "", ",", "", " ", "").Replace(strings.TrimSpace(s))
	if s == "" {
		return 0, nil
	}
	return money.PoundsToMinor(s) // handles negatives: "-54.20" → -5420
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

// =============================================================================
// OFX statement import
// -----------------------------------------------------------------------------
// OFX is the format banks export ("Barclays - data.ofx"). Two wire forms exist:
//   - OFX 1.x — SGML, with UNCLOSED leaf tags (<TRNAMT>200.00 with no </TRNAMT>);
//     only aggregates like <STMTTRN>…</STMTTRN> are closed.
//   - OFX 2.x — well-formed XML (every tag closed).
// We need only a handful of fields per transaction, so rather than a full SGML/XML
// parser (or a new dependency) we scan tolerantly: read each <TAG>'s value up to the
// next '<' (ofxField). That single rule reads BOTH forms — in 2.x the closing
// "</TAG>" simply IS the next '<', so the value comes out identical.
//
// Each <STMTTRN> becomes a parsedLine and flows through the SAME insert path as the
// CSV importer. Unlike CSV, OFX's TRNAMT is already SIGNED and each transaction has
// the bank's own unique id (FITID) — so dedupe is external_id = "ofx:"+FITID, which
// the (bank_account_id, external_id) unique index makes idempotent on re-import.
//
// v1 imports every STMTTRN into the account named in the URL; the file's BANKACCTFROM
// (sort code / account number) is parsed past, not matched (see BACKLOG).
// =============================================================================

// looksLikeOFX sniffs the upload to choose the parser. An OFX file either starts with
// the SGML header keyword "OFXHEADER" (1.x) or contains the <OFX> root (1.x and 2.x);
// a CSV statement never does. Only the head is inspected, upper-cased for a
// case-insensitive match (this string is used for a boolean test, never for indexing).
func looksLikeOFX(raw []byte) bool {
	head := raw
	if len(head) > 1024 {
		head = head[:1024]
	}
	h := strings.ToUpper(string(head))
	return strings.HasPrefix(strings.TrimSpace(h), "OFXHEADER") || strings.Contains(h, "<OFX>")
}

// validOFXTransactionType is the OFX TRNTYPE enum the bank_transactions CHECK accepts
// (db/schema/banking_schema.sql). Any code outside it — e.g. the spec's HOLD, the one
// standard type the column omits — is normalised to OTHER so an insert can't trip the
// CHECK.
var validOFXTransactionType = map[string]bool{
	"CREDIT": true, "DEBIT": true, "INT": true, "DIV": true, "FEE": true,
	"SRVCHG": true, "DEP": true, "ATM": true, "POS": true, "XFER": true,
	"CHECK": true, "PAYMENT": true, "CASH": true, "DIRECTDEP": true,
	"DIRECTDEBIT": true, "REPEATPMT": true, "OTHER": true,
}

// ofxStmtTrnRe captures the body of each <STMTTRN>…</STMTTRN> aggregate. These are
// closed even in OFX 1.x, so a non-greedy match per block is reliable. (?s) lets '.'
// span the newlines between a block's fields.
var ofxStmtTrnRe = regexp.MustCompile(`(?s)<STMTTRN>(.*?)</STMTTRN>`)

// parseStatementOFX parses an uploaded OFX statement into the same []parsedLine the
// CSV path produces. ALL-OR-NOTHING like the CSV importer: any bad <STMTTRN> fails the
// whole import with a 422 (nothing inserted) — re-upload after a fix is safe thanks to
// the FITID dedupe.
func parseStatementOFX(raw []byte) ([]parsedLine, error) {
	blocks := ofxStmtTrnRe.FindAllStringSubmatch(string(raw), -1)
	if len(blocks) == 0 {
		return nil, kernel.ErrValidation("no transactions found in the OFX file", nil)
	}

	out := make([]parsedLine, 0, len(blocks))
	var rowErrs []string
	occ := map[string]int{} // occurrence counter, only for the FITID-less fallback key

	for i, m := range blocks {
		block := m[1]
		txnNum := i + 1 // 1-based transaction index, for error messages

		// DTPOSTED is YYYYMMDDHHMMSS[tz] (e.g. 20260615000000[-5:EST]); transactions are
		// dated by day, so take the first 8 digits and ignore the time/timezone.
		dateStr := ofxField(block, "DTPOSTED")
		if len(dateStr) < 8 {
			rowErrs = append(rowErrs, fmt.Sprintf("transaction %d: missing or invalid DTPOSTED %q", txnNum, dateStr))
			continue
		}
		t, derr := time.Parse("20060102", dateStr[:8])
		if derr != nil {
			rowErrs = append(rowErrs, fmt.Sprintf("transaction %d: DTPOSTED %q is not a valid date", txnNum, dateStr))
			continue
		}

		// TRNAMT is already SIGNED (+ money in, - money out) — straight to minor units.
		amtStr := ofxField(block, "TRNAMT")
		if amtStr == "" {
			rowErrs = append(rowErrs, fmt.Sprintf("transaction %d: missing TRNAMT", txnNum))
			continue
		}
		amountMinor, aerr := money.PoundsToMinor(amtStr)
		if aerr != nil {
			rowErrs = append(rowErrs, fmt.Sprintf("transaction %d: TRNAMT %q is not a valid amount", txnNum, amtStr))
			continue
		}

		// Description = NAME (the payee/narrative), falling back to MEMO. Both carry
		// embedded tabs/padding from the bank, so collapse the whitespace.
		name := cleanOFXText(ofxField(block, "NAME"))
		memo := cleanOFXText(ofxField(block, "MEMO"))
		desc := name
		if desc == "" {
			desc = memo
		}
		if desc == "" {
			desc = "(no description)" // NAME/MEMO are optional in OFX; keep description non-empty
		}
		var memoPtr *string
		if memo != "" {
			memoPtr = &memo
		}

		// Dedupe: the bank's FITID is unique per account → idempotent re-import. If a
		// (non-compliant) export omits it, fall back to the CSV-style synthetic hash,
		// with an occurrence counter so identical lines in one file stay distinct.
		var externalID string
		if fitid := ofxField(block, "FITID"); fitid != "" {
			externalID = "ofx:" + fitid
		} else {
			base := fmt.Sprintf("%s\x1f%d\x1f%s", t.Format("2006-01-02"), amountMinor, desc)
			k := occ[base]
			occ[base] = k + 1
			sum := sha256.Sum256([]byte(fmt.Sprintf("%s\x1f%d", base, k)))
			externalID = "ofx:" + hex.EncodeToString(sum[:])
		}

		// TRNTYPE is the bank's real type code; keep it, but normalise anything outside
		// the DB CHECK set (e.g. HOLD) to OTHER.
		trnType := strings.ToUpper(ofxField(block, "TRNTYPE"))
		if !validOFXTransactionType[trnType] {
			trnType = "OTHER"
		}

		out = append(out, parsedLine{
			DatedOn:         pgtype.Date{Time: t, Valid: true},
			AmountMinor:     amountMinor,
			Description:     desc,
			BankMemo:        memoPtr,
			ExternalID:      externalID,
			TransactionType: trnType,
		})
	}

	if len(rowErrs) > 0 {
		msg := "the OFX file has invalid transactions: " + strings.Join(firstN(rowErrs, 5), "; ")
		if len(rowErrs) > 5 {
			msg += fmt.Sprintf(" (and %d more)", len(rowErrs)-5)
		}
		return nil, kernel.ErrValidation(msg, nil)
	}
	return out, nil
}

// ofxField returns the value of a leaf OFX tag inside one <STMTTRN> block. It reads
// from just after "<TAG>" up to the next '<' — the tag's value in BOTH OFX 1.x (where
// the leaf tag is unclosed and terminated by the following element) and 2.x (where the
// next '<' is the closing "</TAG>"). OFX tag names are upper-case by spec, so the match
// is case-sensitive (avoids the byte-offset hazard of upper-casing the haystack).
// Returns "" if the tag is absent; SGML entities (&amp; …) are unescaped.
func ofxField(block, tag string) string {
	open := "<" + tag + ">"
	idx := strings.Index(block, open)
	if idx < 0 {
		return ""
	}
	rest := block[idx+len(open):]
	if end := strings.IndexByte(rest, '<'); end >= 0 {
		rest = rest[:end]
	}
	return strings.TrimSpace(html.UnescapeString(rest))
}

// cleanOFXText collapses the whitespace OFX NAME/MEMO fields carry — the Barclays
// export embeds TABs and trailing spaces — into single spaces, trimmed.
func cleanOFXText(s string) string {
	return strings.Join(strings.Fields(s), " ")
}
