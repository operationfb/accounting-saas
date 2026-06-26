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
// HMRC fraud-prevention headers (Gov-Client-* / Gov-Vendor-*) are applied to every
// request here via applyFraudHeaders(req, ctx) — assembled per request in fraud.go
// and validated against HMRC's Test Fraud Prevention Headers API.
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
	Start     string `json:"start"`    // YYYY-MM-DD
	End       string `json:"end"`      // YYYY-MM-DD
	Due       string `json:"due"`      // YYYY-MM-DD
	Status    string `json:"status"`   // "O" = open, "F" = fulfilled
	Received  string `json:"received"` // YYYY-MM-DD; present only when Status = "F"
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

// listHMRCObligations calls HMRC's /obligations endpoint and returns every
// obligation whose START date falls in [from, to] (both YYYY-MM-DD). HMRC caps
// the window at 366 days — the caller must stay within that. We deliberately do
// NOT set status=O, so the result includes both open AND fulfilled obligations
// (the period reconciliation needs the full stagger, not just what's open).
func listHMRCObligations(ctx context.Context, client *http.Client, apiBaseURL, vrn, accessToken, from, to string) ([]hmrcObligation, error) {
	u, _ := url.Parse(fmt.Sprintf("%s/%s/obligations", apiBaseURL, vrn))
	q := u.Query()
	q.Set("from", from)
	q.Set("to", to)
	u.RawQuery = q.Encode()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return nil, kernel.ErrInternal(fmt.Errorf("build obligations request: %w", err))
	}
	req.Header.Set("Authorization", "Bearer "+accessToken)
	req.Header.Set("Accept", "application/vnd.hmrc.1.0+json")
	applyFraudHeaders(req, ctx)

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
	return obResp.Obligations, nil
}

