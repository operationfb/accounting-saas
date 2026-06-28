package banking

// statement_import.go
// =============================================================================
// Statement import: bulk-add bank transactions from an uploaded file. Two formats
// are accepted on the SAME endpoint, auto-detected from the file's contents
// (looksLikeOFX): a fixed-template CSV (below) and OFX (the format banks export —
// see the OFX section at the bottom of this file). Both parse into the same
// []parsedLine and flow through the one insert path in ImportStatement.
//
// The CSV is parsed through a ColumnMapping (statement_detect.go): which column is the
// date / description / amount, the amount SHAPE (one signed column vs a money-in/out
// pair), and the date layout. ImportStatement either takes a mapping the user CONFIRMED
// (the detect→confirm→commit flow) or, when none is passed, AUTO-DETECTS one (detectFormat)
// — so our own template (date, description, amount; signed; DD/MM/YYYY) still "just works"
// with no mapping. No new dependency (stdlib encoding/csv + crypto/sha256).
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
	Balance         pgtype.Int8 // optional running balance from the statement (NULL when absent)
	ExternalID      string      // stable dedupe key
	TransactionType string      // CREDIT | DEBIT
}

// ImportStatement parses an uploaded CSV, dedupes against the account's existing
// imported lines, and inserts the new ones (owner/admin). Returns the counts plus
// the refreshed statement. Atomic: a single bad row fails the whole import.
func (s *Service) ImportStatement(ctx context.Context, authUserID, authOrgID uuid.UUID, accountID string, content io.Reader, mapping *ColumnMapping) (*StatementImportResponse, error) {
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
	switch {
	case looksLikeOFX(raw):
		// OFX is self-describing — a confirmed CSV mapping doesn't apply, so ignore it.
		lines, err = parseStatementOFX(raw)
	case mapping != nil:
		// The user confirmed a mapping in the detect→confirm step — parse with exactly it.
		var records [][]string
		if records, err = readCSVRecords(bytes.NewReader(raw)); err == nil {
			lines, err = parseWithMapping(records, *mapping)
		}
	default:
		// No mapping: auto-detect (our own template + any unambiguous bank export).
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
				BalanceMinor:    ln.Balance,
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

// readCSVRecords reads the whole CSV into memory (statements are small + body-capped at
// 5 MiB) tolerating ragged rows, since we index by column POSITION. Strips a UTF-8 BOM
// from the first header cell so column 0 matches its synonyms (and shows cleanly in the UI).
func readCSVRecords(r io.Reader) ([][]string, error) {
	cr := csv.NewReader(r)
	cr.FieldsPerRecord = -1 // tolerate ragged rows; we index by column position
	cr.TrimLeadingSpace = true
	records, err := cr.ReadAll()
	if err != nil {
		return nil, kernel.ErrValidation("could not read the CSV file", err)
	}
	if len(records) == 0 {
		return nil, kernel.ErrValidation("the CSV file is empty", nil)
	}
	if len(records[0]) > 0 {
		records[0][0] = strings.TrimPrefix(records[0][0], bomPrefix)
	}
	return records, nil
}

// parseStatementCSV is the NO-MAPPING path: read the file, AUTO-DETECT the column mapping,
// and parse. Our own template still "just works" (detectFormat maps date/description/amount),
// and an undetectable file fails with the same 422 the old hard-coded parser gave — the
// missing-column check now lives in validateMapping (called by parseWithMapping).
func parseStatementCSV(r io.Reader) ([]parsedLine, error) {
	records, err := readCSVRecords(r)
	if err != nil {
		return nil, err
	}
	mapping, _ := detectFormat(records)
	return parseWithMapping(records, mapping)
}

// parseWithMapping applies an explicit ColumnMapping to the records (row 0 = header),
// ALL-OR-NOTHING like the original parser: any bad row fails the whole import with a 422
// naming the first offenders (nothing inserted). parseMappedRow does the per-row work.
func parseWithMapping(records [][]string, m ColumnMapping) ([]parsedLine, error) {
	if len(records) == 0 {
		return nil, kernel.ErrValidation("the CSV file is empty", nil)
	}
	if err := validateMapping(m, len(records[0])); err != nil {
		return nil, err
	}
	out := make([]parsedLine, 0, len(records)-1)
	var rowErrs []string
	occ := map[string]int{} // occurrence counter per (date|amount|description), for dedupe ids
	for i, rec := range records[1:] {
		rowNum := i + 2 // 1-based, including the header row
		if isBlankRecord(rec) {
			continue
		}
		row, err := parseMappedRow(rec, m)
		if err != nil {
			rowErrs = append(rowErrs, fmt.Sprintf("row %d: %s", rowNum, err.Error()))
			continue
		}
		out = append(out, parsedLine{
			DatedOn:         pgtype.Date{Time: row.Date, Valid: true},
			AmountMinor:     row.AmountMinor,
			Description:     row.Description,
			BankMemo:        row.BankMemo,
			Balance:         row.Balance,
			ExternalID:      syntheticExternalID("csv:", row.Date, row.AmountMinor, row.Description, occ),
			TransactionType: transactionTypeForSign(row.AmountMinor),
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

// mappedRow is one CSV row interpreted through a ColumnMapping, before its dedupe id is
// assigned (that needs the occurrence counter the all-or-nothing caller owns).
type mappedRow struct {
	Date        time.Time
	AmountMinor int64 // signed
	Description string
	BankMemo    *string
	Balance     pgtype.Int8 // optional running balance (NULL when the column is absent/blank)
}

// parseMappedRow interprets one data row through the mapping. Returns a row-level error
// (for the all-or-nothing 422 list OR the preview's per-row note) without aborting the batch.
func parseMappedRow(rec []string, m ColumnMapping) (mappedRow, error) {
	cell := func(p *int) string {
		if p == nil || *p < 0 || *p >= len(rec) {
			return ""
		}
		return strings.TrimSpace(rec[*p])
	}

	dateStr := cell(m.DateColumn)
	if dateStr == "" {
		return mappedRow{}, errors.New("missing date")
	}
	t, err := time.Parse(m.DateFormat, dateStr)
	if err != nil {
		return mappedRow{}, fmt.Errorf("date %q doesn't match the selected format", dateStr)
	}

	desc := cell(m.DescriptionColumn)
	if desc == "" {
		return mappedRow{}, errors.New("missing description")
	}

	var amountMinor int64
	switch m.AmountFormat {
	case amountFormatSigned:
		amtStr := cell(m.AmountColumn)
		if amtStr == "" {
			return mappedRow{}, errors.New("missing amount")
		}
		if amountMinor, err = parseSignedAmount(amtStr); err != nil {
			return mappedRow{}, fmt.Errorf("amount %q isn't a number", amtStr)
		}
	case amountFormatSplit:
		inMinor, ierr := parsePositiveAmount(cell(m.MoneyInColumn))
		outMinor, oerr := parsePositiveAmount(cell(m.MoneyOutColumn))
		if ierr != nil || oerr != nil {
			return mappedRow{}, errors.New("money in / money out must be a positive number")
		}
		hasIn, hasOut := inMinor > 0, outMinor > 0
		if hasIn == hasOut { // both populated, or neither
			return mappedRow{}, errors.New("provide exactly one of money in or money out")
		}
		amountMinor = inMinor
		if hasOut {
			amountMinor = -outMinor
		}
	default:
		return mappedRow{}, errors.New("invalid amount format")
	}
	if amountMinor == 0 {
		return mappedRow{}, errors.New("amount must not be zero")
	}

	var memoPtr *string
	if memo := cell(m.MemoColumn); memo != "" {
		memoPtr = &memo
	}
	// Balance is an OPTIONAL convenience column — a missing/blank/garbled cell is non-fatal
	// (we just leave balance_minor NULL), so a bad balance never blocks the whole import.
	var balance pgtype.Int8
	if balStr := cell(m.BalanceColumn); balStr != "" {
		if bal, berr := parseSignedAmount(balStr); berr == nil {
			balance = pgtype.Int8{Int64: bal, Valid: true}
		}
	}
	return mappedRow{Date: t, AmountMinor: amountMinor, Description: desc, BankMemo: memoPtr, Balance: balance}, nil
}

// parseSignedAmount parses a SIGNED amount column → minor units (pence). Tolerates £,
// commas and spaces, but KEEPS a leading '-' (negative = money out). Empty → 0 with no
// error; callers treat an empty cell as a "missing amount" row error.
func parseSignedAmount(s string) (int64, error) {
	s = strings.NewReplacer("£", "", ",", "", " ", "").Replace(strings.TrimSpace(s))
	if s == "" {
		return 0, nil
	}
	return money.PoundsToMinor(s) // handles negatives: "-54.20" → -5420
}

// parsePositiveAmount parses one side of a money-in/out pair → minor units. Empty → 0 (the
// row's other side carries the value). Tolerates £, commas and spaces; rejects a negative
// (the sign is implied by WHICH column the value sits in, not by a leading '-').
func parsePositiveAmount(s string) (int64, error) {
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

// syntheticExternalID builds the stable dedupe key for a line the bank gives no id to
// (every CSV row; an OFX row missing its FITID). The occurrence counter keeps byte-identical
// lines within one file distinct. prefix namespaces the source ("csv:" / "ofx:").
func syntheticExternalID(prefix string, t time.Time, amountMinor int64, desc string, occ map[string]int) string {
	base := fmt.Sprintf("%s\x1f%d\x1f%s", t.Format("2006-01-02"), amountMinor, desc)
	k := occ[base]
	occ[base] = k + 1
	sum := sha256.Sum256([]byte(fmt.Sprintf("%s\x1f%d", base, k)))
	return prefix + hex.EncodeToString(sum[:])
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
			externalID = syntheticExternalID("ofx:", t, amountMinor, desc, occ)
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
