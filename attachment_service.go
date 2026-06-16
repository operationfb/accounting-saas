package main

// attachment_service.go
// =============================================================================
// AttachmentService — business logic for expense file attachments (receipts).
//
// Where things live (the golden rule):
//   - File BYTES    → Google Cloud Storage, via the Storage interface.
//   - File METADATA → PostgreSQL (expense_attachments), via sqlc queries.
//
// The flow for an upload:
//   1. Authorise the caller against the parent expense — same rule as reading an
//      expense: the claimant, or an org owner/admin.
//   2. Validate the file: a size cap, and the REAL content type via a magic-byte
//      sniff (never the client-declared type or the filename extension — both
//      can lie).
//   3. Write the bytes to GCS under a server-chosen key (no user input in the
//      path).
//   4. Record the metadata row. The FIRST file on an expense becomes the
//      primary (default) one.
//
// GCS and PostgreSQL are not one transaction, so step 3 (GCS) runs before step 4
// (DB); if the DB write fails we best-effort delete the just-uploaded object so
// we don't leak an orphan file.
// =============================================================================

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"path"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	auth "github.com/operationfb/accounting-saas/db/auth"
	expenses "github.com/operationfb/accounting-saas/db/expenses"
)

// placeholderCategoryNominal is the nominal code of the catch-all category a
// Smart Upload skeleton is filed under until the user picks a real one. '6021'
// is "Sundries" in the seeded chart of accounts — a normal, VAT-able admin
// category, which is the least-wrong default for an as-yet-unclassified receipt.
const placeholderCategoryNominal = "6021"

// ocrEnqueuer is the slice of OcrService that AttachmentService depends on: kick
// off background OCR for a freshly captured attachment. Declared here (the
// consumer side, the Go convention) and used as a nil-able dependency — when OCR
// is not configured, Smart Upload still creates the draft, it just stays PENDING.
type ocrEnqueuer interface {
	Enqueue(attachmentID, orgID uuid.UUID, documentType string)
}

// allowedContentTypes maps each accepted (sniffed) MIME type to the extension we
// give the stored object. Receipts are PDFs or photos/scans. This map doubles as
// the allowlist: a sniffed type that is not a key here is rejected.
var allowedContentTypes = map[string]string{
	"application/pdf": ".pdf",
	"image/jpeg":      ".jpg",
	"image/png":       ".png",
}

const (
	// defaultMaxUploadBytes caps a single file at 20 MiB. file_size_bytes is an
	// INTEGER (int32) column, so this stays comfortably in range.
	defaultMaxUploadBytes int64 = 20 * 1024 * 1024
	// defaultSignedURLTTL is how long a generated download URL stays valid.
	defaultSignedURLTTL = 15 * time.Minute
	// sniffLen is how many leading bytes http.DetectContentType inspects.
	sniffLen = 512
)

// AttachmentService orchestrates storage + database for receipt files.
type AttachmentService struct {
	pool        *pgxpool.Pool
	queries     *expenses.Queries
	authQueries auth.Querier
	storage     Storage
	ocr         ocrEnqueuer // nil when OCR isn't configured

	maxBytes int64
	urlTTL   time.Duration
}

// NewAttachmentService constructs the service. Passing maxBytes or urlTTL <= 0
// selects the package defaults. store may be nil when the app is started without
// GCS configured (GCS_BUCKET unset); in that case the upload/download paths
// return a clear internal error rather than panicking. ocr may be nil when
// Document AI isn't configured; Smart Upload then creates drafts without OCR.
func NewAttachmentService(pool *pgxpool.Pool, queries *expenses.Queries, authQueries auth.Querier, store Storage, ocr ocrEnqueuer, maxBytes int64, urlTTL time.Duration) *AttachmentService {
	if maxBytes <= 0 {
		maxBytes = defaultMaxUploadBytes
	}
	if urlTTL <= 0 {
		urlTTL = defaultSignedURLTTL
	}
	return &AttachmentService{
		pool:        pool,
		queries:     queries,
		authQueries: authQueries,
		storage:     store,
		ocr:         ocr,
		maxBytes:    maxBytes,
		urlTTL:      urlTTL,
	}
}

// =============================================================================
// RESPONSE DTO
//
// Kept here (not in server.go with the expense DTOs) to keep the attachments
// feature self-contained. It never carries the file bytes (fetched separately
// via a signed download URL) nor the internal storage bucket/key. It DOES carry
// the OCR status + structured data (but not the bulky ocr_raw_text): the Smart
// Upload frontend polls ocr_status to know when extraction is done and reads
// ocr_extracted_data for the "OCR found…" badges.
// =============================================================================

