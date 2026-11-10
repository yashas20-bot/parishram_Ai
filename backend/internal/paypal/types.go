package paypal

import (
	"errors"
	"fmt"
	"strings"
)

// Amount represents monetary value in PayPal's API.
type Amount struct {
	CurrencyCode string `json:"currency_code"`
	Value        string `json:"value"` // Formatted decimal representation, e.g. "2000.00"
}

// PurchaseUnitRequest specifies purchase details for order creation.
type PurchaseUnitRequest struct {
	ReferenceID string `json:"reference_id,omitempty"`
	Description string `json:"description,omitempty"`
	CustomID    string `json:"custom_id,omitempty"`
	Amount      Amount `json:"amount"`
}

// ApplicationContext customizes the PayPal checkout experience.
type ApplicationContext struct {
	BrandName   string `json:"brand_name,omitempty"`
	LandingPage string `json:"landing_page,omitempty"`
	UserAction  string `json:"user_action,omitempty"`
	ReturnURL   string `json:"return_url,omitempty"`
	CancelURL   string `json:"cancel_url,omitempty"`
}

// CreateOrderRequest is the payload sent to POST /v2/checkout/orders.
type CreateOrderRequest struct {
	Intent             string                `json:"intent"` // Strictly "CAPTURE"
	PurchaseUnits      []PurchaseUnitRequest `json:"purchase_units"`
	ApplicationContext *ApplicationContext   `json:"application_context,omitempty"`
}

// LinkDescription represents a HATEOAS navigation link returned by PayPal.
type LinkDescription struct {
	Href   string `json:"href"`
	Rel    string `json:"rel"`
	Method string `json:"method"`
}

// PurchaseUnit represents an individual purchase unit in a PayPal order response.
type PurchaseUnit struct {
	ReferenceID string `json:"reference_id,omitempty"`
	Amount      Amount `json:"amount,omitempty"`
}

// Order represents the PayPal Orders API v2 response.
type Order struct {
	ID            string            `json:"id"`
	Status        string            `json:"status"` // CREATED, SAVED, APPROVED, VOIDED, COMPLETED, PAYER_ACTION_REQUIRED
	Intent        string            `json:"intent"`
	PurchaseUnits []PurchaseUnit    `json:"purchase_units,omitempty"`
	Links         []LinkDescription `json:"links,omitempty"`
	CreateTime    string            `json:"create_time,omitempty"`
}

// ApprovalURL searches the HATEOAS links for the customer approval URL.
func (o *Order) ApprovalURL() string {
	for _, l := range o.Links {
		if l.Rel == "approve" || l.Rel == "payer-action" {
			return l.Href
		}
	}
	return ""
}

// FormatMinorToDecimal converts an integer minor unit amount (e.g. paise, cents)
// into an exact decimal string for PayPal using pure integer arithmetic.
func FormatMinorToDecimal(amount int64, currency string) (string, error) {
	if amount <= 0 {
		return "", errors.New("amount must be greater than zero")
	}

	curr := strings.ToUpper(strings.TrimSpace(currency))
	if len(curr) != 3 {
		return "", fmt.Errorf("invalid 3-letter currency code: %s", currency)
	}

	switch curr {
	case "JPY", "KRW", "VND", "CLP", "PYG", "ISK", "UGX", "RWF":
		// 0 decimal places
		return fmt.Sprintf("%d", amount), nil
	case "BHD", "KWD", "OMR", "JOD", "TND":
		// 3 decimal places
		major := amount / 1000
		minor := amount % 1000
		return fmt.Sprintf("%d.%03d", major, minor), nil
	default:
		// Standard 2 decimal places (INR, USD, EUR, GBP, CAD, AUD, etc.)
		major := amount / 100
		minor := amount % 100
		return fmt.Sprintf("%d.%02d", major, minor), nil
	}
}
