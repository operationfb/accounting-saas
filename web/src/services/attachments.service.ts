import { apiFetch, apiUpload } from '@/lib/api'
import {
  ListAttachmentsResponseSchema,
  AttachmentEnvelopeSchema,
  DownloadUrlResponseSchema,
  type Attachment,
} from '@/types/attachment'

// All endpoints are nested under an expense and auth-gated (claimant or org
// admin). The bearer token + 401 handling come from apiFetch/apiUpload. Ids are
// URL-encoded defensively, mirroring the expenses service.

const base = (expenseId: string) => `/expenses/${encodeURIComponent(expenseId)}/attachments`

// GET .../attachments — metadata only (primary first, then oldest→newest). An
// empty list may arrive as null, so default to [].
export async function listAttachments(expenseId: string): Promise<Attachment[]> {
  const data = await apiFetch<unknown>(base(expenseId), { method: 'GET' })
  return ListAttachmentsResponseSchema.parse(data).attachments ?? []
}

// POST .../attachments — multipart upload. Sends the raw File under `file` and
// the optional `description`; the backend sniffs the real MIME type (it ignores
// the filename/Content-Type), enforces the size limit, and makes the FIRST file
// uploaded to an expense its primary. Returns the created row.
export async function uploadAttachment(
  expenseId: string,
  file: File,
  description?: string,
): Promise<Attachment> {
  const form = new FormData()
  form.append('file', file)
  if (description && description.trim()) form.append('description', description.trim())
  const data = await apiUpload<unknown>(base(expenseId), form)
  return AttachmentEnvelopeSchema.parse(data).attachment
}

// GET .../:attachmentId/download — returns a short-lived (~15 min) signed GCS
// URL. Fetch it at click time and open it; never persist it.
export async function getDownloadUrl(expenseId: string, attachmentId: string): Promise<string> {
  const path = `${base(expenseId)}/${encodeURIComponent(attachmentId)}/download`
  const data = await apiFetch<unknown>(path, { method: 'GET' })
  return DownloadUrlResponseSchema.parse(data).download_url
}

// PATCH .../:attachmentId/primary — marks this attachment primary and clears the
// flag on the others (one transaction). Returns the now-primary row.
export async function setPrimaryAttachment(
  expenseId: string,
  attachmentId: string,
): Promise<Attachment> {
  const path = `${base(expenseId)}/${encodeURIComponent(attachmentId)}/primary`
  const data = await apiFetch<unknown>(path, { method: 'PATCH' })
  return AttachmentEnvelopeSchema.parse(data).attachment
}

// DELETE .../:attachmentId — removes the row + the stored object. If it was the
// primary and others remain, the backend promotes the oldest. 204, no body.
export async function deleteAttachment(expenseId: string, attachmentId: string): Promise<void> {
  const path = `${base(expenseId)}/${encodeURIComponent(attachmentId)}`
  await apiFetch<unknown>(path, { method: 'DELETE' })
}
