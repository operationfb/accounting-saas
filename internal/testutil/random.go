package util

import (
	"fmt"
	"math/rand"
	"strings"
	"time"
)

const alphabet = "abcdefghijklmnopqrstuvwxyz"

func init() {
	//rand.Seed(time.Now().UnixNano())
}

func RandomInt(min, max int64) int64 {
	return min + rand.Int63n(max-min+1)
}

// RandomString returns a random lowercase string of length n.
func RandomString(n int) string {
	var sb strings.Builder
	k := len(alphabet)
	for i := 0; i < n; i++ {
		c := alphabet[rand.Intn(k)]
		sb.WriteByte(c)
	}
	return sb.String()
}

// RandomEmail returns a random valid-looking email address.
func RandomEmail() string {
	return fmt.Sprintf("%s@email.com", RandomString(7))
}

// RandomCurrency returns a random ISO 4217 currency code from a small list.
func RandomCurrency() string {
	currencies := []string{"GBP", "EUR", "USD"}
	return currencies[rand.Intn(len(currencies))]
}

// =============================================================================
// EXPENSE-SPECIFIC GENERATORS
// =============================================================================

// RandomExpenseDescription returns a plausible random expense description.
// Using domain-relevant strings (not just "abcdef") makes test output easier
// to read when a test fails and you're inspecting the request/response body.
func RandomExpenseDescription() string {
	prefixes := []string{
		"Train ticket to", "Hotel stay in", "Client lunch at",
		"Software subscription", "Office supplies from", "Taxi to",
	}
	places := []string{
		"London", "Manchester", "Birmingham", "Edinburgh", "Bristol",
	}
	prefix := prefixes[rand.Intn(len(prefixes))]
	place := places[rand.Intn(len(places))]
	return fmt.Sprintf("%s %s", prefix, place)
}

// RandomGrossValue returns a random positive pound amount as a string,
// e.g. "42.50", "7.00", "199.99".
// We return a string because that is what CreateExpenseRequest accepts —
// the API takes a decimal string to avoid JavaScript float precision issues.
func RandomGrossValue() string {
	// Generate pence between 100 (£1.00) and 50000 (£500.00)
	pence := RandomInt(100, 50000)
	pounds := pence / 100
	remainingPence := pence % 100
	return fmt.Sprintf("%d.%02d", pounds, remainingPence)
}

// RandomDatedOn returns a random date string in YYYY-MM-DD format within the
// past 90 days. Expenses are rarely back-dated more than a quarter.
func RandomDatedOn() string {
	daysAgo := rand.Intn(90) // 0 to 89 days ago
	date := time.Now().AddDate(0, 0, -daysAgo)
	return date.Format("2006-01-02")
}

// RandomReceiptReference returns a random receipt/invoice reference string,
// e.g. "INV-00042" — the kind of reference you'd find on a supplier receipt.
func RandomReceiptReference() string {
	return fmt.Sprintf("INV-%05d", RandomInt(1, 99999))
}

// RandomSupplierName returns a plausible random supplier name.
func RandomSupplierName() string {
	adjectives := []string{"Global", "Northern", "Premier", "Direct", "City"}
	nouns := []string{"Supplies", "Services", "Solutions", "Group", "Partners"}
	adj := adjectives[rand.Intn(len(adjectives))]
	noun := nouns[rand.Intn(len(nouns))]
	return fmt.Sprintf("%s %s Ltd", adj, noun)
}

// =============================================================================
// CONTACT-SPECIFIC GENERATORS
// =============================================================================

// RandomContactOrgName returns a plausible, UNIQUE-ish company name for a test
// contact. The embedded random token keeps it distinct so list/lookup assertions
// can find exactly the row a test created.
func RandomContactOrgName() string {
	names := []string{"Acme", "Globex", "Initech", "Umbrella", "Soylent", "Hooli"}
	return fmt.Sprintf("%s %s Ltd", names[rand.Intn(len(names))], RandomString(6))
}