// AttachmentResponse is the API shape for one attachment's metadata.
type AttachmentResponse struct {
	ID               string  `json:"id"`
	ExpenseID        string  `json:"expense_id"`
	FileName         string  `json:"file_name"`
	ContentType      string  `json:"content_type"`
	FileSizeBytes    int32   `json:"file_size_bytes"`
	IsPrimary        bool    `json:"is_primary"`
	Description      *string `json:"description,omitempty"`
	UploadedByUserID string  `json:"uploaded_by_user_id"`
	CreatedAt        string  `json:"created_at"`

	// OCR (Smart Upload). OCRStatus is the polled signal: PENDING|PROCESSING are
	// non-terminal; COMPLETE|FAILED|SKIPPED are terminal. OCRExtractedData is the
	// raw JSONB ("what OCR saw") passed straight through as JSON.
	OCRStatus        string          `json:"ocr_status"`
	OCRExtractedData json.RawMessage `json:"ocr_extracted_data,omitempty"`
	OCRProcessedAt   *string         `json:"ocr_processed_at,omitempty"`
}

// attachmentToResponse maps a generated row to the API shape. It reuses the
// nullable-text/timestamp helpers from expense_service.go (same package).
func attachmentToResponse(a expenses.ExpenseAttachment) *AttachmentResponse {
	return &AttachmentResponse{
		ID:               a.ID.String(),
		ExpenseID:        a.ExpenseID.String(),
		FileName:         a.FileName,
		ContentType:      a.ContentType,
		FileSizeBytes:    a.FileSizeBytes,
		IsPrimary:        a.IsPrimary,
		Description:      nullTextToPtr(a.Description),
		UploadedByUserID: a.UploadedByUserID.String(),
		CreatedAt:        a.CreatedAt.Time.Format(time.RFC3339),
		OCRStatus:        a.OcrStatus,
		// JSONB comes back as []byte; json.RawMessage passes it through verbatim.
		// nil (no OCR yet) is omitted by the ,omitempty tag.
		OCRExtractedData: json.RawMessage(a.OcrExtractedData),
		OCRProcessedAt:   timestampToStringPtr(a.OcrProcessedAt),
	}
}

// =============================================================================
// AUTHORISATION + PARENT LOADING
// =============================================================================

// authorize mirrors ExpenseService.authorize: confirm the caller is an ACTIVE
// member of the organisation and return their role. It is duplicated (rather
// than shared) to keep the attachments feature self-contained — the logic is
// identical and security-critical, so keep the two in sync.
func (s *AttachmentService) authorize(ctx context.Context, userID, orgID uuid.UUID) (auth.OrganisationRole, error) {
	m, err := s.authQueries.GetMembership(ctx, auth.GetMembershipParams{
		OrganisationID: orgID,
		UserID:         userID,
	})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return "", ErrForbidden("you are not a member of this organisation")
		}
		return "", ErrInternal(err)
	}
	if m.Status != "active" {
		return "", ErrForbidden("your organisation membership is not active")
	}
	return m.Role, nil
}

// loadAuthorisedExpense authorises the caller and loads the parent expense,
// enforcing the same access rule as reading an expense: the claimant, or an
// owner/admin of the organisation. Returns ErrNotFound when the expense does not
// exist in this org, ErrForbidden when the caller may not touch it.
func (s *AttachmentService) loadAuthorisedExpense(ctx context.Context, callerID, orgID, expenseID uuid.UUID) (expenses.Expense, error) {
	role, err := s.authorize(ctx, callerID, orgID)
	if err != nil {
		return expenses.Expense{}, err
	}
	exp, err := s.queries.GetExpense(ctx, expenses.GetExpenseParams{
		ID:             expenseID,
		OrganisationID: orgID,
	})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return expenses.Expense{}, ErrNotFound("expense", expenseID.String())
		}
		return expenses.Expense{}, ErrInternal(err)
	}
	if exp.UserID != callerID && !isOrgAdmin(role) {
		return expenses.Expense{}, ErrForbidden("you do not have access to this expense")
	}
	return exp, nil
}

