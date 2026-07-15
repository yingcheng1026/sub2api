package payment

import (
	"fmt"
	"math"
	"regexp"

	"github.com/shopspring/decimal"
)

const centsPerYuan = 100

var yuanAmountPattern = regexp.MustCompile(`^[0-9]+(?:\.[0-9]{1,2})?$`)

// YuanToFen converts a CNY yuan string (e.g. "10.50") to fen (int64).
// Uses shopspring/decimal for precision.
func YuanToFen(yuanStr string) (int64, error) {
	if !yuanAmountPattern.MatchString(yuanStr) {
		return 0, fmt.Errorf("invalid amount format")
	}
	d, err := decimal.NewFromString(yuanStr)
	if err != nil {
		return 0, fmt.Errorf("invalid amount")
	}
	if d.Cmp(decimal.Zero) <= 0 {
		return 0, fmt.Errorf("amount must be positive")
	}
	scaled := d.Mul(decimal.NewFromInt(centsPerYuan))
	if scaled.GreaterThan(decimal.NewFromInt(math.MaxInt64)) {
		return 0, fmt.Errorf("amount exceeds supported range")
	}
	return scaled.IntPart(), nil
}

// FenToYuan converts fen (int64) to yuan as a float64 for interface compatibility.
func FenToYuan(fen int64) float64 {
	return decimal.NewFromInt(fen).Div(decimal.NewFromInt(centsPerYuan)).InexactFloat64()
}
