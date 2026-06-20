package integrations

// attachments.go
// =============================================================================
// Receipt-attachment push: the workflow-facing operation that hands the external
// Cloud Workflow a single base64-encoded receipt to attach to the destination
// expense (e.g. FreeAgent's nested `attachment`).
//
// The binary lives behind a narrow cross-domain seam (AttachmentFetcher, satisfied
// by *main.AttachmentService) — this package needn't depend on the whole
// attachments/storage domain, mirroring the Handler's ExpenseRepublisher. The
// base64 is produced HERE (tested Go), never in the YAML; the YAML just forwards
// it. Only the PRIMARY attachment is pushed (the destination takes one per
// expense), and the fetcher size-guards before downloading.
// =============================================================================

import (
	"context"
	"encoding/base64"

	"github.com/google/uuid"

	"github.com/operationfb/accounting-saas/internal/kernel"
)

// AttachmentFetcher reads an expense's PRIMARY receipt for the push. Returns
// found=false (nil error) when there is nothing to push — no attachment, one over
// maxBytes, or a cross-tenant expense id. A genuine storage/DB failure is a (raw)
// error. Satisfied by *main.AttachmentService.PrimaryAttachmentForPush.
type AttachmentFetcher interface {
	PrimaryAttachmentForPush(ctx context.Context, orgID, expenseID uuid.UUID, maxBytes int64) (data []byte, fileName, contentType string, found bool, err error)
}

// InternalAttachmentResponse is the 200 body of the attachment internal endpoint.
// ContentType is the RAW stored MIME (e.g. "application/pdf"); the workflow remaps
// it into the destination's vocabulary (FreeAgent wants "application/x-pdf"),
// exactly like it maps ec_status and the category URL — provider mapping is YAML.
type InternalAttachmentResponse struct {
	Data        string `json:"data"` // base64-encoded file bytes
	FileName    string `json:"file_name"`
	ContentType string `json:"content_type"`
}

// AttachmentForPush returns the expense's primary receipt as base64, or found=false
// when there is nothing to push (→ the endpoint answers 204, and the workflow omits
// the attachment). Org-scoped via the fetcher. A nil fetcher (attachment push not
// wired) is treated as "no attachment", matching the optional-dependency pattern.
func (s *Service) AttachmentForPush(ctx context.Context, orgID, expenseID uuid.UUID) (*InternalAttachmentResponse, bool, error) {
	if s.attachments == nil {
		return nil, false, nil
	}
	data, fileName, contentType, found, err := s.attachments.PrimaryAttachmentForPush(ctx, orgID, expenseID, s.maxAttachmentBytes)
	if err != nil {
		return nil, false, kernel.ErrInternal(err)
	}
	if !found {
		return nil, false, nil
	}
	return &InternalAttachmentResponse{
		Data:        base64.StdEncoding.EncodeToString(data),
		FileName:    fileName,
		ContentType: contentType,
	}, true, nil
}
