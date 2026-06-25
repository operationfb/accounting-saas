package vat

// hmrc.go
// =============================================================================
// HMRC MTD VAT API — types and HTTP helpers used by SubmitReturn.
//
// The auth-only OAuth part of the HMRC integration lives in
// internal/integrations/hmrc (same pattern as FreeAgent). This file covers
// the VAT-domain calls:
//   - GET  {apiBaseURL}/{vrn}/obligations  — find the HMRC periodKey for a period
//   - POST {apiBaseURL}/{vrn}/returns      — submit the 9-box return
//
// HMRC API facts:
//   - Accept header required: "application/vnd.hmrc.1.0+json"
//   - Boxes 1–5: decimal pounds to 2 dp (e.g. 105.50)
//   - Boxes 6–9: integer whole pounds (e.g. 1054)
//   - netVatDue (box5): always the POSITIVE magnitude (no sign) — HMRC treats
//     negative Box5 as a repayment via the paymentIndicator field, not a sign
//
// NOTE: HMRC fraud-prevention headers (Gov-Client-Connection-Method etc.) are
// REQUIRED for production but not enforced in the sandbox. They are deferred
// to a follow-up (see BACKLOG.md).
// =============================================================================

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"time"

	"github.com/operationfb/accounting-saas/internal/kernel"
)

// =============================================================================
// OBLIGATION TYPES
// =============================================================================

type hmrcObligationsResponse struct {
	Obligations []hmrcObligation `json:"obligations"`
}

type hmrcObligation struct {
	PeriodKey string `json:"periodKey"`
	Start     string `json:"start"` // YYYY-MM-DD
	End       string `json:"end"`   // YYYY-MM-DD
	Due       string `json:"due"`   // YYYY-MM-DD
	Status    string `json:"status"` // "O" = open, "F" = fulfilled
}

// =============================================================================
// RETURN REQUEST/RESPONSE TYPES
// =============================================================================

// hmrcReturnRequest is the JSON body for POST /organisations/vat/{vrn}/returns.
// Boxes 1-5 are pounds with up to 2dp (sent as float64 for HMRC's JSON; our
// internal representation is pence so we divide by 100). Boxes 6-9 are whole
// pounds (integers), rounded as per HMRC's requirement.
type hmrcReturnRequest struct {
	PeriodKey                    string  `json:"periodKey"`
	VatDueSales                  float64 `json:"vatDueSales"`                  // box1
	VatDueAcquisitions           float64 `json:"vatDueAcquisitions"`           // box2
	TotalVatDue                  float64 `json:"totalVatDue"`                  // box3
	VatReclaimedCurrPeriod       float64 `json:"vatReclaimedCurrPeriod"`       // box4
	NetVatDue                    float64 `json:"netVatDue"`                    // box5 (always positive magnitude)
	TotalValueSalesExVAT         int64   `json:"totalValueSalesExVAT"`         // box6, whole pounds
	TotalValuePurchasesExVAT     int64   `json:"totalValuePurchasesExVAT"`     // box7, whole pounds
	TotalValueGoodsSuppliedExVAT int64   `json:"totalValueGoodsSuppliedExVAT"` // box8, whole pounds
	TotalAcquisitionsExVAT       int64   `json:"totalAcquisitionsExVAT"`       // box9, whole pounds
	Finalised                    bool    `json:"finalised"`
}

// hmrcReturnResponse is the successful 200 response from HMRC after a return
// is accepted. FormBundleNumber is the HMRC reference the taxpayer keeps.
type hmrcReturnResponse struct {
	ProcessingDate   string `json:"processingDate"`   // ISO8601
	PaymentIndicator string `json:"paymentIndicator"` // "BANK" | "DD" | ""
	FormBundleNumber string `json:"formBundleNumber"`
	ChargeRefNumber  string `json:"chargeRefNumber"` // absent when nil/refund
}

// hmrcAPIError is the error envelope HMRC returns on 4xx/5xx.
type hmrcAPIError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

// =============================================================================
// OBLIGATION LOOKUP
// =============================================================================

// fetchHMRCObligation calls HMRC's /obligations endpoint and returns the
// obligation whose period-end date equals the given periodEnd (YYYY-MM-DD).
// Returns a 409 AppError if no matching open obligation is found.
func fetchHMRCObligation(ctx context.Context, client *http.Client, apiBaseURL, vrn, accessToken, periodEnd string) (*hmrcObligation, error) {
	// Query a 6-year window — wide enough to cover back-filings and the upcoming
	// period, without overfetching. HMRC /obligations filters by the obligation's
	// start date falling in [from, to].
	to := time.Now().AddDate(1, 0, 0).Format("2006-01-02")
	from := time.Now().AddDate(-5, 0, 0).Format("2006-01-02")

	u, _ := url.Parse(fmt.Sprintf("%s/%s/obligations", apiBaseURL, vrn))
	q := u.Query()
	q.Set("from", from)
	q.Set("to", to)
	// Note: omitting status=O returns ALL obligations (open + fulfilled), letting
	// us match already-filed periods for re-viewing. To submit, we only care that
	// an obligation EXISTS (the status guard is in the service).
	u.RawQuery = q.Encode()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return nil, kernel.ErrInternal(fmt.Errorf("build obligations request: %w", err))
	}
	req.Header.Set("Authorization", "Bearer "+accessToken)
	req.Header.Set("Accept", "application/vnd.hmrc.1.0+json")

	resp, err := client.Do(req)
	if err != nil {
		return nil, kernel.ErrInternal(fmt.Errorf("HMRC obligations call failed: %w", err))
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))

	if resp.StatusCode != http.StatusOK {
		return nil, hmrcHTTPError(resp.StatusCode, body)
	}

	var obResp hmrcObligationsResponse
	if err := json.Unmarshal(body, &obResp); err != nil {
		return nil, kernel.ErrInternal(fmt.Errorf("decode HMRC obligations: %w", err))
	}

	// Find the obligation matching the period's end date.
	for i := range obResp.Obligations {
		if obResp.Obligations[i].End == periodEnd {
			return &obResp.Obligations[i], nil
		}
	}
	return nil, kernel.ErrConflict(fmt.Sprintf(
		"no HMRC obligation found for period ending %s — make sure the period is open in HMRC", periodEnd,
	))
}

