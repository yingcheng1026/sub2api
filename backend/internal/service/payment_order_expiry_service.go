package service

import (
	"context"
	"log/slog"
	"sync"
	"time"
)

const expiryCheckTimeout = 30 * time.Second

// PaymentOrderExpiryService periodically expires timed-out payment orders.
type PaymentOrderExpiryService struct {
	paymentSvc *PaymentService
	interval   time.Duration
	stopCh     chan struct{}
	stopOnce   sync.Once
	wg         sync.WaitGroup
}

func NewPaymentOrderExpiryService(paymentSvc *PaymentService, interval time.Duration) *PaymentOrderExpiryService {
	return &PaymentOrderExpiryService{
		paymentSvc: paymentSvc,
		interval:   interval,
		stopCh:     make(chan struct{}),
	}
}

func (s *PaymentOrderExpiryService) Start() {
	if s == nil || s.paymentSvc == nil || s.interval <= 0 {
		return
	}
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		ticker := time.NewTicker(s.interval)
		defer ticker.Stop()

		s.runOnce()
		for {
			select {
			case <-ticker.C:
				s.runOnce()
			case <-s.stopCh:
				return
			}
		}
	}()
}

func (s *PaymentOrderExpiryService) Stop() {
	if s == nil {
		return
	}
	s.stopOnce.Do(func() {
		close(s.stopCh)
	})
	s.wg.Wait()
}

func (s *PaymentOrderExpiryService) runOnce() {
	expiryCtx, cancelExpiry := context.WithTimeout(context.Background(), expiryCheckTimeout)
	expired, err := s.paymentSvc.ExpireTimedOutOrders(expiryCtx)
	cancelExpiry()
	if err != nil {
		slog.Error("[PaymentOrderExpiry] failed to expire orders", "error", err)
	} else if expired > 0 {
		slog.Info("[PaymentOrderExpiry] expired timed-out orders", "count", expired)
	}

	recoveryCtx, cancelRecovery := context.WithTimeout(context.Background(), expiryCheckTimeout)
	recovered, err := s.paymentSvc.RecoverStaleFulfillments(recoveryCtx, defaultStaleRecoveryBatchSize)
	cancelRecovery()
	if err != nil {
		slog.Error("[PaymentOrderExpiry] failed to recover stale fulfillments", "error", err)
	} else if recovered > 0 {
		slog.Info("[PaymentOrderExpiry] recovered stale fulfillments", "count", recovered)
	}
}
