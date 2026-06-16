import { defineStore } from 'pinia'
import type { Organisation, User } from '@/types/auth'
import { login as loginRequest } from '@/services/auth.service'

// One JSON blob persisted under this key, in local- OR sessionStorage.
const STORAGE_KEY = 'auth'

interface PersistedSession {
  token: string
  user: User
  organisation: Organisation | null
  expiresAt: number | null
}

interface AuthState {
  token: string | null
  user: User | null
  // The organisation the session is scoped to (its name is shown in the top bar).
  organisation: Organisation | null
  // Epoch ms when the token expires; null when unknown (the backend doesn't
  // send an expiry today, and the encrypted token can't be read client-side).
  expiresAt: number | null
}

export const useAuthStore = defineStore('auth', {
  state: (): AuthState => ({
    token: null,
    user: null,
    organisation: null,
    expiresAt: null,
  }),

  getters: {
    // We only KNOW a token is expired if the backend told us when (expiresAt).
    // Otherwise we trust it until the server rejects a request with 401.
    isAuthenticated: (state): boolean =>
      !!state.token && (state.expiresAt === null || Date.now() < state.expiresAt),

    // Whether the caller may act as an organisation admin — e.g. file an expense
    // on behalf of another user. Mirrors the backend isOrgAdmin: owner or admin
    // only. The role is per-organisation and arrives in the login response
    // (organisation.role), so it reads off the scoped organisation.
    isOrgAdmin: (state): boolean =>
      state.organisation?.role === 'owner' || state.organisation?.role === 'admin',
  },

  actions: {
    async login(email: string, password: string, keepLoggedIn: boolean): Promise<void> {
      const res = await loginRequest(email, password)
      this.setSession(
        res.access_token,
        res.user,
        res.organisation ?? null,
        res.expires_in ?? null,
        keepLoggedIn,
      )
    },

    // keepLoggedIn → localStorage (survives a browser restart, shared across
    // tabs); otherwise sessionStorage (per-tab, cleared when the tab closes).
    // We write to one and clear the other so a stale copy can't linger.
    setSession(
      token: string,
      user: User,
      organisation: Organisation | null,
      expiresInSeconds: number | null,
      keepLoggedIn: boolean,
    ): void {
      this.token = token
      this.user = user
      this.organisation = organisation
      this.expiresAt = expiresInSeconds ? Date.now() + expiresInSeconds * 1000 : null

      const persisted: PersistedSession = {
        token,
        user,
        organisation,
        expiresAt: this.expiresAt,
      }
      const primary = keepLoggedIn ? localStorage : sessionStorage
      const other = keepLoggedIn ? sessionStorage : localStorage
      other.removeItem(STORAGE_KEY)
      primary.setItem(STORAGE_KEY, JSON.stringify(persisted))
    },

    // Client-side logout: discard the session everywhere. NOTE: with stateless
    // PASETO and no denylist, the token stays technically valid server-side
    // until it expires — true revocation is "Phase 2 auth".
    logout(): void {
      this.token = null
      this.user = null
      this.organisation = null
      this.expiresAt = null
      localStorage.removeItem(STORAGE_KEY)
      sessionStorage.removeItem(STORAGE_KEY)
    },

    // Re-hydrate from storage on app boot (local first, then session).
    loadFromStorage(): void {
      const raw = localStorage.getItem(STORAGE_KEY) ?? sessionStorage.getItem(STORAGE_KEY)
      if (!raw) return
      try {
        const session = JSON.parse(raw) as PersistedSession
        // Drop a known-expired session rather than restoring it.
        if (session.expiresAt && Date.now() >= session.expiresAt) {
          this.logout()
          return
        }
        this.token = session.token
        this.user = session.user
        this.organisation = session.organisation ?? null
        this.expiresAt = session.expiresAt ?? null
      } catch {
        // Corrupt blob — clear it.
        this.logout()
      }
    },
  },
})