// loadAuthorisedAttachment authorises the caller against the parent expense and
// returns the attachment, ensuring it both exists in this org AND belongs to the
// expense named in the URL (defence in depth against id-swapping).
func (s *AttachmentService) loadAuthorisedAttachment(ctx context.Context, callerID, orgID uuid.UUID, expenseID, attachmentID string) (expenses.ExpenseAttachment, error) {
	eid, err := parseResourceUUID(expenseID, "expense")
	if err != nil {
		return expenses.ExpenseAttachment{}, err
	}
	aid, err := parseResourceUUID(attachmentID, "attachment")
	if err != nil {
		return expenses.ExpenseAttachment{}, err
	}
	if _, err := s.loadAuthorisedExpense(ctx, callerID, orgID, eid); err != nil {
		return expenses.ExpenseAttachment{}, err
	}
	att, err := s.queries.GetExpenseAttachment(ctx, expenses.GetExpenseAttachmentParams{
		ID:             aid,
		OrganisationID: orgID,
	})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return expenses.ExpenseAttachment{}, ErrNotFound("attachment", attachmentID)
		}
		return expenses.ExpenseAttachment{}, ErrInternal(err)
	}
	if att.ExpenseID != eid {
		// Exists in the org but under a DIFFERENT expense than the URL claims —
		// treat as not found rather than confirming its existence.
		return expenses.ExpenseAttachment{}, ErrNotFound("attachment", attachmentID)
	}
	return att, nil
}

// =============================================================================
// UPLOAD
// =============================================================================

// UploadAttachment validates and stores one receipt file for an expense, then
// records its metadata. content is the file body; size and filename come from
// the multipart header (handler) or the test. description is an optional label.
// The first file uploaded to an expense becomes the primary (default) one.
func (s *AttachmentService) UploadAttachment(
	ctx context.Context,
	callerID, orgID uuid.UUID,
	expenseID string,
	filename string,
	size int64,
	content io.ReadSeeker,
	description *string,
) (*AttachmentResponse, error) {
	if s.storage == nil {
		return nil, ErrInternal(errors.New("file storage is not configured (GCS_BUCKET unset)"))
	}

	eid, err := parseResourceUUID(expenseID, "expense")
	if err != nil {
		return nil, err
	}
	if _, err := s.loadAuthorisedExpense(ctx, callerID, orgID, eid); err != nil {
		return nil, err
	}

	// ---- Validate size + sniff the real content type ------------------------
	contentType, ext, err := s.validateUpload(size, content)
	if err != nil {
		return nil, err
	}

	// ---- Storage key: server-chosen, no user input in the path --------------
	// Layout: orgs/<org>/expenses/<expense>/<random-uuid><ext>. The human
	// filename is kept only in the file_name column, so a hostile filename can
	// never escape the prefix or collide with another object.
	objectID := uuid.New()
	key := fmt.Sprintf("orgs/%s/expenses/%s/%s%s", orgID, eid, objectID, ext)

	// ---- 1) Write the bytes to GCS FIRST ------------------------------------
	if err := s.storage.Upload(ctx, key, contentType, content); err != nil {
		return nil, ErrInternal(fmt.Errorf("upload to storage: %w", err))
	}

	// ---- 2) Record metadata; the first file becomes primary -----------------
	var created expenses.ExpenseAttachment
	dbErr := s.withTx(ctx, func(qtx *expenses.Queries) error {
		count, err := qtx.CountExpenseAttachments(ctx, eid)
		if err != nil {
			return err
		}
		row, err := qtx.CreateExpenseAttachment(ctx, expenses.CreateExpenseAttachmentParams{
			ExpenseID:        eid,
			OrganisationID:   orgID,
			FileName:         sanitiseFilename(filename),
			ContentType:      contentType,
			FileSizeBytes:    int32(size),
			StoragePath:      key,
			StorageBucket:    s.storage.Bucket(),
			Description:      pgNullText(description),
			IsPrimary:        count == 0, // first file on this expense → primary (default)
			UploadedByUserID: callerID,
		})
		if err != nil {
			return err
		}
		created = row
		// TODO: write an expense_audit_log entry (see CreateExpense's transaction).
		return nil
	})
	if dbErr != nil {
		// The DB write failed but the object is already in GCS. Best-effort delete
		// it so we don't leak an orphan. Use a fresh context: the request ctx may
		// itself be the reason the write failed (cancelled / timed out).
		s.cleanupOrphan(key)
		return nil, ErrInternal(fmt.Errorf("record attachment metadata: %w", dbErr))
	}

	return attachmentToResponse(created), nil
}

