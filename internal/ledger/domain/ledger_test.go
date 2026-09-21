package domain

import (
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
)

func TestNewPaymentJournalCreatesBalancedEntries(t *testing.T) {
	eventID := uuid.New()
	paymentID := uuid.New()
	createdAt := time.Date(2026, time.September, 19, 12, 0, 0, 0, time.FixedZone("test", 2*60*60))

	journal, err := NewPaymentJournal(eventID, paymentID, " usd ", 1250, createdAt)
	if err != nil {
		t.Fatalf("NewPaymentJournal() error = %v", err)
	}
	if journal.EventID != eventID || journal.PaymentID != paymentID {
		t.Fatalf("journal identifiers do not match source event")
	}
	if journal.Currency != "USD" {
		t.Fatalf("journal currency = %q, want USD", journal.Currency)
	}
	if !journal.CreatedAt.Equal(createdAt.UTC()) {
		t.Fatalf("journal created_at = %v, want %v", journal.CreatedAt, createdAt.UTC())
	}
	if len(journal.Entries) != 2 {
		t.Fatalf("entry count = %d, want 2", len(journal.Entries))
	}
	if journal.Entries[0].Direction != Debit || journal.Entries[1].Direction != Credit {
		t.Fatalf("entry directions = %q/%q, want debit/credit", journal.Entries[0].Direction, journal.Entries[1].Direction)
	}
	if err := journal.Validate(); err != nil {
		t.Fatalf("Validate() error = %v", err)
	}
}

func TestNewRefundJournalReversesPaymentDirections(t *testing.T) {
	journal, err := NewRefundJournal(uuid.New(), uuid.New(), uuid.New(), "USD", 250, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if journal.ReferenceType != "refund" {
		t.Fatalf("reference type=%q", journal.ReferenceType)
	}
	if journal.Entries[0].Account != string(MerchantPayableAccount) || journal.Entries[0].Direction != Debit {
		t.Fatalf("first refund entry=%+v", journal.Entries[0])
	}
	if journal.Entries[1].Account != string(ProviderClearingAccount) || journal.Entries[1].Direction != Credit {
		t.Fatalf("second refund entry=%+v", journal.Entries[1])
	}
	if err := journal.Validate(); err != nil {
		t.Fatal(err)
	}
}

func TestJournalValidateRejectsBrokenDoubleEntryInvariants(t *testing.T) {
	tests := []struct {
		name       string
		mutate     func(*Journal)
		wantTarget error
	}{
		{
			name: "unbalanced credit",
			mutate: func(journal *Journal) {
				journal.Entries[1].Amount--
			},
			wantTarget: ErrUnbalancedJournal,
		},
		{
			name: "mixed currency",
			mutate: func(journal *Journal) {
				journal.Entries[1].Currency = "EUR"
			},
			wantTarget: ErrInvalidJournal,
		},
		{
			name: "unknown direction",
			mutate: func(journal *Journal) {
				journal.Entries[1].Direction = Direction("sideways")
			},
			wantTarget: ErrInvalidJournal,
		},
		{
			name: "non-positive entry",
			mutate: func(journal *Journal) {
				journal.Entries[0].Amount = 0
			},
			wantTarget: ErrInvalidJournal,
		},
		{
			name: "entry from another journal",
			mutate: func(journal *Journal) {
				journal.Entries[0].JournalID = uuid.New()
			},
			wantTarget: ErrInvalidJournal,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			journal, err := NewPaymentJournal(uuid.New(), uuid.New(), "USD", 1000, time.Now().UTC())
			if err != nil {
				t.Fatalf("NewPaymentJournal() error = %v", err)
			}
			test.mutate(&journal)
			err = journal.Validate()
			if !errors.Is(err, test.wantTarget) {
				t.Fatalf("Validate() error = %v, want errors.Is(%v)", err, test.wantTarget)
			}
		})
	}
}

func TestNewPaymentJournalRejectsInvalidSourceData(t *testing.T) {
	tests := []struct {
		name      string
		eventID   uuid.UUID
		paymentID uuid.UUID
		currency  string
		amount    int64
		createdAt time.Time
	}{
		{name: "missing event", paymentID: uuid.New(), currency: "USD", amount: 1, createdAt: time.Now()},
		{name: "missing payment", eventID: uuid.New(), currency: "USD", amount: 1, createdAt: time.Now()},
		{name: "invalid currency", eventID: uuid.New(), paymentID: uuid.New(), currency: "US1", amount: 1, createdAt: time.Now()},
		{name: "zero amount", eventID: uuid.New(), paymentID: uuid.New(), currency: "USD", createdAt: time.Now()},
		{name: "missing timestamp", eventID: uuid.New(), paymentID: uuid.New(), currency: "USD", amount: 1},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := NewPaymentJournal(test.eventID, test.paymentID, test.currency, test.amount, test.createdAt)
			if !errors.Is(err, ErrInvalidJournal) {
				t.Fatalf("NewPaymentJournal() error = %v, want ErrInvalidJournal", err)
			}
		})
	}
}
