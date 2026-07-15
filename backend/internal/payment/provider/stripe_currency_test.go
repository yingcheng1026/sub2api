package provider

import (
	"encoding/json"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/payment"
	stripe "github.com/stripe/stripe-go/v85"
)

func TestParseStripePaymentIntentRejectsNonCNYCurrency(t *testing.T) {
	t.Parallel()

	for _, currency := range []string{"usd", ""} {
		t.Run(currency, func(t *testing.T) {
			t.Parallel()
			event := stripePaymentIntentTestEvent(t, currency)
			if _, err := parseStripePaymentIntent(event, payment.ProviderStatusSuccess, "signed-body"); err == nil {
				t.Fatalf("parseStripePaymentIntent accepted currency %q", currency)
			}
		})
	}
}

func TestParseStripePaymentIntentReturnsSignedCurrencyEvidence(t *testing.T) {
	t.Parallel()

	notification, err := parseStripePaymentIntent(
		stripePaymentIntentTestEvent(t, stripeCurrency),
		payment.ProviderStatusSuccess,
		"signed-body",
	)
	if err != nil {
		t.Fatalf("parseStripePaymentIntent: %v", err)
	}
	if got := notification.Metadata["currency"]; got != "CNY" {
		t.Fatalf("metadata currency = %q, want CNY", got)
	}
}

func stripePaymentIntentTestEvent(t *testing.T, currency string) *stripe.Event {
	t.Helper()
	raw, err := json.Marshal(map[string]any{
		"id":       "pi_currency_test",
		"amount":   1234,
		"currency": currency,
		"metadata": map[string]string{"orderId": "order-currency-test"},
	})
	if err != nil {
		t.Fatalf("marshal payment intent: %v", err)
	}
	return &stripe.Event{Data: &stripe.EventData{Raw: raw}}
}