// =============================================================================
// SMART UPLOAD (receipt-first capture)
// =============================================================================

// CaptureFromReceipt powers the "Smart Upload" button: a receipt/invoice is
// dropped in with no expense yet, so we CREATE a skeleton draft (needs_review=
// TRUE) with this file attached, then kick off background OCR to fill it in.
// This is deliberately distinct from UploadAttachment ("Add file"), which
// attaches to an EXISTING expense and runs no OCR.
//
// documentType ("receipt" | "invoice") routes OCR to the matching Document AI
// processor. The returned detail response already embeds the new attachment with
// ocr_status=PENDING, so the frontend can open the draft form and start polling.
func (s *AttachmentService) CaptureFromReceipt(
	ctx context.Context,
	callerID, orgID uuid.UUID,
	documentType string,
	filename string,
	size int64,
	content io.ReadSeeker,
) (*ExpenseDetailResponse, error) {
	if s.storage == nil {
		return nil, ErrInternal(errors.New("file storage is not configured (GCS_BUCKET unset)"))
	}
	if !validDocumentType(documentType) {
		return nil, ErrValidation(`document_type must be "receipt" or "invoice"`, nil)
	}

	// Authorise: an active member of the org (same bar as creating an expense).
	if _, err := s.authorize(ctx, callerID, orgID); err != nil {
		return nil, err
	}

	// Validate size + sniff the real content type (shared with UploadAttachment).
	contentType, ext, err := s.validateUpload(size, content)
	if err != nil {
		return nil, err
	}

	// Resolve the org's placeholder category ('Sundries') for the skeleton.
	cat, err := s.queries.GetExpenseCategoryByNominalCode(ctx, expenses.GetExpenseCategoryByNominalCodeParams{
		OrganisationID: orgID,
		NominalCode:    placeholderCategoryNominal,
	})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrInternal(fmt.Errorf("no placeholder category %q configured for organisation %s", placeholderCategoryNominal, orgID))
		}
		return nil, ErrInternal(err)
	}

	// Storage key: server-chosen. The expense id doesn't exist yet (created in the
	// tx below), so we use a capture-scoped prefix; a hostile filename still can't
	// escape it (the human name is kept only in file_name).
	objectID := uuid.New()
	key := fmt.Sprintf("orgs/%s/captures/%s%s", orgID, objectID, ext)

	// 1) Bytes to GCS FIRST — same ordering as UploadAttachment: object before
	//    metadata, so a failed DB write leaves a harmless orphan object we clean
	//    up, never a skeleton expense pointing at a missing file.
	if err := s.storage.Upload(ctx, key, contentType, content); err != nil {
		return nil, ErrInternal(fmt.Errorf("upload to storage: %w", err))
	}

	// 2) One transaction: the skeleton expense + its (primary) attachment.
	var expID, attID uuid.UUID
	dbErr := s.withTx(ctx, func(qtx *expenses.Queries) error {
		exp, err := qtx.CreateSkeletonExpense(ctx, expenses.CreateSkeletonExpenseParams{
			OrganisationID:        orgID,
			UserID:                callerID,
			CreatedByUserID:       callerID,
			CategoryID:            cat.ID,
			DatedOn:               pgDateFromTime(time.Now()), // placeholder; OCR overwrites
			Description:           "Awaiting review",          // placeholder
			GrossValueMinor:       0,                          // placeholder; OCR fills
			NativeGrossValueMinor: 0,
		})
		if err != nil {
			return err
		}
		att, err := qtx.CreateExpenseAttachment(ctx, expenses.CreateExpenseAttachmentParams{
			ExpenseID:        exp.ID,
			OrganisationID:   orgID,
			FileName:         sanitiseFilename(filename),
			ContentType:      contentType,
			FileSizeBytes:    int32(size),
			StoragePath:      key,
			StorageBucket:    s.storage.Bucket(),
			Description:      pgNullText(nil), // no user description on a capture
			IsPrimary:        true,            // first (only) file on a fresh skeleton
			UploadedByUserID: callerID,
		})
		if err != nil {
			return err
		}
		expID, attID = exp.ID, att.ID
		return nil
	})
	if dbErr != nil {
		s.cleanupOrphan(key)
		return nil, ErrInternal(fmt.Errorf("create capture: %w", dbErr))
	}

	// 3) Kick off background OCR (no-op when not configured). The attachment is
	//    PENDING; the worker drives it to a terminal state.
	if s.ocr != nil {
		s.ocr.Enqueue(attID, orgID, documentType)
	}

	// 4) Return the new draft (with its PENDING attachment) so the frontend can
	//    open the form and poll GET /expenses/:id until OCR completes.
	return buildExpenseDetail(ctx, s.queries, orgID, expID)
}

