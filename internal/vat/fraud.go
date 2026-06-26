package vat

// fraud.go
// =============================================================================
// HMRC fraud-prevention headers (Gov-Client-* / Gov-Vendor-*). HMRC requires
// these on EVERY MTD API call in production; our connection method is
// WEB_APP_VIA_SERVER (browser → our server → HMRC), which means a chunk of the
// data is collected in the user's browser and forwarded through us.
//
// Pipeline:
//   1. The SPA sends its browser-collected signals as ONE JSON header,
//      X-Client-Fraud-Signals (see web/src/lib/fraudSignals.ts).
//   2. fraudHeadersMiddleware (on the VAT route group) parses that, adds the
//      SERVER-derived values (client public IP, timestamps, user id, vendor
//      identity, forwarded chain), formats everything to HMRC's exact header
//      shapes, and stashes the assembled map in the request context.
//   3. Every outbound HMRC request (hmrcGet / postHMRCReturn / listHMRCObligations)
//      calls applyFraudHeaders(req, ctx) to set them.
//
// Robustness: a missing/garbage signal just omits that one header — we never
// fail a VAT request over a fraud header. The sandbox doesn't enforce them; we
// validate format against HMRC's Test Fraud Prevention Headers API.
//
// Encoding: values are percent-encoded per RFC 3986 (space → %20, not '+'); the
// structured headers (screens/window/forwarded) keep their literal '&'/'=' and
// only percent-encode the sub-values that need it.
// =============================================================================

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"

	"github.com/operationfb/accounting-saas/internal/kernel"
)

// defaultConnectionMethod is our topology: the user's browser talks to our server,
// and our server calls HMRC.
const defaultConnectionMethod = "WEB_APP_VIA_SERVER"

// userIDKey labels our user identifier inside Gov-Client-User-IDs (the key is the
// vendor's own choice; HMRC just wants a stable key=value).
const userIDKey = "kontala"

// clientSignalsHeader is the single consolidated header the SPA sends.
const clientSignalsHeader = "X-Client-Fraud-Signals"

// FraudConfig is the static vendor identity + connection method, from env (main.go).
// VendorPublicIP is our Cloud Run STATIC EGRESS IP; empty means "not provisioned
// yet" → the vendor-IP-derived headers are omitted rather than sent wrong.
type FraudConfig struct {
	ProductName      string
	Version          string
	VendorPublicIP   string
	ConnectionMethod string
}

// clientFraudSignals is the browser-collected pack (the X-Client-Fraud-Signals JSON).
// Every field is optional — the SPA sends raw values and the server formats them, so
// a missing field simply omits its header. Pointers/slices distinguish "absent".
type clientFraudSignals struct {
	DeviceID         string       `json:"deviceId"`
	UTCOffsetMinutes *int         `json:"utcOffsetMinutes"` // minutes EAST of UTC (+60 = UTC+01:00)
	Screens          []screenInfo `json:"screens"`
	WindowSize       *windowSize  `json:"windowSize"`
	UserAgent        string       `json:"userAgent"`
	DoNotTrack       *bool        `json:"doNotTrack"`
	Plugins          []string     `json:"plugins"`
	LocalIPs         []string     `json:"localIps"`
	LocalIPsTime     string       `json:"localIpsTimestamp"` // ISO8601, already formatted by the SPA
}

type screenInfo struct {
	Width         int     `json:"width"`
	Height        int     `json:"height"`
	ScalingFactor float64 `json:"scalingFactor"`
	ColourDepth   int     `json:"colourDepth"`
}

type windowSize struct {
	Width  int `json:"width"`
	Height int `json:"height"`
}

// =============================================================================
// CONTEXT PLUMBING
// =============================================================================

type fraudCtxKey struct{}

func withFraudHeaders(ctx context.Context, h map[string]string) context.Context {
	return context.WithValue(ctx, fraudCtxKey{}, h)
}

func fraudHeadersFromContext(ctx context.Context) map[string]string {
	h, _ := ctx.Value(fraudCtxKey{}).(map[string]string)
	return h
}

// applyFraudHeaders sets every assembled fraud header on an outbound HMRC request.
// A no-op when the context carries none (e.g. a call made outside a user request),
// so HMRC calls never break for lack of headers.
func applyFraudHeaders(req *http.Request, ctx context.Context) {
	for k, v := range fraudHeadersFromContext(ctx) {
		req.Header.Set(k, v)
	}
}

// =============================================================================
// MIDDLEWARE
// =============================================================================

// fraudHeadersMiddleware assembles the Gov-* header set for the request and stores
// it in the request context. Mounted on the VAT group AFTER AuthMiddleware (it needs
// the authenticated user id). Cheap on non-HMRC VAT routes — the browser only sends
// X-Client-Fraud-Signals on HMRC-bound calls, so otherwise just the server bits run.
func fraudHeadersMiddleware(cfg FraudConfig) gin.HandlerFunc {
	return func(c *gin.Context) {
		h := buildFraudHeaders(c, cfg, kernel.GetAuthUserID(c))
		c.Request = c.Request.WithContext(withFraudHeaders(c.Request.Context(), h))
		c.Next()
	}
}

// =============================================================================
// ASSEMBLY
// =============================================================================