// fetchHMRCObligation returns the obligation whose period-end date equals the
// given periodEnd (YYYY-MM-DD), used by the submit flow to recover HMRC's own
// periodKey. Returns a 409 AppError if no matching obligation is found.
func fetchHMRCObligation(ctx context.Context, client *http.Client, apiBaseURL, vrn, accessToken, periodEnd string) (*hmrcObligation, error) {
	// HMRC caps the obligations query window at 366 days. We centre a ~9-month
	// window on the target period end so we catch any stagger: 200 days before
	// the period end to 30 days after (total ~230 days — well within the limit).
	periodTime, err := time.Parse("2006-01-02", periodEnd)
	if err != nil {
		return nil, kernel.ErrInternal(fmt.Errorf("parse period end %q: %w", periodEnd, err))
	}
	from := periodTime.AddDate(0, 0, -200).Format("2006-01-02")
	to := periodTime.AddDate(0, 0, 30).Format("2006-01-02")

	obligations, err := listHMRCObligations(ctx, client, apiBaseURL, vrn, accessToken, from, to)
	if err != nil {
		return nil, err
	}

	// Find the obligation matching the period's end date.
	for i := range obligations {
		if obligations[i].End == periodEnd {
			return &obligations[i], nil
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
	applyFraudHeaders(req, ctx)

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
		return kernel.ErrConflict("HMRC rejected the request: this organisation is not authorised for MTD VAT")
	case status == http.StatusForbidden:
		return kernel.ErrConflict("HMRC rejected the request: " + msg)
	case status == http.StatusUnprocessableEntity || status == http.StatusBadRequest:
		return kernel.ErrValidation("HMRC rejected the request: "+msg, nil)
	default:
		return kernel.ErrInternal(fmt.Errorf("HMRC returned %d: %s", status, msg))
	}
}

// =============================================================================
// DASHBOARD READS (the HMRC VAT-account GET endpoints)
//
// These back the VAT dashboard. Same shape as fetchHMRCObligation above: a bearer
// token + the vendor Accept header, a 1 MB-capped read, non-2xx mapped via
// hmrcHTTPError, then json.Unmarshal. The service methods that call them — and the
// money/DTO mapping — live in account.go.
//
// Money note: HMRC sends amounts as JSON numbers (pounds). We decode them as
// json.Number (NOT float64); account.go formats them to fixed-dp strings with
// shopspring/decimal — keeping the repo's "never float for money" rule even for
// values we only display.
//
// "No data" is normal for a VAT account that is up to date, so on the COLLECTION
// reads (obligations / liabilities / payments / penalties / financial-details) an
// HMRC 404 is mapped to an EMPTY result rather than an error. The single-resource
// view-return maps 404 to ErrNotFound.
// =============================================================================

// hmrcGet issues an authenticated GET to {apiBaseURL}/{vrn}/{path} (with optional
// query) and returns the raw body + HTTP status. `path` may already contain an
// escaped sub-segment (e.g. "returns/18A1"); callers url.PathEscape any variable
// segment that can contain reserved characters.
func hmrcGet(ctx context.Context, client *http.Client, apiBaseURL, vrn, accessToken, path string, query url.Values) ([]byte, int, error) {
	full := fmt.Sprintf("%s/%s/%s", apiBaseURL, vrn, path)
	if len(query) > 0 {
		full += "?" + query.Encode()
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, full, nil)
	if err != nil {
		return nil, 0, kernel.ErrInternal(fmt.Errorf("build HMRC %s request: %w", path, err))
	}
	req.Header.Set("Authorization", "Bearer "+accessToken)
	req.Header.Set("Accept", "application/vnd.hmrc.1.0+json")
	applyFraudHeaders(req, ctx)

	resp, err := client.Do(req)
	if err != nil {
		return nil, 0, kernel.ErrInternal(fmt.Errorf("HMRC %s call failed: %w", path, err))
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	return body, resp.StatusCode, nil
}

// NOTE: obligations are listed by the shared listHMRCObligations above (added by
// the period-reconciliation feature) — the dashboard reuses it and filters by
// status in Go (see account.go GetHMRCObligations).

// ---- view a submitted return ----

type hmrcViewReturn struct {
	PeriodKey                    string      `json:"periodKey"`
	VatDueSales                  json.Number `json:"vatDueSales"`                  // box1
	VatDueAcquisitions           json.Number `json:"vatDueAcquisitions"`           // box2
	TotalVatDue                  json.Number `json:"totalVatDue"`                  // box3
	VatReclaimedCurrPeriod       json.Number `json:"vatReclaimedCurrPeriod"`       // box4
	NetVatDue                    json.Number `json:"netVatDue"`                    // box5
	TotalValueSalesExVAT         json.Number `json:"totalValueSalesExVAT"`         // box6
	TotalValuePurchasesExVAT     json.Number `json:"totalValuePurchasesExVAT"`     // box7
	TotalValueGoodsSuppliedExVAT json.Number `json:"totalValueGoodsSuppliedExVAT"` // box8
	TotalAcquisitionsExVAT       json.Number `json:"totalAcquisitionsExVAT"`       // box9
}

// fetchHMRCReturn gets a previously submitted return by HMRC periodKey. The
// periodKey can contain reserved characters (e.g. '#'), so it is path-escaped. A
// 404 means HMRC has no return for that key → ErrNotFound.
func fetchHMRCReturn(ctx context.Context, client *http.Client, apiBaseURL, vrn, accessToken, periodKey string) (*hmrcViewReturn, error) {
	body, code, err := hmrcGet(ctx, client, apiBaseURL, vrn, accessToken, "returns/"+url.PathEscape(periodKey), nil)
	if err != nil {
		return nil, err
	}
	if code == http.StatusNotFound {
		return nil, kernel.ErrNotFound("hmrc vat return", periodKey)
	}
	if code != http.StatusOK {
		return nil, hmrcHTTPError(code, body)
	}
	var r hmrcViewReturn
	if err := json.Unmarshal(body, &r); err != nil {
		return nil, kernel.ErrInternal(fmt.Errorf("decode HMRC return: %w", err))
	}
	return &r, nil
}

// ---- liabilities ----

type hmrcLiabilitiesResponse struct {
	Liabilities []hmrcLiability `json:"liabilities"`
}
type hmrcLiability struct {
	TaxPeriod         *hmrcTaxPeriod `json:"taxPeriod"`
	Type              string         `json:"type"`
	OriginalAmount    json.Number    `json:"originalAmount"`
	OutstandingAmount json.Number    `json:"outstandingAmount"`
	Due               string         `json:"due"`
}
type hmrcTaxPeriod struct {
	From string `json:"from"`
	To   string `json:"to"`
}

func fetchHMRCLiabilities(ctx context.Context, client *http.Client, apiBaseURL, vrn, accessToken, from, to string) ([]hmrcLiability, error) {
	q := url.Values{}
	q.Set("from", from)
	q.Set("to", to)
	body, code, err := hmrcGet(ctx, client, apiBaseURL, vrn, accessToken, "liabilities", q)
	if err != nil {
		return nil, err
	}
	if code == http.StatusNotFound {
		return []hmrcLiability{}, nil
	}
	if code != http.StatusOK {
		return nil, hmrcHTTPError(code, body)
	}
	var resp hmrcLiabilitiesResponse
	if err := json.Unmarshal(body, &resp); err != nil {
		return nil, kernel.ErrInternal(fmt.Errorf("decode HMRC liabilities: %w", err))
	}
	return resp.Liabilities, nil
}

// ---- payments ----

type hmrcPaymentsResponse struct {
	Payments []hmrcPayment `json:"payments"`
}
type hmrcPayment struct {
	Amount   json.Number `json:"amount"`
	Received string      `json:"received"`
}

func fetchHMRCPayments(ctx context.Context, client *http.Client, apiBaseURL, vrn, accessToken, from, to string) ([]hmrcPayment, error) {
	q := url.Values{}
	q.Set("from", from)
	q.Set("to", to)
	body, code, err := hmrcGet(ctx, client, apiBaseURL, vrn, accessToken, "payments", q)
	if err != nil {
		return nil, err
	}
	if code == http.StatusNotFound {
		return []hmrcPayment{}, nil
	}
	if code != http.StatusOK {
		return nil, hmrcHTTPError(code, body)
	}
	var resp hmrcPaymentsResponse
	if err := json.Unmarshal(body, &resp); err != nil {
		return nil, kernel.ErrInternal(fmt.Errorf("decode HMRC payments: %w", err))
	}
	return resp.Payments, nil
}

// ---- penalties ----
// A lenient subset of HMRC's Penalties API: the late-submission points summary, the
// running totals, and the individual late-submission / late-payment charges (each
// with the penaltyChargeReference that drills into financial-details).

type hmrcPenaltiesResponse struct {
	Totalisations         *hmrcPenaltyTotals `json:"totalisations"`
	LateSubmissionPenalty *hmrcLSP           `json:"lateSubmissionPenalty"`
	LatePaymentPenalty    *hmrcLPP           `json:"latePaymentPenalty"`
}
type hmrcPenaltyTotals struct {
	LSPTotalValue  json.Number `json:"LSPTotalValue"`
	LPPPostedTotal json.Number `json:"LPPPostedTotal"`
}
type hmrcLSP struct {
	Summary *hmrcLSPSummary `json:"summary"`
	Details []hmrcLSPDetail `json:"details"`
}
type hmrcLSPSummary struct {
	ActivePenaltyPoints   int         `json:"activePenaltyPoints"`
	InactivePenaltyPoints int         `json:"inactivePenaltyPoints"`
	RegimeThreshold       int         `json:"regimeThreshold"`
	PenaltyChargeAmount   json.Number `json:"penaltyChargeAmount"`
}
type hmrcLSPDetail struct {
	PenaltyChargeReference string      `json:"penaltyChargeReference"`
	PenaltyCategory        string      `json:"penaltyCategory"`
	PenaltyStatus          string      `json:"penaltyStatus"`
	ChargeAmount           json.Number `json:"chargeAmount"`
}
type hmrcLPP struct {
	Details []hmrcLPPDetail `json:"details"`
}
type hmrcLPPDetail struct {
	PenaltyChargeReference   string      `json:"penaltyChargeReference"`
	PenaltyCategory          string      `json:"penaltyCategory"`
	PenaltyStatus            string      `json:"penaltyStatus"`
	PenaltyAmountOutstanding json.Number `json:"penaltyAmountOutstanding"`
}

func fetchHMRCPenalties(ctx context.Context, client *http.Client, apiBaseURL, vrn, accessToken string) (*hmrcPenaltiesResponse, error) {
	body, code, err := hmrcGet(ctx, client, apiBaseURL, vrn, accessToken, "penalties", nil)
	if err != nil {
		return nil, err
	}
	if code == http.StatusNotFound {
		return &hmrcPenaltiesResponse{}, nil
	}
	if code != http.StatusOK {
		return nil, hmrcHTTPError(code, body)
	}
	var resp hmrcPenaltiesResponse
	if err := json.Unmarshal(body, &resp); err != nil {
		return nil, kernel.ErrInternal(fmt.Errorf("decode HMRC penalties: %w", err))
	}
	return &resp, nil
}

// ---- financial details (a penalty charge drill-down) ----

type hmrcFinancialDetails struct {
	DocumentDetails []hmrcFinancialDoc `json:"documentDetails"`
}
type hmrcFinancialDoc struct {
	DocumentType              string      `json:"documentType"`
	ChargeReferenceNumber     string      `json:"chargeReferenceNumber"`
	DocumentTotalAmount       json.Number `json:"documentTotalAmount"`
	DocumentOutstandingAmount json.Number `json:"documentOutstandingAmount"`
	DocumentDueDate           string      `json:"documentDueDate"`
}

func fetchHMRCFinancialDetails(ctx context.Context, client *http.Client, apiBaseURL, vrn, accessToken, chargeRef string) (*hmrcFinancialDetails, error) {
	body, code, err := hmrcGet(ctx, client, apiBaseURL, vrn, accessToken, "financial-details/"+url.PathEscape(chargeRef), nil)
	if err != nil {
		return nil, err
	}
	if code == http.StatusNotFound {
		return &hmrcFinancialDetails{}, nil
	}
	if code != http.StatusOK {
		return nil, hmrcHTTPError(code, body)
	}
	var resp hmrcFinancialDetails
	if err := json.Unmarshal(body, &resp); err != nil {
		return nil, kernel.ErrInternal(fmt.Errorf("decode HMRC financial details: %w", err))
	}
	return &resp, nil
}

// ---- information (registered VAT business details) ----
// Provisional shape: a lenient read of the commonly-present name + address +
// registration date. To be confirmed/tuned against the live sandbox response (we
// only ever READ fields we recognise, so extra/changed fields are ignored safely).

type hmrcInformation struct {
	OrganisationName string       `json:"organisationName"`
	TradingName      string       `json:"tradingName"`
	IndividualName   *hmrcName    `json:"individualName"`
	BusinessAddress  *hmrcAddress `json:"businessAddress"`
	RegistrationDate string       `json:"registrationDate"`
}
type hmrcName struct {
	FirstName string `json:"firstName"`
	LastName  string `json:"lastName"`
}
type hmrcAddress struct {
	Line1       string `json:"line1"`
	Line2       string `json:"line2"`
	Line3       string `json:"line3"`
	Line4       string `json:"line4"`
	Postcode    string `json:"postcode"`
	CountryCode string `json:"countryCode"`
}

func fetchHMRCInformation(ctx context.Context, client *http.Client, apiBaseURL, vrn, accessToken string) (*hmrcInformation, error) {
	body, code, err := hmrcGet(ctx, client, apiBaseURL, vrn, accessToken, "information", nil)
	if err != nil {
		return nil, err
	}
	if code == http.StatusNotFound {
		return &hmrcInformation{}, nil
	}
	if code != http.StatusOK {
		return nil, hmrcHTTPError(code, body)
	}
	var resp hmrcInformation
	if err := json.Unmarshal(body, &resp); err != nil {
		return nil, kernel.ErrInternal(fmt.Errorf("decode HMRC information: %w", err))
	}
	return &resp, nil
}