// =============================================================================
// LIST / DOWNLOAD / SET-PRIMARY / DELETE
// =============================================================================

// ListAttachments returns the metadata for every file on an expense (primary
// first). Valid (and empty) when the expense has no attachments — receipts are
// optional. No bytes and no URLs are returned.
func (s *AttachmentService) ListAttachments(ctx context.Context, callerID, orgID uuid.UUID, expenseID string) ([]*AttachmentResponse, error) {
	eid, err := parseResourceUUID(expenseID, "expense")
	if err != nil {
		return nil, err
	}
	if _, err := s.loadAuthorisedExpense(ctx, callerID, orgID, eid); err != nil {
		return nil, err
	}
	rows, err := s.queries.ListExpenseAttachments(ctx, eid)
	if err != nil {
		return nil, ErrInternal(err)
	}
	out := make([]*AttachmentResponse, 0, len(rows))
	for _, r := range rows {
		out = append(out, attachmentToResponse(r))
	}
	return out, nil
}

// GetDownloadURL returns a short-lived signed URL for an attachment's bytes.
func (s *AttachmentService) GetDownloadURL(ctx context.Context, callerID, orgID uuid.UUID, expenseID, attachmentID string) (string, error) {
	if s.storage == nil {
		return "", ErrInternal(errors.New("file storage is not configured (GCS_BUCKET unset)"))
	}
	att, err := s.loadAuthorisedAttachment(ctx, callerID, orgID, expenseID, attachmentID)
	if err != nil {
		return "", err
	}
	url, err := s.storage.SignedDownloadURL(ctx, att.StoragePath, s.urlTTL)
	if err != nil {
		return "", ErrInternal(fmt.Errorf("generate download URL: %w", err))
	}
	return url, nil
}

// SetPrimary marks one attachment as the primary (default) file for its expense,
// clearing the flag on the others in the same transaction so exactly one remains.
func (s *AttachmentService) SetPrimary(ctx context.Context, callerID, orgID uuid.UUID, expenseID, attachmentID string) (*AttachmentResponse, error) {
	att, err := s.loadAuthorisedAttachment(ctx, callerID, orgID, expenseID, attachmentID)
	if err != nil {
		return nil, err
	}
	txErr := s.withTx(ctx, func(qtx *expenses.Queries) error {
		if err := qtx.UnsetExpensePrimary(ctx, expenses.UnsetExpensePrimaryParams{
			ExpenseID:      att.ExpenseID,
			OrganisationID: orgID,
		}); err != nil {
			return err
		}
		return qtx.SetAttachmentPrimary(ctx, expenses.SetAttachmentPrimaryParams{
			ID:             att.ID,
			OrganisationID: orgID,
		})
	})
	if txErr != nil {
		return nil, ErrInternal(fmt.Errorf("set primary attachment: %w", txErr))
	}
	att.IsPrimary = true
	return attachmentToResponse(att), nil
}

// DeleteAttachment removes an attachment's metadata row and its stored file. If
// the deleted file was the primary one, the oldest remaining attachment is
// promoted so the "exactly one primary" rule still holds.
func (s *AttachmentService) DeleteAttachment(ctx context.Context, callerID, orgID uuid.UUID, expenseID, attachmentID string) error {
	att, err := s.loadAuthorisedAttachment(ctx, callerID, orgID, expenseID, attachmentID)
	if err != nil {
		return err
	}

	// Do the DB work first: if it fails we have NOT yet deleted the file, so
	// nothing is lost.
	txErr := s.withTx(ctx, func(qtx *expenses.Queries) error {
		if err := qtx.DeleteExpenseAttachment(ctx, expenses.DeleteExpenseAttachmentParams{
			ID:             att.ID,
			OrganisationID: orgID,
		}); err != nil {
			return err
		}
		if att.IsPrimary {
			// The expense just lost its primary — promote the oldest remaining
			// file. The list is ordered is_primary DESC, created_at ASC; with no
			// primary left, the first row is the oldest.
			remaining, err := qtx.ListExpenseAttachments(ctx, att.ExpenseID)
			if err != nil {
				return err
			}
			if len(remaining) > 0 {
				if err := qtx.SetAttachmentPrimary(ctx, expenses.SetAttachmentPrimaryParams{
					ID:             remaining[0].ID,
					OrganisationID: orgID,
				}); err != nil {
					return err
				}
			}
		}
		return nil
	})
	if txErr != nil {
		return ErrInternal(fmt.Errorf("delete attachment metadata: %w", txErr))
	}

	// Metadata is gone; now best-effort delete the bytes. If this fails the row
	// is already gone, leaving an orphan object for the reconciliation sweep
	// (backlog) to reclaim.
	if s.storage != nil {
		s.cleanupOrphan(att.StoragePath)
	}
	return nil
}

