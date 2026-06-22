package attachments

// attachment_handler.go
// =============================================================================
// HTTP handlers for expense attachments (receipts). The handler's job is the
// usual narrow one: parse the request, call AttachmentService, write the
// response. All routes here sit behind authMiddleware, so the caller's identity
// (user + organisation) comes from the token via kernel.GetAuthUserID / kernel.GetAuthOrgID,
// never from the request body.
//
// Routes (registered in server.go, nested under the expenses group):
//   POST   /api/v1/expenses/:id/attachments                         upload
//   GET    /api/v1/expenses/:id/attachments                         list metadata
//   GET    /api/v1/expenses/:id/attachments/:attachmentId/download  signed URL
//   PATCH  /api/v1/expenses/:id/attachments/:attachmentId/primary   set primary
//   DELETE /api/v1/expenses/:id/attachments/:attachmentId           delete
// =============================================================================

import (
	"net/http"

	"github.com/gin-gonic/gin"

	kernel "github.com/operationfb/accounting-saas/internal/kernel"
	"github.com/operationfb/accounting-saas/token"
)

// Handler is the HTTP boundary for the attachments domain.
type Handler struct{ svc *Service }

// NewHandler builds the attachments HTTP handler.
func NewHandler(svc *Service) *Handler { return &Handler{svc: svc} }

// RegisterRoutes mounts the attachment sub-resource + Smart Upload under
// /api/v1/expenses (its own auth group; same :id wildcard as the expense CRUD
// routes that internal/expenses registers separately — Gin merges the two).
func (h *Handler) RegisterRoutes(r *gin.Engine, tokenMaker token.Maker) {
	g := r.Group("/api/v1/expenses")
	g.Use(kernel.AuthMiddleware(tokenMaker))
	{
		g.POST("/capture", h.handleSmartUpload)
		g.POST("/:id/attachments", h.handleUploadAttachment)
		g.GET("/:id/attachments", h.handleListAttachments)
		g.GET("/:id/attachments/:attachmentId/download", h.handleDownloadAttachment)
		g.PATCH("/:id/attachments/:attachmentId/primary", h.handleSetPrimaryAttachment)
		g.DELETE("/:id/attachments/:attachmentId", h.handleDeleteAttachment)
	}
}

// MaxUploadRequestBytes hard-caps the whole multipart request body. It is a
// little larger than the per-file limit to allow for multipart framing; the
// service enforces the precise per-file size limit. This stops a client from
// streaming a huge body before we ever look at the declared file size.
const MaxUploadRequestBytes = defaultMaxUploadBytes + (1 << 20) // +1 MiB slack

// handleUploadAttachment handles POST /api/v1/expenses/:id/attachments.
// Expects multipart/form-data with a "file" field and an optional "description".
func (h *Handler) handleUploadAttachment(c *gin.Context) {
	expenseID := c.Param("id")
	userID := kernel.GetAuthUserID(c)
	orgID := kernel.GetAuthOrgID(c)

	// Cap the request body BEFORE parsing so an oversized upload can't exhaust
	// memory/disk. Reads past the limit fail, which surfaces below as a 400.
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, MaxUploadRequestBytes)

	fileHeader, err := c.FormFile("file")
	if err != nil {
		// Either no "file" field, or the body exceeded MaxUploadRequestBytes.
		c.JSON(http.StatusBadRequest, gin.H{"error": gin.H{
			"code":    "validation_error",
			"message": "a multipart 'file' field is required (or the upload was too large)",
		}})
		return
	}

	// fileHeader.Open returns a multipart.File, which is an io.ReadSeeker — the
	// service rewinds it after sniffing the content type.
	f, err := fileHeader.Open()
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": gin.H{
			"code":    "validation_error",
			"message": "could not read the uploaded file",
		}})
		return
	}
	defer f.Close()

	var description *string
	if d := c.PostForm("description"); d != "" {
		description = &d
	}

	resp, err := h.svc.UploadAttachment(
		c.Request.Context(), userID, orgID, expenseID,
		fileHeader.Filename, fileHeader.Size, f, description,
	)
	if err != nil {
		kernel.RespondError(c, err)
		return
	}
	c.JSON(http.StatusCreated, gin.H{"attachment": resp})
}

