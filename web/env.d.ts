/// <reference types="vite/client" />

// Type our custom Vite env vars so `import.meta.env.VITE_API_BASE_URL` is typed.
interface ImportMetaEnv {
  readonly VITE_API_BASE_URL: string
}

interface ImportMeta {
  readonly env: ImportMetaEnv
}
