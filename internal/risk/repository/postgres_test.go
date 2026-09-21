package repository

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/praedyth/payment-platform/internal/risk/domain"
)

func TestScanAssessment(t *testing.T) {
	t.Parallel()
	wantID := uuid.New()
	wantPaymentID := uuid.New()
	wantCreatedAt := time.Now().UTC().Truncate(time.Microsecond)
	row := scannerFunc(func(dest ...any) error {
		*(dest[0].(*uuid.UUID)) = wantID
		*(dest[1].(*uuid.UUID)) = wantPaymentID
		*(dest[2].(*string)) = "v1"
		*(dest[3].(*int)) = 75
		*(dest[4].(*domain.Decision)) = domain.DecisionDeny
		*(dest[5].(*[]byte)) = []byte(`["high_amount","uncommon_currency"]`)
		*(dest[6].(*time.Time)) = wantCreatedAt
		return nil
	})

	got, err := scanAssessment(row)
	if err != nil {
		t.Fatalf("scanAssessment() error = %v", err)
	}
	if got.ID != wantID || got.PaymentID != wantPaymentID || got.Decision != domain.DecisionDeny || got.Score != 75 {
		t.Fatalf("scanAssessment() = %#v", got)
	}
	if len(got.Reasons) != 2 || got.Reasons[0] != domain.ReasonHighAmount {
		t.Fatalf("scanAssessment() reasons = %v", got.Reasons)
	}
}

func TestScanAssessmentRejectsMalformedReasons(t *testing.T) {
	t.Parallel()
	row := scannerFunc(func(dest ...any) error {
		*(dest[0].(*uuid.UUID)) = uuid.New()
		*(dest[1].(*uuid.UUID)) = uuid.New()
		*(dest[2].(*string)) = "v1"
		*(dest[3].(*int)) = 0
		*(dest[4].(*domain.Decision)) = domain.DecisionAllow
		*(dest[5].(*[]byte)) = []byte(`not-json`)
		*(dest[6].(*time.Time)) = time.Now().UTC()
		return nil
	})

	if _, err := scanAssessment(row); err == nil {
		t.Fatal("expected malformed reasons to fail")
	}
}

func TestPostgresWithoutPoolFails(t *testing.T) {
	t.Parallel()
	_, err := (&Postgres{}).GetOrCreate(context.Background(), domain.Assessment{})
	if err == nil {
		t.Fatal("expected repository without pool to fail")
	}
}

func TestScanAssessmentPropagatesScanError(t *testing.T) {
	t.Parallel()
	want := errors.New("scan failed")
	_, err := scanAssessment(scannerFunc(func(...any) error { return want }))
	if !errors.Is(err, want) {
		t.Fatalf("expected scan error, got %v", err)
	}
}

type scannerFunc func(dest ...any) error

func (f scannerFunc) Scan(dest ...any) error {
	return f(dest...)
}
