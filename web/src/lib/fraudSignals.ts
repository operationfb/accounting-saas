// HMRC fraud-prevention — the browser half.
// =============================================================================
// Our connection method is WEB_APP_VIA_SERVER, so HMRC requires data only the
// user's browser knows (device, screen, timezone, IPs…). We collect it here and
// send it as ONE JSON header — X-Client-Fraud-Signals — on HMRC-bound API calls;
// the Go server formats it into HMRC's exact "Gov-Client-*" headers.
//
// One pack per session: getFraudSignals() memoises the in-flight PROMISE, so the
// pre-warm and the real call dedupe to a single collection — the slow part (the
// WebRTC local-IP gather) runs at most once. `deviceId` persists across sessions in
// localStorage (a stable device identity is the point); everything else is in-memory
// only, so volatile fields can't go stale from disk. Cleared on logout.
// =============================================================================

const DEVICE_ID_KEY = 'kontala.hmrcDeviceId'
const LOCAL_IP_TIMEOUT_MS = 1000

export interface FraudSignals {
  deviceId: string
  utcOffsetMinutes: number // minutes EAST of UTC (+60 = UTC+01:00); server formats it
  screens: { width: number; height: number; scalingFactor: number; colourDepth: number }[]
  windowSize: { width: number; height: number }
  userAgent: string
  doNotTrack: boolean
  plugins: string[]
  localIps?: string[]
  localIpsTimestamp?: string // ISO8601, set only when localIps is non-empty
}

// The session cache is the in-flight promise itself, so concurrent callers (pre-warm
// + the real request) share one collection.
let packPromise: Promise<FraudSignals> | null = null

// getFraudSignals returns the session-cached signal pack, building it once (lazily).
export function getFraudSignals(): Promise<FraudSignals> {
  return (packPromise ??= buildPack())
}

// prewarmFraudSignals starts collection early (fire-and-forget) so the slow WebRTC
// gather overlaps with the user reading a page/modal. A no-op once the pack exists —
// safe to call from several places; collection happens at most once per session.
export function prewarmFraudSignals(): void {
  void getFraudSignals()
}

// resetFraudSignals drops the in-memory pack — call on logout so a different user on
// the same tab rebuilds it. The persisted `deviceId` is intentionally left in place.
export function resetFraudSignals(): void {
  packPromise = null
}

async function buildPack(): Promise<FraudSignals> {
  const localIps = await gatherLocalIPs()
  const pack: FraudSignals = {
    deviceId: getOrCreateDeviceId(),
    utcOffsetMinutes: -new Date().getTimezoneOffset(), // JS returns minutes BEHIND UTC; negate
    screens: [
      {
        width: window.screen.width,
        height: window.screen.height,
        scalingFactor: window.devicePixelRatio || 1,
        colourDepth: window.screen.colorDepth,
      },
    ],
    windowSize: { width: window.innerWidth, height: window.innerHeight },
    userAgent: navigator.userAgent,
    doNotTrack: navigator.doNotTrack === '1' || navigator.doNotTrack === 'yes',
    plugins: Array.from(navigator.plugins ?? []).map((p) => p.name),
  }
  if (localIps.length > 0) {
    pack.localIps = localIps
    pack.localIpsTimestamp = new Date().toISOString()
  }
  return pack
}

// getOrCreateDeviceId returns a stable per-browser UUID, persisted in localStorage.
// Falls back to an ephemeral id if storage is blocked (private mode).
function getOrCreateDeviceId(): string {
  try {
    const existing = localStorage.getItem(DEVICE_ID_KEY)
    if (existing) return existing
    const id = crypto.randomUUID()
    localStorage.setItem(DEVICE_ID_KEY, id)
    return id
  } catch {
    return crypto.randomUUID()
  }
}

// gatherLocalIPs best-effort collects the device's private IPs via WebRTC. Modern
// browsers obfuscate these behind mDNS (.local) hostnames, which we drop — so this is
// often empty, and HMRC accepts a documented omission. Hard-capped so it never hangs.
async function gatherLocalIPs(): Promise<string[]> {
  if (typeof RTCPeerConnection === 'undefined') return []
  return new Promise<string[]>((resolve) => {
    const ips = new Set<string>()
    let pc: RTCPeerConnection
    try {
      pc = new RTCPeerConnection({ iceServers: [] })
    } catch {
      resolve([])
      return
    }
    let settled = false
    const finish = () => {
      if (settled) return
      settled = true
      clearTimeout(timer)
      try {
        pc.close()
      } catch {
        /* ignore */
      }
      resolve([...ips])
    }
    const timer = setTimeout(finish, LOCAL_IP_TIMEOUT_MS)
    pc.onicecandidate = (e) => {
      if (!e.candidate) {
        finish() // gathering complete
        return
      }
      // Pull an IPv4 dotted-quad or an IPv6 (has a colon) out of the candidate line.
      const m = /(\d{1,3}(?:\.\d{1,3}){3}|[a-f0-9]*:[a-f0-9:]+)/i.exec(e.candidate.candidate)
      const ip = m?.[0]
      if (ip && !ip.endsWith('.local')) ips.add(ip)
    }
    try {
      pc.createDataChannel('probe')
      pc.createOffer()
        .then((o) => pc.setLocalDescription(o))
        .catch(finish)
    } catch {
      finish()
    }
  })
}
