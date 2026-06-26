package vat

// fraud_test.go
// =============================================================================
// Pure unit tests for the HMRC fraud-header assembly — no DB, no real HTTP. They
// pin the exact HMRC formats (timezone, screens, percent-encoding, forwarded) so a
// formatting regression is caught here rather than by HMRC's validator in prod.
// =============================================================================

import (
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

func ptr[T any](v T) *T { return &v }

func TestFormatTimezone(t *testing.T) {
	cases := []struct {
		min  int
		want string
	}{
		{60, "UTC+01:00"},   // BST
		{0, "UTC+00:00"},    // GMT
		{-300, "UTC-05:00"}, // US Eastern
		{330, "UTC+05:30"},  // IST (half-hour offset)
		{-30, "UTC-00:30"},
	}
	for _, c := range cases {
		if got := formatTimezone(c.min); got != c.want {
			t.Errorf("formatTimezone(%d) = %q, want %q", c.min, got, c.want)
		}
	}
}

func TestPctEncode(t *testing.T) {
	// HMRC wants RFC-3986 percent-encoding: space is %20, not '+'.
	if got := pctEncode("a b"); got != "a%20b" {
		t.Errorf(`pctEncode("a b") = %q, want "a%%20b"`, got)
	}
	if got := pctEncode("Mozilla/5.0 (X11)"); strings.Contains(got, "+") {
		t.Errorf("pctEncode must not emit '+', got %q", got)
	}
}

func TestFormatFloat(t *testing.T) {
	// Scaling factor: no trailing zeros.
	for in, want := range map[float64]string{2: "2", 1.5: "1.5", 1: "1"} {
		if got := formatFloat(in); got != want {
			t.Errorf("formatFloat(%v) = %q, want %q", in, got, want)
		}
	}
}

func TestBuildFraudHeaders(t *testing.T) {
	gin.SetMode(gin.TestMode)

	signals := clientFraudSignals{
		DeviceID:         "11111111-1111-1111-1111-111111111111",
		UTCOffsetMinutes: ptr(60),
		Screens:          []screenInfo{{Width: 1920, Height: 1080, ScalingFactor: 2, ColourDepth: 24}},
		WindowSize:       &windowSize{Width: 1280, Height: 720},
		UserAgent:        "Mozilla/5.0 (Macintosh)",
		DoNotTrack:       ptr(false),
		Plugins:          []string{},
		LocalIPs:         []string{"192.168.1.10"},
		LocalIPsTime:     "2026-06-26T10:00:00.000Z",
	}
	raw, _ := json.Marshal(signals)

	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	req := httptest.NewRequest("POST", "/api/v1/vat/returns/x/submit", nil)
	req.RemoteAddr = "203.0.113.7:50000" // no X-Forwarded-For → ClientIP is this host
	req.Header.Set(clientSignalsHeader, string(raw))
	c.Request = req

	cfg := FraudConfig{ProductName: "Kontala", Version: "1.2.3", VendorPublicIP: "198.51.100.2", ConnectionMethod: "WEB_APP_VIA_SERVER"}
	userID := uuid.MustParse("22222222-2222-2222-2222-222222222222")
	h := buildFraudHeaders(c, cfg, userID)

	want := map[string]string{
		"Gov-Client-Connection-Method":     "WEB_APP_VIA_SERVER",
		"Gov-Client-User-IDs":              "kontala=22222222-2222-2222-2222-222222222222",
		"Gov-Client-Public-IP":             "203.0.113.7",
		"Gov-Client-Public-Port":           "50000",
		"Gov-Client-Device-ID":             "11111111-1111-1111-1111-111111111111",
		"Gov-Client-Timezone":              "UTC+01:00",
		"Gov-Client-Screens":               "width=1920&height=1080&scaling-factor=2&colour-depth=24",
		"Gov-Client-Window-Size":           "width=1280&height=720",
		"Gov-Client-Browser-JS-User-Agent": "Mozilla/5.0 (Macintosh)", // RAW, not percent-encoded
		"Gov-Client-Browser-Do-Not-Track":  "false",
		"Gov-Client-Browser-Plugins":       "",
		"Gov-Client-Local-IPs":             "192.168.1.10",
		"Gov-Client-Local-IPs-Timestamp":   "2026-06-26T10:00:00.000Z",
		"Gov-Vendor-Product-Name":          "Kontala",
		"Gov-Vendor-Version":               "Kontala=1.2.3",
		"Gov-Vendor-License-IDs":           "",
		"Gov-Vendor-Public-IP":             "198.51.100.2",
		"Gov-Vendor-Forwarded":             "by=198.51.100.2&for=203.0.113.7",
	}
	for k, v := range want {
		if h[k] != v {
			t.Errorf("header %s = %q, want %q", k, h[k], v)
		}
	}
	if h["Gov-Client-Public-IP-Timestamp"] == "" {
		t.Error("Gov-Client-Public-IP-Timestamp should be set")
	}
}

// When the egress IP isn't provisioned (blank), the vendor-IP headers are omitted
// rather than sent wrong — the rest still assemble.
func TestBuildFraudHeaders_NoVendorIP(t *testing.T) {
	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	req := httptest.NewRequest("GET", "/api/v1/vat/hmrc/obligations", nil)
	req.RemoteAddr = "203.0.113.7:50000"
	c.Request = req

	h := buildFraudHeaders(c, FraudConfig{ProductName: "Kontala", Version: "1.0.0"}, uuid.New())

	if _, ok := h["Gov-Vendor-Public-IP"]; ok {
		t.Error("Gov-Vendor-Public-IP should be omitted when egress IP is blank")
	}
	if _, ok := h["Gov-Vendor-Forwarded"]; ok {
		t.Error("Gov-Vendor-Forwarded should be omitted when egress IP is blank")
	}
	if h["Gov-Client-Connection-Method"] != "WEB_APP_VIA_SERVER" {
		t.Error("default connection method should still be set")
	}
}

// A garbage X-Client-Fraud-Signals blob must not break assembly — the server-side
// headers still go.
func TestBuildFraudHeaders_BadSignals(t *testing.T) {
	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	req := httptest.NewRequest("GET", "/api/v1/vat/hmrc/obligations", nil)
	req.RemoteAddr = "203.0.113.7:50000"
	req.Header.Set(clientSignalsHeader, "{not json")
	c.Request = req

	h := buildFraudHeaders(c, FraudConfig{ProductName: "Kontala"}, uuid.New())
	if h["Gov-Client-Public-IP"] != "203.0.113.7" {
		t.Errorf("server-side headers should survive a bad signals blob; got IP %q", h["Gov-Client-Public-IP"])
	}
	if _, ok := h["Gov-Client-Device-ID"]; ok {
		t.Error("no device id should be set from a garbage blob")
	}
}
