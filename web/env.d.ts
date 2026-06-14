/// <reference types="vite/client" />

// Type our custom Vite env vars so `import.meta.env.VITE_API_BASE_URL` is typed.
interface ImportMetaEnv {
  readonly VITE_API_BASE_URL: string
  // Custom-VAT cap as a % of the total (see ExpenseEntryView); defaults to 30.
  readonly VITE_VAT_MAX_PERCENT: string
  // Max receipt-attachment size in MB (see AttachmentsField); defaults to 20.
  readonly VITE_MAX_UPLOAD_MB: string
}

interface ImportMeta {
  readonly env: ImportMetaEnv
}
