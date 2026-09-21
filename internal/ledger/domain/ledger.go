package domain

import (
	"errors"
	"fmt"
	"math"
	"strings"
	"time"

	"github.com/google/uuid"
)

var (
	ErrInvalidJournal    = errors.New("invalid ledger journal")
	ErrUnbalancedJournal = errors.New("unbalanced ledger journal")
)

type Direction string

const (
	Debit  Direction = "debit"
	Credit Direction = "credit"

	ProviderClearingAccount DirectionAccount = "asset:provider_clearing"
	MerchantPayableAccount  DirectionAccount = "liability:merchant_payable"
)

// DirectionAccount gives the built-in chart-of-accounts names a distinct type
// without preventing the ledger from accepting additional accounts later.
type DirectionAccount string

type Entry struct {
	ID        uuid.UUID
	JournalID uuid.UUID
	Account   string
	Direction Direction
	Amount    int64
	Currency  string
	CreatedAt time.Time
}

type Journal struct {
	ID            uuid.UUID
	EventID       uuid.UUID
	PaymentID     uuid.UUID
	ReferenceType string
	ReferenceID   uuid.UUID
	Currency      string
	Amount        int64
	CreatedAt     time.Time
	Entries       []Entry
}

// NewPaymentJournal records the provider clearing asset and the matching
// merchant liability. Amounts remain positive; Direction carries the sign.
func NewPaymentJournal(eventID, paymentID uuid.UUID, currency string, amount int64, occurredAt time.Time) (Journal, error) {
	journalID := uuid.New()
	currency = strings.ToUpper(strings.TrimSpace(currency))
	createdAt := occurredAt.UTC()

	journal := Journal{
		ID:            journalID,
		EventID:       eventID,
		PaymentID:     paymentID,
		ReferenceType: "payment",
		ReferenceID:   paymentID,
		Currency:      currency,
		Amount:        amount,
		CreatedAt:     createdAt,
		Entries: []Entry{
			{
				ID:        uuid.New(),
				JournalID: journalID,
				Account:   string(ProviderClearingAccount),
				Direction: Debit,
				Amount:    amount,
				Currency:  currency,
				CreatedAt: createdAt,
			},
			{
				ID:        uuid.New(),
				JournalID: journalID,
				Account:   string(MerchantPayableAccount),
				Direction: Credit,
				Amount:    amount,
				Currency:  currency,
				CreatedAt: createdAt,
			},
		},
	}

	if err := journal.Validate(); err != nil {
		return Journal{}, err
	}
	return journal, nil
}

// NewRefundJournal reverses the original payment direction: the merchant
// payable liability is debited and the provider clearing asset is credited.
func NewRefundJournal(eventID, refundID, paymentID uuid.UUID, currency string, amount int64, occurredAt time.Time) (Journal, error) {
	journalID := uuid.New()
	currency = strings.ToUpper(strings.TrimSpace(currency))
	createdAt := occurredAt.UTC()
	journal := Journal{
		ID:            journalID,
		EventID:       eventID,
		PaymentID:     paymentID,
		ReferenceType: "refund",
		ReferenceID:   refundID,
		Currency:      currency,
		Amount:        amount,
		CreatedAt:     createdAt,
		Entries: []Entry{
			{
				ID: uuid.New(), JournalID: journalID,
				Account: string(MerchantPayableAccount), Direction: Debit,
				Amount: amount, Currency: currency, CreatedAt: createdAt,
			},
			{
				ID: uuid.New(), JournalID: journalID,
				Account: string(ProviderClearingAccount), Direction: Credit,
				Amount: amount, Currency: currency, CreatedAt: createdAt,
			},
		},
	}
	if err := journal.Validate(); err != nil {
		return Journal{}, err
	}
	return journal, nil
}

// Validate enforces double-entry invariants before any database transaction is
// opened. Each side must equal the journal amount, so a balanced but unrelated
// set of entries cannot be stored under the journal.
func (j Journal) Validate() error {
	if j.ID == uuid.Nil {
		return fmt.Errorf("%w: journal id is required", ErrInvalidJournal)
	}
	if j.EventID == uuid.Nil {
		return fmt.Errorf("%w: event id is required", ErrInvalidJournal)
	}
	if j.PaymentID == uuid.Nil {
		return fmt.Errorf("%w: payment id is required", ErrInvalidJournal)
	}
	if j.ReferenceType != "payment" && j.ReferenceType != "refund" {
		return fmt.Errorf("%w: reference type must be payment or refund", ErrInvalidJournal)
	}
	if j.ReferenceID == uuid.Nil {
		return fmt.Errorf("%w: reference id is required", ErrInvalidJournal)
	}
	if j.Amount <= 0 {
		return fmt.Errorf("%w: amount must be positive", ErrInvalidJournal)
	}
	if !validCurrency(j.Currency) {
		return fmt.Errorf("%w: currency must be three uppercase ASCII letters", ErrInvalidJournal)
	}
	if j.CreatedAt.IsZero() {
		return fmt.Errorf("%w: created_at is required", ErrInvalidJournal)
	}
	if len(j.Entries) < 2 {
		return fmt.Errorf("%w: at least two entries are required", ErrInvalidJournal)
	}

	entryIDs := make(map[uuid.UUID]struct{}, len(j.Entries))
	var debitTotal, creditTotal int64
	for index, entry := range j.Entries {
		if entry.ID == uuid.Nil {
			return fmt.Errorf("%w: entry %d id is required", ErrInvalidJournal, index)
		}
		if _, exists := entryIDs[entry.ID]; exists {
			return fmt.Errorf("%w: duplicate entry id %s", ErrInvalidJournal, entry.ID)
		}
		entryIDs[entry.ID] = struct{}{}

		if entry.JournalID != j.ID {
			return fmt.Errorf("%w: entry %d belongs to another journal", ErrInvalidJournal, index)
		}
		if strings.TrimSpace(entry.Account) == "" {
			return fmt.Errorf("%w: entry %d account is required", ErrInvalidJournal, index)
		}
		if entry.Amount <= 0 {
			return fmt.Errorf("%w: entry %d amount must be positive", ErrInvalidJournal, index)
		}
		if entry.Currency != j.Currency {
			return fmt.Errorf("%w: entry %d currency differs from journal", ErrInvalidJournal, index)
		}
		if entry.CreatedAt.IsZero() {
			return fmt.Errorf("%w: entry %d created_at is required", ErrInvalidJournal, index)
		}

		switch entry.Direction {
		case Debit:
			if debitTotal > math.MaxInt64-entry.Amount {
				return fmt.Errorf("%w: debit total overflow", ErrInvalidJournal)
			}
			debitTotal += entry.Amount
		case Credit:
			if creditTotal > math.MaxInt64-entry.Amount {
				return fmt.Errorf("%w: credit total overflow", ErrInvalidJournal)
			}
			creditTotal += entry.Amount
		default:
			return fmt.Errorf("%w: entry %d has unknown direction %q", ErrInvalidJournal, index, entry.Direction)
		}
	}

	if debitTotal != creditTotal || debitTotal != j.Amount {
		return fmt.Errorf(
			"%w: debit=%d credit=%d journal_amount=%d",
			ErrUnbalancedJournal,
			debitTotal,
			creditTotal,
			j.Amount,
		)
	}
	return nil
}

func validCurrency(currency string) bool {
	if len(currency) != 3 {
		return false
	}
	for _, char := range currency {
		if char < 'A' || char > 'Z' {
			return false
		}
	}
	return true
}
