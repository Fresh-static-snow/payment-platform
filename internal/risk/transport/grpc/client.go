package riskgrpc

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"
	riskv1 "github.com/praedyth/payment-platform/gen/risk/v1"
	"github.com/praedyth/payment-platform/internal/risk/domain"
	"go.opentelemetry.io/contrib/instrumentation/google.golang.org/grpc/otelgrpc"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	healthv1 "google.golang.org/grpc/health/grpc_health_v1"
)

const defaultRequestTimeout = 500 * time.Millisecond

type Client struct {
	client  riskv1.RiskServiceClient
	health  healthv1.HealthClient
	closer  interface{ Close() error }
	timeout time.Duration
}

func NewClient(client riskv1.RiskServiceClient, timeout time.Duration) *Client {
	if timeout <= 0 {
		timeout = defaultRequestTimeout
	}
	return &Client{client: client, timeout: timeout}
}

func Dial(address string, timeout time.Duration) (*Client, error) {
	connection, err := grpc.NewClient(address,
		grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithStatsHandler(otelgrpc.NewClientHandler()),
	)
	if err != nil {
		return nil, fmt.Errorf("create risk gRPC connection: %w", err)
	}
	client := NewClient(riskv1.NewRiskServiceClient(connection), timeout)
	client.health = healthv1.NewHealthClient(connection)
	client.closer = connection
	return client, nil
}

func (c *Client) Ready(ctx context.Context) error {
	if c == nil || c.health == nil {
		return fmt.Errorf("risk gRPC health client is not configured")
	}
	response, err := c.health.Check(ctx, &healthv1.HealthCheckRequest{Service: riskv1.RiskService_ServiceDesc.ServiceName})
	if err != nil {
		return fmt.Errorf("risk health RPC: %w", err)
	}
	if response.GetStatus() != healthv1.HealthCheckResponse_SERVING {
		return fmt.Errorf("risk service is %s", response.GetStatus())
	}
	return nil
}

// Assess always applies a bounded RPC deadline. context.WithTimeout preserves a
// caller's earlier deadline, so an HTTP/Kafka processing budget is never extended.
func (c *Client) Assess(ctx context.Context, payment domain.Payment) (domain.Assessment, error) {
	if c == nil || c.client == nil {
		return domain.Assessment{}, fmt.Errorf("risk gRPC client is not configured")
	}
	requestCtx, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()
	requestCtx = withOutgoingRequestID(requestCtx)

	response, err := c.client.AssessPayment(requestCtx, &riskv1.AssessPaymentRequest{
		PaymentId: payment.ID.String(),
		UserId:    payment.UserID.String(),
		Amount:    payment.Amount,
		Currency:  payment.Currency,
	})
	if err != nil {
		return domain.Assessment{}, fmt.Errorf("risk AssessPayment RPC: %w", err)
	}
	if response == nil {
		return domain.Assessment{}, fmt.Errorf("risk AssessPayment RPC returned an empty response")
	}
	decisionID, err := uuid.Parse(response.GetDecisionId())
	if err != nil {
		return domain.Assessment{}, fmt.Errorf("risk response has invalid decision_id: %w", err)
	}
	paymentID, err := uuid.Parse(response.GetPaymentId())
	if err != nil {
		return domain.Assessment{}, fmt.Errorf("risk response has invalid payment_id: %w", err)
	}
	createdAt := response.GetCreatedAt()
	if createdAt == nil || createdAt.CheckValid() != nil {
		return domain.Assessment{}, fmt.Errorf("risk response has invalid created_at")
	}

	assessment := domain.Assessment{
		ID:           decisionID,
		PaymentID:    paymentID,
		RulesVersion: response.GetRulesVersion(),
		Score:        int(response.GetScore()),
		Decision:     decodeDecision(response.GetDecision()),
		Reasons:      append([]string(nil), response.GetReasons()...),
		CreatedAt:    createdAt.AsTime(),
	}
	if err := assessment.Validate(); err != nil {
		return domain.Assessment{}, fmt.Errorf("validate risk response: %w", err)
	}
	return assessment, nil
}

func (c *Client) Close() error {
	if c == nil || c.closer == nil {
		return nil
	}
	return c.closer.Close()
}

func decodeDecision(decision riskv1.Decision) domain.Decision {
	switch decision {
	case riskv1.Decision_DECISION_ALLOW:
		return domain.DecisionAllow
	case riskv1.Decision_DECISION_DENY:
		return domain.DecisionDeny
	default:
		return ""
	}
}
