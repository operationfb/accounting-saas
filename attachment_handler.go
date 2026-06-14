package main

// attachment_handler.go
// =============================================================================
// HTTP handlers for expense attachments (receipts). The handler's job is the
// usual narrow one: parse the request, call AttachmentService, write the
// response. All routes here sit behind authMiddleware, so the caller's identity
// (user + organisation) comes from the token via getAuthUserID / getAuthOrgID,
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
)

// maxUploadRequestBytes hard-caps the whole multipart request body. It is a
// little larger than the per-file limit to allow for multipart framing; the
// service enforces the precise per-file size limit. This stops a client from
// streaming a huge body before we ever look at the declared file size.
const maxUploadRequestBytes = defaultMaxUploadBytes + (1 << 20) // +1 MiB slack

// writeAppError maps any error to the standard JSON error envelope, exactly as
// the expense handlers do inline. Centralised here because every attachment
// handler needs it.
func writeAppError(c *gin.Context, err error) {
	appErr := AsAppError(err)
	if appErr.Code == ErrCodeInternal {
		_ = appErr.Error() // TODO: replace with a structured logger (slog/zap)
	}
	c.JSON(appErr.HTTPStatus(), gin.H{"error": appErr.ClientResponse()})
}

// handleUploadAttachment handles POST /api/v1/expenses/:id/attachments.
// Expects multipart/form-data with a "file" field and an optional "description".
func (s *Server) handleUploadAttachment(c *gin.Context) {
	expenseID := c.Param("id")
	userID := getAuthUserID(c)
	orgID := getAuthOrgID(c)

	// Cap the request body BEFORE parsing so an oversized upload can't exhaust
	// memory/disk. Reads past the limit fail, which surfaces below as a 400.
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, maxUploadRequestBytes)

	fileHeader, err := c.FormFile("file")
	if err != nil {
		// Either no "file" field, or the body exceeded maxUploadRequestBytes.
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

	resp, err := s.attachmentService.UploadAttachment(
		c.Request.Context(), userID, orgID, expenseID,
		fileHeader.Filename, fileHeader.Size, f, description,
	)
	if err != nil {
		writeAppError(c, err)
		return
	}
	c.JSON(http.StatusCreated, gin.H{"attachment": resp})
}

// handleListAttachments handles GET /api/v1/expenses/:id/attachments.
func (s *Server) handleListAttachments(c *gin.Context) {
	list, err := s.attachmentService.ListAttachments(
		c.Request.Context(), getAuthUserID(c), getAuthOrgID(c), c.Param("id"),
	)
	if err != nil {
		writeAppError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"attachments": list})
}

// handleDownloadAttachment handles
// GET /api/v1/expenses/:id/attachments/:attachmentId/download.
// It returns a short-lived signed URL the client uses to fetch the bytes
// directly from storage. We return the URL as JSON (rather than 302-redirecting)
// so the SPA can decide how to use it.
func (s *Server) handleDownloadAttachment(c *gin.Context) {
	url, err := s.attachmentService.GetDownloadURL(
		c.Request.Context(), getAuthUserID(c), getAuthOrgID(c), c.Param("id"), c.Param("attachmentId"),
	)
	if err != nil {
		writeAppError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"download_url": url})
}

// handleSetPrimaryAttachment handles
// PATCH /api/v1/expenses/:id/attachments/:attachmentId/primary.
func (s *Server) handleSetPrimaryAttachment(c *gin.Context) {
	resp, err := s.attachmentService.SetPrimary(
		c.Request.Context(), getAuthUserID(c), getAuthOrgID(c), c.Param("id"), c.Param("attachmentId"),
	)
	if err != nil {
		writeAppError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"attachment": resp})
}

// handleDeleteAttachment handles
// DELETE /api/v1/expenses/:id/attachments/:attachmentId.
func (s *Server) handleDeleteAttachment(c *gin.Context) {
	err := s.attachmentService.DeleteAttachment(
		c.Request.Context(), getAuthUserID(c), getAuthOrgID(c), c.Param("id"), c.Param("attachmentId"),
	)
	if err != nil {
		writeAppError(c, err)
		return
	}
	c.Status(http.StatusNoContent)
}
