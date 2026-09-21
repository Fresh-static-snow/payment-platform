package riskgrpc

import (
	"context"
	"errors"
	"strings"

	"github.com/google/uuid"
	riskv1 "github.com/praedyth/payment-platform/gen/risk/v1"
	"github.com/praedyth/payment-platform/internal/risk/domain"
	riskservice "github.com/praedyth/payment-platform/internal/risk/service"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/timestamppb"
)

type Server struct {
	riskv1.UnimplementedRiskServiceServer
	service *riskservice.Service
}

func NewServer(service *riskservice.Service) *Server {
	return &Server{service: service}
}

func (s *Server) AssessPayment(ctx context.Context, request *riskv1.AssessPaymentRequest) (*riskv1.AssessPaymentResponse, error) {
	if s == nil || s.service == nil {
		return nil, status.Error(codes.Internal, "risk service is not configured")
	}
	if request == nil {
		return nil, status.Error(codes.InvalidArgument, "request is required")
	}
	paymentID, err := uuid.Parse(request.GetPaymentId())
	if err != nil {
		return nil, status.Error(codes.InvalidArgument, "payment_id must be a valid UUID")
	}
	userID, err := uuid.Parse(request.GetUserId())
	if err != nil {
		return nil, status.Error(codes.InvalidArgument, "user_id must be a valid UUID")
	}

	assessment, err := s.service.Assess(ctx, domain.Payment{
		ID:       paymentID,
		UserID:   userID,
		Amount:   request.GetAmount(),
		Currency: strings.TrimSpace(request.GetCurrency()),
	})
	if err != nil {
		return nil, mapServiceError(err)
	}
	createdAt := timestamppb.New(assessment.CreatedAt)
	if err := createdAt.CheckValid(); err != nil {
		return nil, status.Error(codes.Internal, "risk decision contains an invalid timestamp")
	}

	return &riskv1.AssessPaymentResponse{
		DecisionId:   assessment.ID.String(),
		PaymentId:    assessment.PaymentID.String(),
		RulesVersion: assessment.RulesVersion,
		Score:        int32(assessment.Score),
		Decision:     encodeDecision(assessment.Decision),
		Reasons:      append([]string(nil), assessment.Reasons...),
		CreatedAt:    createdAt,
	}, nil
}

func encodeDecision(decision domain.Decision) riskv1.Decision {
	switch decision {
	case domain.DecisionAllow:
		return riskv1.Decision_DECISION_ALLOW
	case domain.DecisionDeny:
		return riskv1.Decision_DECISION_DENY
	default:
		return riskv1.Decision_DECISION_UNSPECIFIED
	}
}

func mapServiceError(err error) error {
	switch {
	case errors.Is(err, domain.ErrInvalidPayment), errors.Is(err, domain.ErrInvalidDecision):
		return status.Error(codes.InvalidArgument, err.Error())
	case errors.Is(err, context.Canceled):
		return status.Error(codes.Canceled, context.Canceled.Error())
	case errors.Is(err, context.DeadlineExceeded):
		return status.Error(codes.DeadlineExceeded, context.DeadlineExceeded.Error())
	default:
		return status.Error(codes.Internal, "risk assessment failed")
	}
}
