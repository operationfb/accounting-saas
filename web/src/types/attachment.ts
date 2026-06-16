import { z } from 'zod'

// Mirrors the backend's AttachmentResponse (attachment_service.go). One row of
// receipt metadata — the file BYTES live in GCS, never here; this is only the
// metadata Postgres stores. `description` is omitted by the API when null, so
// it's optional/nullable. Sizes are plain integers (bytes), formatted for
// display with formatBytes().
export const AttachmentSchema = z.object({
  id: z.string(),
  expense_id: z.string(),
  file_name: z.string(),
  content_type: z.string(), // MIME, server-sniffed: application/pdf | image/jpeg | image/png
  file_size_bytes: z.number(),
  is_primary: z.boolean(),
  description: z.string().nullish(),
  uploaded_by_user_id: z.string(),
  created_at: z.string(), // RFC3339
  // OCR fields (Smart Upload). ocr_status drives the capture-polling state machine;
  // the rest are for badges/audit. Absent on ordinary (non-OCR) attachments → nullish.
  ocr_status: z.string().nullish(), // PENDING | PROCESSING | COMPLETE | FAILED | SKIPPED
  ocr_extracted_data: z.unknown().nullish(),
  ocr_processed_at: z.string().nullish(), // RFC3339, set when OCR reaches a terminal state
})
export type Attachment = z.infer<typeof AttachmentSchema>

// GET /api/v1/expenses/:id/attachments → { "attachments": [...] }. An empty
// list can come back as null (Go marshals a nil slice to null), so allow null
// and default to [] at the call site — same convention as the expenses list.
export const ListAttachmentsResponseSchema = z.object({
  attachments: z.array(AttachmentSchema).nullish(),
})

// POST (upload) and PATCH (set-primary) both return the single affected row.
export const AttachmentEnvelopeSchema = z.object({
  attachment: AttachmentSchema,
})

// GET .../:attachmentId/download → { "download_url": "<short-lived GCS URL>" }.
// The URL is signed and expires (~15 min) — fetch it on demand, never store it.
export const DownloadUrlResponseSchema = z.object({
  download_url: z.string(),
})
