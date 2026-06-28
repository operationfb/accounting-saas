package banking

// statement_preview.go
// =============================================================================
// The "detect" half of the detect → confirm → commit import flow. PreviewStatement
// reads an uploaded file but WRITES NOTHING: for a CSV it returns the detected columns,
// a PROPOSED ColumnMapping, the date-format choices, and a sample of how the first rows
// would be interpreted — everything the confirm screen needs to pre-fill its dropdowns
// and show a live preview. For an OFX file (self-describing) it just returns the parsed
// sample rows, no mapping UI. The user confirms/edits, then the commit endpoint
// (ImportStatement) re-sends the file + the agreed mapping.
// =============================================================================

import (
	"bytes"
	"context"
	"errors"
	"io"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	banking "github.com/operationfb/accounting-saas/db/banking"
	"github.com/operationfb/accounting-saas/internal/kernel"
	"github.com/operationfb/accounting-saas/money"
)

const (
	previewRowLimit    = 10 // how many interpreted rows the preview returns
	previewSampleLimit = 3  // how many raw sample values per detected column
)

// StatementImportPreviewResponse is the detect step's payload. For CSV every field is
// populated; for OFX only Format + PreviewRows + TotalRows are (no mapping to confirm).
type StatementImportPreviewResponse struct {
	Format            string             `json:"format"` // "csv" | "ofx"
	Columns           []DetectedColumn   `json:"columns,omitempty"`
	Mapping           *ColumnMapping     `json:"mapping,omitempty"`
	DateFormatOptions []dateLayoutOption `json:"date_format_options,omitempty"`
	PreviewRows       []PreviewRow       `json:"preview_rows"`
	TotalRows         int                `json:"total_rows"`
	Warnings          []string           `json:"warnings,omitempty"`
}

// DetectedColumn is one column of the uploaded CSV: its position, the raw header text (for
// the dropdown label) and a few sample values (so the user can tell columns apart).
type DetectedColumn struct {
	Index   int      `json:"index"`
	Header  string   `json:"header"`
	Samples []string `json:"samples"`
}

// PreviewRow is one data row as the (proposed or edited) mapping would interpret it — money
// already split into in/out pound strings by sign. Error is set (and the money fields left
// nil) when the row wouldn't import as currently mapped, so the user sees what to fix.
type PreviewRow struct {
	DatedOn     string  `json:"dated_on,omitempty"`
	Description string  `json:"description,omitempty"`
	MoneyIn     *string `json:"money_in,omitempty"`
	MoneyOut    *string `json:"money_out,omitempty"`
	Balance     *string `json:"balance,omitempty"`
	Error       *string `json:"error,omitempty"`
}

// PreviewStatement validates access, reads the (handler-capped) upload, and returns the
// detect-step payload. owner/admin only; 404 if the account isn't in this org — the same
// guards as ImportStatement, since preview reveals the file's contents. An optional
// override mapping lets the confirm screen RE-preview with the user's edits (live feedback)
// without re-detecting; nil means "auto-detect" (the first preview).
func (s *Service) PreviewStatement(ctx context.Context, authUserID, authOrgID uuid.UUID, accountID string, content io.Reader, override *ColumnMapping) (*StatementImportPreviewResponse, error) {
	accountUUID, err := uuid.Parse(accountID)
	if err != nil {
		return nil, kernel.ErrValidation("id is not a valid UUID", err)
	}
	if err := s.requireAdmin(ctx, authUserID, authOrgID); err != nil {
		return nil, err
	}
	if _, err := s.queries.GetBankAccount(ctx, banking.GetBankAccountParams{ID: accountUUID, OrganisationID: authOrgID}); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, kernel.ErrNotFound("bank account", accountID)
		}
		return nil, kernel.ErrInternal(err)
	}
	raw, err := io.ReadAll(content)
	if err != nil {
		return nil, kernel.ErrValidation("could not read the uploaded file", err)
	}
	if looksLikeOFX(raw) {
		return previewOFX(raw)
	}
	return previewCSV(raw, override)
}

