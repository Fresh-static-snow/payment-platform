package domain

import (
	"fmt"
	"strings"
)

const (
	ReasonHighAmount       = "high_amount"
	ReasonCriticalAmount   = "critical_amount"
	ReasonUncommonCurrency = "uncommon_currency"
	ReasonNoRiskSignals    = "no_risk_signals"
)

type Evaluation struct {
	Score    int
	Decision Decision
	Reasons  []string
}

type PolicyConfig struct {
	RulesVersion        string
	DenyThreshold       int
	HighAmountMinor     int64
	CriticalAmountMinor int64
	CommonCurrencies    []string
}

// Policy deliberately contains only deterministic rules. Replaying the same
// payment under the same version therefore produces the same candidate result;
// the repository makes that result durable and race-safe.
type Policy struct {
	rulesVersion        string
	denyThreshold       int
	highAmountMinor     int64
	criticalAmountMinor int64
	commonCurrencies    map[string]struct{}
}

func DefaultPolicy() Policy {
	return Policy{
		rulesVersion:        "v1",
		denyThreshold:       70,
		highAmountMinor:     100_000,
		criticalAmountMinor: 1_000_000,
		commonCurrencies: map[string]struct{}{
			"USD": {},
			"EUR": {},
			"GBP": {},
			"UAH": {},
			"PLN": {},
		},
	}
}

func NewPolicy(cfg PolicyConfig) (Policy, error) {
	if strings.TrimSpace(cfg.RulesVersion) == "" {
		return Policy{}, fmt.Errorf("%w: rules version is required", ErrInvalidPolicy)
	}
	if cfg.DenyThreshold < 1 || cfg.DenyThreshold > 100 {
		return Policy{}, fmt.Errorf("%w: deny threshold must be between 1 and 100", ErrInvalidPolicy)
	}
	if cfg.HighAmountMinor <= 0 {
		return Policy{}, fmt.Errorf("%w: high amount threshold must be positive", ErrInvalidPolicy)
	}
	if cfg.CriticalAmountMinor <= cfg.HighAmountMinor {
		return Policy{}, fmt.Errorf("%w: critical amount threshold must exceed high amount threshold", ErrInvalidPolicy)
	}
	if len(cfg.CommonCurrencies) == 0 {
		return Policy{}, fmt.Errorf("%w: at least one common currency is required", ErrInvalidPolicy)
	}

	currencies := make(map[string]struct{}, len(cfg.CommonCurrencies))
	for _, currency := range cfg.CommonCurrencies {
		currency = strings.TrimSpace(currency)
		if len(currency) != 3 || strings.ToUpper(currency) != currency {
			return Policy{}, fmt.Errorf("%w: invalid common currency %q", ErrInvalidPolicy, currency)
		}
		currencies[currency] = struct{}{}
	}

	return Policy{
		rulesVersion:        cfg.RulesVersion,
		denyThreshold:       cfg.DenyThreshold,
		highAmountMinor:     cfg.HighAmountMinor,
		criticalAmountMinor: cfg.CriticalAmountMinor,
		commonCurrencies:    currencies,
	}, nil
}

func (p Policy) RulesVersion() string {
	return p.rulesVersion
}

func (p Policy) Evaluate(payment Payment) (Evaluation, error) {
	if err := payment.Validate(); err != nil {
		return Evaluation{}, err
	}

	score := 0
	reasons := make([]string, 0, 2)
	switch {
	case payment.Amount >= p.criticalAmountMinor:
		score += 80
		reasons = append(reasons, ReasonCriticalAmount)
	case payment.Amount >= p.highAmountMinor:
		score += 35
		reasons = append(reasons, ReasonHighAmount)
	}
	if _, common := p.commonCurrencies[payment.Currency]; !common {
		score += 40
		reasons = append(reasons, ReasonUncommonCurrency)
	}
	if score > 100 {
		score = 100
	}
	if len(reasons) == 0 {
		reasons = append(reasons, ReasonNoRiskSignals)
	}

	decision := DecisionAllow
	if score >= p.denyThreshold {
		decision = DecisionDeny
	}
	return Evaluation{Score: score, Decision: decision, Reasons: reasons}, nil
}