// buildFraudHeaders assembles the full Gov-* set from the server-side values
// (client IP, timestamps, user id, vendor identity, forwarded) plus the browser
// signals in X-Client-Fraud-Signals. Pure-ish (reads only c) so it unit-tests with
// a synthetic gin.Context.
func buildFraudHeaders(c *gin.Context, cfg FraudConfig, userID uuid.UUID) map[string]string {
	h := make(map[string]string, 20)
	nowISO := time.Now().UTC().Format("2006-01-02T15:04:05.000Z")

	// --- Connection method (static) ---
	method := cfg.ConnectionMethod
	if method == "" {
		method = defaultConnectionMethod
	}
	h["Gov-Client-Connection-Method"] = method

	// --- Our user id ---
	if userID != uuid.Nil {
		h["Gov-Client-User-IDs"] = userIDKey + "=" + pctEncode(userID.String())
	}

	// --- The client's public IP, as our server saw it (+ when) ---
	clientIP := c.ClientIP()
	if clientIP != "" {
		h["Gov-Client-Public-IP"] = clientIP
		h["Gov-Client-Public-IP-Timestamp"] = nowISO
	}

	// --- The client's source port (best-effort from the connection) ---
	// Correct for a direct connection; behind Cloud Run's proxy this is the proxy's
	// port, not the user's (a documented HMRC limitation — see BACKLOG). Sent when
	// derivable so the header isn't missing.
	if _, port, err := net.SplitHostPort(c.Request.RemoteAddr); err == nil && port != "" {
		h["Gov-Client-Public-Port"] = port
	}

	// --- Vendor identity (static config) ---
	if cfg.ProductName != "" {
		h["Gov-Vendor-Product-Name"] = pctEncode(cfg.ProductName)
		if cfg.Version != "" {
			// HMRC format: "<software>=<version>".
			h["Gov-Vendor-Version"] = pctEncode(cfg.ProductName) + "=" + pctEncode(cfg.Version)
		}
	}
	// HMRC requires this header even for a vendor that holds no licences — send it
	// empty (key=value pairs of "<software>=<licenceId>" when we ever have any).
	h["Gov-Vendor-License-IDs"] = ""

	// --- Vendor public IP + the forwarded hop (only when the egress IP is known) ---
	if cfg.VendorPublicIP != "" {
		h["Gov-Vendor-Public-IP"] = cfg.VendorPublicIP
		if clientIP != "" {
			h["Gov-Vendor-Forwarded"] = "by=" + pctEncode(cfg.VendorPublicIP) + "&for=" + pctEncode(clientIP)
		}
	}

	// --- Browser-collected signals (optional) ---
	if raw := c.GetHeader(clientSignalsHeader); raw != "" {
		var s clientFraudSignals
		if err := json.Unmarshal([]byte(raw), &s); err == nil {
			applyClientSignals(h, s)
		}
		// A malformed blob is ignored — the server-side headers above still go.
	}

	return h
}

// applyClientSignals formats the browser-collected values into their HMRC headers.
// Each field is independent: an absent one just omits its header.
func applyClientSignals(h map[string]string, s clientFraudSignals) {
	if s.DeviceID != "" {
		h["Gov-Client-Device-ID"] = s.DeviceID
	}
	if s.UTCOffsetMinutes != nil {
		h["Gov-Client-Timezone"] = formatTimezone(*s.UTCOffsetMinutes)
	}
	if len(s.Screens) > 0 {
		parts := make([]string, 0, len(s.Screens))
		for _, sc := range s.Screens {
			parts = append(parts, fmt.Sprintf("width=%d&height=%d&scaling-factor=%s&colour-depth=%d",
				sc.Width, sc.Height, formatFloat(sc.ScalingFactor), sc.ColourDepth))
		}
		h["Gov-Client-Screens"] = strings.Join(parts, ",")
	}
	if s.WindowSize != nil {
		h["Gov-Client-Window-Size"] = fmt.Sprintf("width=%d&height=%d", s.WindowSize.Width, s.WindowSize.Height)
	}
	if s.UserAgent != "" {
		// RAW, not percent-encoded — HMRC parses product/version/platform out of it
		// and explicitly rejects an encoded value.
		h["Gov-Client-Browser-JS-User-Agent"] = s.UserAgent
	}
	if s.DoNotTrack != nil {
		h["Gov-Client-Browser-Do-Not-Track"] = strconv.FormatBool(*s.DoNotTrack)
	}
	// Plugins: comma-separated, each percent-encoded. We always send the header (an
	// empty value is the correct representation of "no plugins" for modern browsers).
	pluginParts := make([]string, 0, len(s.Plugins))
	for _, p := range s.Plugins {
		pluginParts = append(pluginParts, pctEncode(p))
	}
	h["Gov-Client-Browser-Plugins"] = strings.Join(pluginParts, ",")

	if len(s.LocalIPs) > 0 {
		h["Gov-Client-Local-IPs"] = strings.Join(s.LocalIPs, ",")
		if s.LocalIPsTime != "" {
			h["Gov-Client-Local-IPs-Timestamp"] = s.LocalIPsTime
		}
	}
}

// =============================================================================
// FORMAT HELPERS
// =============================================================================

// formatTimezone renders minutes-east-of-UTC as HMRC's "UTC±HH:MM" (e.g. +60 →
// "UTC+01:00", -300 → "UTC-05:00", 0 → "UTC+00:00").
func formatTimezone(offsetMin int) string {
	sign := "+"
	if offsetMin < 0 {
		sign = "-"
		offsetMin = -offsetMin
	}
	return fmt.Sprintf("UTC%s%02d:%02d", sign, offsetMin/60, offsetMin%60)
}

// formatFloat renders a scaling factor without trailing zeros ("1", "1.5", "2").
func formatFloat(f float64) string {
	return strconv.FormatFloat(f, 'f', -1, 64)
}

// pctEncode percent-encodes a value per RFC 3986 — like url.QueryEscape but with
// space as %20 (not '+'), which is what HMRC expects.
func pctEncode(s string) string {
	return strings.ReplaceAll(url.QueryEscape(s), "+", "%20")
}