// previewCSV builds the CSV preview: detected columns + a proposed (or the user's override)
// mapping + the interpreted sample rows. It is TOLERANT — per-row errors are captured into
// PreviewRow.Error rather than failing, so an imperfect mapping still shows the user where
// to adjust (the strict all-or-nothing check happens later, in parseWithMapping at commit).
func previewCSV(raw []byte, override *ColumnMapping) (*StatementImportPreviewResponse, error) {
	records, err := readCSVRecords(bytes.NewReader(raw))
	if err != nil {
		return nil, err
	}
	var mapping ColumnMapping
	var warnings []string
	if override != nil {
		mapping = *override // re-preview with the user's edited mapping
	} else {
		mapping, warnings = detectFormat(records)
	}

	header := records[0]
	cols := make([]DetectedColumn, len(header))
	for i, h := range header {
		cols[i] = DetectedColumn{Index: i, Header: h, Samples: sampleColumn(records[1:], i, previewSampleLimit)}
	}

	rows := make([]PreviewRow, 0, previewRowLimit)
	total := 0
	for _, rec := range records[1:] {
		if isBlankRecord(rec) {
			continue
		}
		total++
		if len(rows) < previewRowLimit {
			rows = append(rows, buildPreviewRow(rec, mapping))
		}
	}
	return &StatementImportPreviewResponse{
		Format:            "csv",
		Columns:           cols,
		Mapping:           &mapping,
		DateFormatOptions: dateFormatAllowlist,
		PreviewRows:       rows,
		TotalRows:         total,
		Warnings:          warnings,
	}, nil
}

// previewOFX returns a small sample of the parsed OFX lines. OFX is self-describing, so
// there is no mapping to confirm — the confirm screen just shows these rows and an Import
// button. Reuses the same parser the commit path uses (all-or-nothing: a malformed file
// surfaces its 422 here too).
func previewOFX(raw []byte) (*StatementImportPreviewResponse, error) {
	lines, err := parseStatementOFX(raw)
	if err != nil {
		return nil, err
	}
	rows := make([]PreviewRow, 0, previewRowLimit)
	for i, ln := range lines {
		if i >= previewRowLimit {
			break
		}
		rows = append(rows, previewRowFromLine(ln))
	}
	return &StatementImportPreviewResponse{Format: "ofx", PreviewRows: rows, TotalRows: len(lines)}, nil
}

// buildPreviewRow interprets one raw CSV row through the mapping for display. On a row-level
// error it returns just the error (plus the raw description cell, if mapped, for context).
func buildPreviewRow(rec []string, m ColumnMapping) PreviewRow {
	row, err := parseMappedRow(rec, m)
	if err != nil {
		msg := err.Error()
		pr := PreviewRow{Error: &msg}
		if m.DescriptionColumn != nil && *m.DescriptionColumn >= 0 && *m.DescriptionColumn < len(rec) {
			pr.Description = rec[*m.DescriptionColumn]
		}
		return pr
	}
	pr := PreviewRow{DatedOn: row.Date.Format("2006-01-02"), Description: row.Description}
	splitMoney(row.AmountMinor, &pr)
	if row.Balance.Valid {
		v := money.MinorToPounds(row.Balance.Int64)
		pr.Balance = &v
	}
	return pr
}

// previewRowFromLine maps an already-parsed line (OFX path) into a PreviewRow.
func previewRowFromLine(ln parsedLine) PreviewRow {
	pr := PreviewRow{DatedOn: ln.DatedOn.Time.Format("2006-01-02"), Description: ln.Description}
	splitMoney(ln.AmountMinor, &pr)
	return pr
}

// splitMoney fills a PreviewRow's money_in / money_out from a signed minor amount — the
// same +in / -out split the statement response uses, so the preview matches the result.
func splitMoney(amountMinor int64, pr *PreviewRow) {
	switch {
	case amountMinor > 0:
		v := money.MinorToPounds(amountMinor)
		pr.MoneyIn = &v
	case amountMinor < 0:
		v := money.MinorToPounds(-amountMinor)
		pr.MoneyOut = &v
	}
}

// sampleColumn collects up to limit non-empty values from a column, so the confirm screen
// can show "e.g. 22/06/2026, 23/06/2026" under each dropdown.
func sampleColumn(rows [][]string, col, limit int) []string {
	out := make([]string, 0, limit)
	for _, rec := range rows {
		if col >= len(rec) {
			continue
		}
		if v := rec[col]; v != "" {
			out = append(out, v)
			if len(out) >= limit {
				break
			}
		}
	}
	return out
}
