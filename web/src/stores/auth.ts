import { defineStore } from 'pinia'
import type { Organisation, User } from '@/types/auth'
import {
  login as loginRequest,
  listMyOrganisations,
  switchOrganisation as switchOrganisationRequest,
} from '@/services/auth.service'
import { resetFraudSignals } from '@/lib/fraudSignals'

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
  // Every organisation the user can switch to (populated on demand for the
  // top-bar switcher). NOT persisted — it's cheap to re-fetch and can go stale.
  organisations: Organisation[]
  // Epoch ms when the token expires; null when unknown (the backend doesn't
  // send an expiry today, and the encrypted token can't be read client-side).
  expiresAt: number | null
}

export const useAuthStore = defineStore('auth', {
  state: (): AuthState => ({
    token: null,
    user: null,
    organisation: null,
    organisations: [],
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

    // The switcher is only worth showing when the user belongs to more than one
    // organisation. (Reads the fetched list, so call loadMyOrganisations first.)
    canSwitchOrganisation: (state): boolean => state.organisations.length > 1,
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

    // Fetch the organisations the user can switch to (for the top-bar switcher).
    // Cheap and always fresh — call it when the account menu opens.
    async loadMyOrganisations(): Promise<void> {
      this.organisations = await listMyOrganisations()
    },

    // Re-scope the session to another organisation the user belongs to. The
    // backend re-mints the token (same body as login), which we store in place of
    // the current session — preserving the user's "keep me logged in" choice by
    // writing back to whichever storage currently holds the session.
    //
    // The caller is expected to force a full page reload afterwards so all
    // org-scoped caches (TanStack Query) are dropped and refetched for the new org.
    async switchOrganisation(organisationId: string): Promise<void> {
      const res = await switchOrganisationRequest(organisationId)
      const keepLoggedIn = localStorage.getItem(STORAGE_KEY) !== null
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

    // Keep the cached organisation summary (the name shown in the top bar, and
    // the country_code that drives country-scoped features) in sync after the
    // Company Details screen saves a change. Mutating the reactive state updates
    // the top bar immediately; we also re-persist into whichever storage holds
    // the session so a reload keeps the new values. No-op if there's no session.
    patchOrganisationSummary(patch: { name?: string; country_code?: string }): void {
      if (!this.organisation) return
      if (patch.name !== undefined) this.organisation.name = patch.name
      if (patch.country_code !== undefined) this.organisation.country_code = patch.country_code

      const storage = localStorage.getItem(STORAGE_KEY)
        ? localStorage
        : sessionStorage.getItem(STORAGE_KEY)
          ? sessionStorage
          : null
      if (!storage || !this.token || !this.user) return
      const persisted: PersistedSession = {
        token: this.token,
        user: this.user,
        organisation: this.organisation,
        expiresAt: this.expiresAt,
      }
      storage.setItem(STORAGE_KEY, JSON.stringify(persisted))
    },

    // Keep the cached user's display name (shown in the top-bar account dropdown)
    // in sync after the My Details screen saves a change. Mirrors
    // patchOrganisationSummary: mutating the reactive state updates the dropdown
    // immediately, and we re-persist into whichever storage holds the session so a
    // reload keeps the new values. No-op if there's no session.
    patchUser(patch: { first_name?: string; last_name?: string }): void {
      if (!this.user) return
      if (patch.first_name !== undefined) this.user.first_name = patch.first_name
      if (patch.last_name !== undefined) this.user.last_name = patch.last_name

      const storage = localStorage.getItem(STORAGE_KEY)
        ? localStorage
        : sessionStorage.getItem(STORAGE_KEY)
          ? sessionStorage
          : null
      if (!storage || !this.token) return
      const persisted: PersistedSession = {
        token: this.token,
        user: this.user,
        organisation: this.organisation,
        expiresAt: this.expiresAt,
      }
      storage.setItem(STORAGE_KEY, JSON.stringify(persisted))
    },

    // Client-side logout: discard the session everywhere. NOTE: with stateless
    // PASETO and no denylist, the token stays technically valid server-side
    // until it expires — true revocation is "Phase 2 auth".
    logout(): void {
      this.token = null
      this.user = null
      this.organisation = null
      this.organisations = []
      this.expiresAt = null
      localStorage.removeItem(STORAGE_KEY)
      sessionStorage.removeItem(STORAGE_KEY)
      // Drop the in-memory HMRC fraud-signal pack so a different user on this tab
      // rebuilds it (the persisted device id is intentionally kept).
      resetFraudSignals()
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