// =============================================================================
// RETURN SUBMISSION
// =============================================================================

// postHMRCReturn builds and sends the 9-box return to HMRC. boxes are in pence;
// this function converts to the pound/whole-pound values HMRC expects.
func postHMRCReturn(ctx context.Context, client *http.Client, apiBaseURL, vrn, accessToken, hmrcPeriodKey string, boxes vatBoxes) (*hmrcReturnResponse, error) {
	body := hmrcReturnRequest{
		PeriodKey: hmrcPeriodKey,
		// Boxes 1–5: divide pence by 100 to get pounds (2dp). Use float64 — the
		// HMRC schema allows up to 2dp; our values are always exact multiples of 1p.
		VatDueSales:            float64(boxes.Box1) / 100.0,
		VatDueAcquisitions:     float64(boxes.Box2) / 100.0,
		TotalVatDue:            float64(boxes.Box3) / 100.0,
		VatReclaimedCurrPeriod: float64(boxes.Box4) / 100.0,
		// Box5 (netVatDue) must always be the POSITIVE magnitude. A negative Box5
		// means HMRC owes us a repayment; HMRC signals this via paymentIndicator.
		NetVatDue: math.Round(math.Abs(float64(boxes.Box5))/100.0*100) / 100,
		// Boxes 6–9: round to whole pounds, then convert from pence to pounds (÷100).
		TotalValueSalesExVAT:         roundToWholePoundMinor(boxes.Box6) / 100,
		TotalValuePurchasesExVAT:     roundToWholePoundMinor(boxes.Box7) / 100,
		TotalValueGoodsSuppliedExVAT: roundToWholePoundMinor(boxes.Box8) / 100,
		TotalAcquisitionsExVAT:       roundToWholePoundMinor(boxes.Box9) / 100,
		Finalised:                    true,
	}

	payload, err := json.Marshal(body)
	if err != nil {
		return nil, kernel.ErrInternal(fmt.Errorf("marshal HMRC return: %w", err))
	}

	u := fmt.Sprintf("%s/%s/returns", apiBaseURL, vrn)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, u, bytes.NewReader(payload))
	if err != nil {
		return nil, kernel.ErrInternal(fmt.Errorf("build HMRC return request: %w", err))
	}
	req.Header.Set("Authorization", "Bearer "+accessToken)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/vnd.hmrc.1.0+json")

	resp, err := client.Do(req)
	if err != nil {
		return nil, kernel.ErrInternal(fmt.Errorf("HMRC return submission failed: %w", err))
	}
	defer resp.Body.Close()
	respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))

	if resp.StatusCode != http.StatusCreated && resp.StatusCode != http.StatusOK {
		return nil, hmrcHTTPError(resp.StatusCode, respBody)
	}

	var result hmrcReturnResponse
	if err := json.Unmarshal(respBody, &result); err != nil {
		return nil, kernel.ErrInternal(fmt.Errorf("decode HMRC return response: %w", err))
	}
	return &result, nil
}

// hmrcHTTPError maps HMRC HTTP error responses to AppErrors that the handler
// translates into the right HTTP status for our clients.
func hmrcHTTPError(status int, body []byte) error {
	var apiErr hmrcAPIError
	_ = json.Unmarshal(body, &apiErr) // best-effort; fall through to raw body if it fails

	msg := apiErr.Message
	if msg == "" {
		msg = string(body)
	}

	switch {
	case apiErr.Code == "DUPLICATE_SUBMISSION":
		return kernel.ErrConflict("HMRC rejected the return: already filed for this period")
	case apiErr.Code == "CLIENT_OR_AGENT_NOT_AUTHORISED":
		return kernel.ErrConflict("HMRC rejected the return: this organisation is not authorised for MTD VAT")
	case status == http.StatusForbidden:
		return kernel.ErrConflict("HMRC rejected the return: " + msg)
	case status == http.StatusUnprocessableEntity || status == http.StatusBadRequest:
		return kernel.ErrValidation("HMRC rejected the return: "+msg, nil)
	default:
		return kernel.ErrInternal(fmt.Errorf("HMRC returned %d: %s", status, msg))
	}
}