// =============================================================================
// HELPERS
// =============================================================================

// validateUpload enforces the size cap and sniffs the REAL content type from the
// file's magic bytes (never the client-declared type or filename extension —
// both can lie). It returns the sniffed MIME type and the extension we store the
// object under, and rewinds content to the start so the caller can stream the
// whole file afterwards. Shared by UploadAttachment and CaptureFromReceipt.
func (s *AttachmentService) validateUpload(size int64, content io.ReadSeeker) (contentType, ext string, err error) {
	if size <= 0 {
		return "", "", ErrValidation("uploaded file is empty", nil)
	}
	if size > s.maxBytes {
		return "", "", ErrPayloadTooLarge(fmt.Sprintf("file is %d bytes; the limit is %d bytes", size, s.maxBytes))
	}

	// http.DetectContentType inspects up to the first 512 bytes.
	head := make([]byte, sniffLen)
	n, rerr := io.ReadFull(content, head)
	if rerr != nil && !errors.Is(rerr, io.ErrUnexpectedEOF) && !errors.Is(rerr, io.EOF) {
		return "", "", ErrInternal(fmt.Errorf("read file header: %w", rerr))
	}
	contentType = http.DetectContentType(head[:n])
	allowedExt, ok := allowedContentTypes[contentType]
	if !ok {
		return "", "", ErrUnsupportedMediaType(fmt.Sprintf("file type %q is not allowed; accepted types are PDF, JPEG and PNG", contentType))
	}

	// Rewind: we consumed the header for sniffing, but the upload must stream the
	// whole file.
	if _, serr := content.Seek(0, io.SeekStart); serr != nil {
		return "", "", ErrInternal(fmt.Errorf("rewind file: %w", serr))
	}
	return contentType, allowedExt, nil
}

// withTx runs fn inside a transaction, mirroring ExpenseService.withTransaction
// (kept local so the attachments feature owns its own transactions).
func (s *AttachmentService) withTx(ctx context.Context, fn func(*expenses.Queries) error) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin transaction: %w", err)
	}
	qtx := expenses.New(tx)
	if err := fn(qtx); err != nil {
		_ = tx.Rollback(ctx)
		return err
	}
	return tx.Commit(ctx)
}

// cleanupOrphan best-effort deletes a stored object using a fresh, bounded
// context (so a cancelled/expired request context can't also block cleanup).
func (s *AttachmentService) cleanupOrphan(key string) {
	cleanupCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	_ = s.storage.Delete(cleanupCtx, key)
}

// parseResourceUUID parses a path id, returning a validation error that names
// the resource ("expense", "attachment") when it isn't a UUID.
func parseResourceUUID(id, resource string) (uuid.UUID, error) {
	u, err := uuid.Parse(id)
	if err != nil {
		return uuid.Nil, ErrValidation(fmt.Sprintf("%s id is not a valid UUID", resource), err)
	}
	return u, nil
}

// sanitiseFilename keeps a human-friendly, safe version of the uploaded filename
// for display (stored in file_name). It is NOT used to build the storage key —
// that uses a server-generated UUID — so this is about tidy display, not path
// safety. We drop any directory components and cap the length to the column size.
func sanitiseFilename(name string) string {
	name = strings.ReplaceAll(name, "\\", "/") // normalise Windows separators
	name = path.Base(name)                     // drop directory components
	name = strings.TrimSpace(name)
	if name == "" || name == "." || name == ".." {
		return "upload"
	}
	if r := []rune(name); len(r) > 255 { // file_name is VARCHAR(255) (characters)
		name = string(r[:255])
	}
	return name
}