// handleSmartUpload handles POST /api/v1/expenses/capture.
//
// "Smart Upload": a receipt/invoice dropped in with no expense yet. Expects
// multipart/form-data with a "file" field and a "document_type" field
// ("receipt" | "invoice", which routes OCR to the matching Document AI
// processor). It creates a skeleton draft, attaches the file, and kicks off
// background OCR, then returns the new draft (with its PENDING attachment) so the
// SPA can open the form and poll GET /expenses/:id until OCR fills it in.
func (h *Handler) handleSmartUpload(c *gin.Context) {
	userID := kernel.GetAuthUserID(c)
	orgID := kernel.GetAuthOrgID(c)
	documentType := c.PostForm("document_type")

	// Cap the request body BEFORE parsing, same as handleUploadAttachment.
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, MaxUploadRequestBytes)

	fileHeader, err := c.FormFile("file")
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": gin.H{
			"code":    "validation_error",
			"message": "a multipart 'file' field is required (or the upload was too large)",
		}})
		return
	}

	f, err := fileHeader.Open()
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": gin.H{
			"code":    "validation_error",
			"message": "could not read the uploaded file",
		}})
		return
	}
	defer f.Close()

	resp, err := h.svc.CaptureFromReceipt(
		c.Request.Context(), userID, orgID, documentType,
		fileHeader.Filename, fileHeader.Size, f,
	)
	if err != nil {
		kernel.RespondError(c, err)
		return
	}
	c.JSON(http.StatusCreated, gin.H{"expense": resp})
}

// handleListAttachments handles GET /api/v1/expenses/:id/attachments.
func (h *Handler) handleListAttachments(c *gin.Context) {
	list, err := h.svc.ListAttachments(
		c.Request.Context(), kernel.GetAuthUserID(c), kernel.GetAuthOrgID(c), c.Param("id"),
	)
	if err != nil {
		kernel.RespondError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"attachments": list})
}

// handleDownloadAttachment handles
// GET /api/v1/expenses/:id/attachments/:attachmentId/download.
// It returns a short-lived signed URL the client uses to fetch the bytes
// directly from storage. We return the URL as JSON (rather than 302-redirecting)
// so the SPA can decide how to use it.
func (h *Handler) handleDownloadAttachment(c *gin.Context) {
	url, err := h.svc.GetDownloadURL(
		c.Request.Context(), kernel.GetAuthUserID(c), kernel.GetAuthOrgID(c), c.Param("id"), c.Param("attachmentId"),
	)
	if err != nil {
		kernel.RespondError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"download_url": url})
}

// handleSetPrimaryAttachment handles
// PATCH /api/v1/expenses/:id/attachments/:attachmentId/primary.
func (h *Handler) handleSetPrimaryAttachment(c *gin.Context) {
	resp, err := h.svc.SetPrimary(
		c.Request.Context(), kernel.GetAuthUserID(c), kernel.GetAuthOrgID(c), c.Param("id"), c.Param("attachmentId"),
	)
	if err != nil {
		kernel.RespondError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"attachment": resp})
}

// handleDeleteAttachment handles
// DELETE /api/v1/expenses/:id/attachments/:attachmentId.
func (h *Handler) handleDeleteAttachment(c *gin.Context) {
	err := h.svc.DeleteAttachment(
		c.Request.Context(), kernel.GetAuthUserID(c), kernel.GetAuthOrgID(c), c.Param("id"), c.Param("attachmentId"),
	)
	if err != nil {
		kernel.RespondError(c, err)
		return
	}
	c.Status(http.StatusNoContent)
}
