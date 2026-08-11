package handler

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/repository"
	middleware2 "github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/alicebob/miniredis/v2"
	"github.com/gin-gonic/gin"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

func TestImageConcurrencyLimiter_DefaultDisabledAllowsRequests(t *testing.T) {
	limiter := &imageConcurrencyLimiter{}

	release, acquired := limiter.TryAcquire(false, 1)

	require.True(t, acquired)
	require.Nil(t, release)
}

func TestImageConcurrencyLimiter_RejectsWhenLimitReachedAndAllowsAfterRelease(t *testing.T) {
	limiter := &imageConcurrencyLimiter{}

	release, acquired := limiter.TryAcquire(true, 1)
	require.True(t, acquired)
	require.NotNil(t, release)

	secondRelease, secondAcquired := limiter.TryAcquire(true, 1)
	require.False(t, secondAcquired)
	require.Nil(t, secondRelease)

	release()
	thirdRelease, thirdAcquired := limiter.TryAcquire(true, 1)
	require.True(t, thirdAcquired)
	require.NotNil(t, thirdRelease)
	thirdRelease()
}

func TestImageConcurrencyLimiter_WaitsUntilSlotReleased(t *testing.T) {
	limiter := &imageConcurrencyLimiter{}
	release, acquired := limiter.Acquire(context.Background(), true, 1, true, time.Second, 1)
	require.True(t, acquired)
	require.NotNil(t, release)

	acquiredCh := make(chan func(), 1)
	go func() {
		waitRelease, waitAcquired := limiter.Acquire(context.Background(), true, 1, true, time.Second, 1)
		require.True(t, waitAcquired)
		acquiredCh <- waitRelease
	}()

	time.Sleep(20 * time.Millisecond)
	release()

	select {
	case waitRelease := <-acquiredCh:
		require.NotNil(t, waitRelease)
		waitRelease()
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for image concurrency slot")
	}
}

func TestImageConcurrencyLimiter_WaitTimesOut(t *testing.T) {
	limiter := &imageConcurrencyLimiter{}
	release, acquired := limiter.Acquire(context.Background(), true, 1, true, time.Second, 1)
	require.True(t, acquired)
	require.NotNil(t, release)
	defer release()

	waitRelease, waitAcquired := limiter.Acquire(context.Background(), true, 1, true, 10*time.Millisecond, 1)

	require.False(t, waitAcquired)
	require.Nil(t, waitRelease)
}

func TestImageConcurrencyLimiter_MaxWaitingRequestsRejectsOverflow(t *testing.T) {
	limiter := &imageConcurrencyLimiter{}
	release, acquired := limiter.Acquire(context.Background(), true, 1, true, time.Second, 1)
	require.True(t, acquired)
	require.NotNil(t, release)
	defer release()

	waitingStarted := make(chan struct{})
	waitingDone := make(chan struct{})
	go func() {
		close(waitingStarted)
		waitRelease, waitAcquired := limiter.Acquire(context.Background(), true, 1, true, time.Second, 1)
		if waitAcquired && waitRelease != nil {
			waitRelease()
		}
		close(waitingDone)
	}()
	<-waitingStarted
	time.Sleep(20 * time.Millisecond)

	overflowRelease, overflowAcquired := limiter.Acquire(context.Background(), true, 1, true, time.Second, 1)

	require.False(t, overflowAcquired)
	require.Nil(t, overflowRelease)
	release()
	<-waitingDone
}

func TestImageConcurrencyLimiter_AdmissionObservationCoversOutcomesAndSnapshots(t *testing.T) {
	limiter := &imageConcurrencyLimiter{}
	observations := make(chan imageConcurrencyObservation, 8)
	limiter.SetObserver(func(observation imageConcurrencyObservation) {
		observations <- observation
	})

	release, result := limiter.AcquireObserved(context.Background(), true, 1, false, 0, 0)
	require.True(t, result.Acquired)
	require.Equal(t, imageConcurrencyOutcomeAcquired, result.Observation.Outcome)
	require.Equal(t, 1, result.Observation.Snapshot.Active)
	require.Equal(t, 0, result.Observation.Snapshot.Waiting)
	require.NotNil(t, release)

	_, result = limiter.AcquireObserved(context.Background(), true, 1, false, 0, 0)
	require.False(t, result.Acquired)
	require.Equal(t, imageConcurrencyOutcomeRejected, result.Observation.Outcome)
	require.Equal(t, 1, result.Observation.Snapshot.Active)

	_, result = limiter.AcquireObserved(context.Background(), true, 1, true, time.Second, 0)
	require.False(t, result.Acquired)
	require.Equal(t, imageConcurrencyOutcomeTimeout, result.Observation.Outcome)

	release()

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, result = limiter.AcquireObserved(ctx, true, 1, true, time.Second, 1)
	require.False(t, result.Acquired)
	require.Equal(t, imageConcurrencyOutcomeCanceled, result.Observation.Outcome)

	seen := make(map[imageConcurrencyOutcome]bool)
	for len(seen) < 4 {
		select {
		case observation := <-observations:
			seen[observation.Outcome] = true
		case <-time.After(time.Second):
			t.Fatal("timed out waiting for image concurrency observations")
		}
	}
	require.True(t, seen[imageConcurrencyOutcomeAcquired])
	require.True(t, seen[imageConcurrencyOutcomeRejected])
	require.True(t, seen[imageConcurrencyOutcomeTimeout])
	require.True(t, seen[imageConcurrencyOutcomeCanceled])
}

func TestImageConcurrencyLimiter_QueueFullObservationIncludesWaitingSnapshot(t *testing.T) {
	limiter := &imageConcurrencyLimiter{}
	release, result := limiter.AcquireObserved(context.Background(), true, 1, false, 0, 0)
	require.True(t, result.Acquired)
	require.NotNil(t, release)

	waitingStarted := make(chan struct{})
	waitingDone := make(chan struct{})
	go func() {
		close(waitingStarted)
		waitRelease, waitResult := limiter.AcquireObserved(context.Background(), true, 1, true, time.Second, 1)
		if waitResult.Acquired && waitRelease != nil {
			waitRelease()
		}
		close(waitingDone)
	}()
	<-waitingStarted
	deadline := time.Now().Add(time.Second)
	for limiter.Snapshot().Waiting != 1 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	require.Equal(t, 1, limiter.Snapshot().Waiting)

	_, queueResult := limiter.AcquireObserved(context.Background(), true, 1, true, time.Second, 1)
	require.False(t, queueResult.Acquired)
	require.Equal(t, imageConcurrencyOutcomeQueueFull, queueResult.Observation.Outcome)
	require.Equal(t, 1, queueResult.Observation.Snapshot.Active)
	require.Equal(t, 1, queueResult.Observation.Snapshot.Waiting)

	release()
	select {
	case <-waitingDone:
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for queued image request")
	}
}

func TestOpenAIGatewayHandlerImageTurnAdmission_TextDoesNotHoldImageSlot(t *testing.T) {
	h := &OpenAIGatewayHandler{
		cfg: &config.Config{Gateway: config.GatewayConfig{ImageConcurrency: config.ImageConcurrencyConfig{
			Enabled:               true,
			MaxConcurrentRequests: 1,
			OverflowMode:          config.ImageConcurrencyOverflowModeReject,
		}}},
		imageLimiter: &imageConcurrencyLimiter{},
	}

	textRelease, textAcquired, textObservation := h.acquireImageTurnSlot(context.Background(), false)
	require.True(t, textAcquired)
	require.Nil(t, textRelease)
	require.Equal(t, imageConcurrencyOutcomeSkipped, textObservation.Outcome)
	require.Zero(t, h.imageLimiter.Snapshot().Active)

	imageRelease, imageAcquired, imageObservation := h.acquireImageTurnSlot(context.Background(), true)
	require.True(t, imageAcquired)
	require.Equal(t, imageConcurrencyOutcomeAcquired, imageObservation.Outcome)
	require.NotNil(t, imageRelease)
	require.Equal(t, 1, h.imageLimiter.Snapshot().Active)

	imageRelease()
	imageRelease()
	require.Zero(t, h.imageLimiter.Snapshot().Active)
}

func TestOpenAIGatewayHandlerImageTurnAdmission_SkippedAndCanceledSnapshotsKeepConfig(t *testing.T) {
	h := &OpenAIGatewayHandler{
		cfg: &config.Config{Gateway: config.GatewayConfig{ImageConcurrency: config.ImageConcurrencyConfig{
			Enabled:               true,
			MaxConcurrentRequests: 3,
			OverflowMode:          config.ImageConcurrencyOverflowModeWait,
			WaitTimeoutSeconds:    2,
			MaxWaitingRequests:    4,
		}}},
		imageLimiter: &imageConcurrencyLimiter{},
	}

	_, _, skipped := h.acquireImageTurnSlot(context.Background(), false)
	require.Equal(t, imageConcurrencyOutcomeSkipped, skipped.Outcome)
	require.True(t, skipped.Snapshot.Enabled)
	require.Equal(t, 3, skipped.Snapshot.Limit)

	canceledCtx, cancel := context.WithCancel(context.Background())
	cancel()
	_, acquired, canceled := h.acquireImageTurnSlot(canceledCtx, true)
	require.False(t, acquired)
	require.Equal(t, imageConcurrencyOutcomeCanceled, canceled.Outcome)
	require.True(t, canceled.Snapshot.Enabled)
	require.Equal(t, 3, canceled.Snapshot.Limit)

	_, disabled := h.imageLimiter.AcquireObserved(context.Background(), false, 3, false, 0, 0)
	require.True(t, disabled.Acquired)
	require.Equal(t, imageConcurrencyOutcomeAcquired, disabled.Observation.Outcome)
	require.False(t, disabled.Observation.Snapshot.Enabled)
	require.Equal(t, 3, disabled.Observation.Snapshot.Limit)
}

func TestImageConcurrencyLimiter_ReAdmitAfterFailoverReleaseCompetesForLimit(t *testing.T) {
	limiter := &imageConcurrencyLimiter{}
	firstRelease, first := limiter.AcquireObserved(context.Background(), true, 1, false, 0, 0)
	require.True(t, first.Acquired)
	require.NotNil(t, firstRelease)

	firstRelease()
	competitorRelease, competitor := limiter.AcquireObserved(context.Background(), true, 1, false, 0, 0)
	require.True(t, competitor.Acquired)
	require.NotNil(t, competitorRelease)

	_, retry := limiter.AcquireObserved(context.Background(), true, 1, false, 0, 0)
	require.False(t, retry.Acquired)
	require.Equal(t, imageConcurrencyOutcomeRejected, retry.Observation.Outcome)

	competitorRelease()
}

func TestOpenAIGatewayHandlerEnsureImageTurnSlotForAttempt_ReAdmitsOnlyAfterRelease(t *testing.T) {
	h := &OpenAIGatewayHandler{
		cfg: &config.Config{Gateway: config.GatewayConfig{ImageConcurrency: config.ImageConcurrencyConfig{
			Enabled:               true,
			MaxConcurrentRequests: 1,
			OverflowMode:          config.ImageConcurrencyOverflowModeReject,
		}}},
		imageLimiter: &imageConcurrencyLimiter{},
	}

	var currentRelease func()
	hasRelease := func() bool { return currentRelease != nil }
	setRelease := func(release func()) { currentRelease = release }

	// The first attempt acquires once; a repeated guard call must not
	// double-acquire the still-owned slot.
	firstAcquired, firstObservation := h.ensureImageTurnSlotForAttempt(context.Background(), true, hasRelease, setRelease)
	require.True(t, firstAcquired)
	require.Equal(t, imageConcurrencyOutcomeAcquired, firstObservation.Outcome)
	require.Equal(t, 1, h.imageLimiter.Snapshot().Active)

	secondAcquired, secondObservation := h.ensureImageTurnSlotForAttempt(context.Background(), true, hasRelease, setRelease)
	require.True(t, secondAcquired)
	require.Equal(t, imageConcurrencyOutcomeSkipped, secondObservation.Outcome)
	require.Equal(t, 1, h.imageLimiter.Snapshot().Active)

	// AfterTurn clears ownership before the next account attempt. A competing
	// image request must win the current slot and make re-admission retryable.
	currentRelease()
	currentRelease = nil
	competitorRelease, competitor := h.imageLimiter.AcquireObserved(context.Background(), true, 1, false, 0, 0)
	require.True(t, competitor.Acquired)

	retryAcquired, retryObservation := h.ensureImageTurnSlotForAttempt(context.Background(), true, hasRelease, setRelease)
	require.False(t, retryAcquired)
	require.Equal(t, imageConcurrencyOutcomeRejected, retryObservation.Outcome)
	competitorRelease()

	retryAcquired, retryObservation = h.ensureImageTurnSlotForAttempt(context.Background(), true, hasRelease, setRelease)
	require.True(t, retryAcquired)
	require.Equal(t, imageConcurrencyOutcomeAcquired, retryObservation.Outcome)
	require.NotNil(t, currentRelease)
	currentRelease()
	currentRelease = nil
}

func TestOpenAIGatewayHandlerAcquireImageGenerationSlot_Returns429WhenFull(t *testing.T) {
	gin.SetMode(gin.TestMode)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/images/generations", nil)

	h := &OpenAIGatewayHandler{
		cfg: &config.Config{
			Gateway: config.GatewayConfig{
				ImageConcurrency: config.ImageConcurrencyConfig{
					Enabled:               true,
					MaxConcurrentRequests: 1,
					OverflowMode:          config.ImageConcurrencyOverflowModeReject,
				},
			},
		},
		imageLimiter: &imageConcurrencyLimiter{},
	}
	release, acquired := h.acquireImageGenerationSlot(c, false)
	require.True(t, acquired)
	require.NotNil(t, release)
	defer release()

	blockedRelease, blocked := h.acquireImageGenerationSlot(c, false)

	require.False(t, blocked)
	require.Nil(t, blockedRelease)
	require.Equal(t, http.StatusTooManyRequests, rec.Code)
	require.Equal(t, "rate_limit_error", gjson.GetBytes(rec.Body.Bytes(), "error.type").String())
	require.Contains(t, rec.Body.String(), "Image generation concurrency limit exceeded")
}

func TestOpenAIGatewayHandlerResponses_ImageIntentRejectedByImageConcurrency(t *testing.T) {
	gin.SetMode(gin.TestMode)
	body := `{"model":"gpt-5.4","input":"draw","tools":[{"type":"image_generation"}]}`
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(body))
	groupID := int64(1)
	c.Set(string(middleware2.ContextKeyAPIKey), &service.APIKey{
		ID:      10,
		GroupID: &groupID,
		Group: &service.Group{
			ID:                   groupID,
			AllowImageGeneration: true,
		},
		User: &service.User{ID: 20},
	})
	c.Set(string(middleware2.ContextKeyUser), middleware2.AuthSubject{UserID: 20, Concurrency: 1})

	h := &OpenAIGatewayHandler{
		gatewayService:          &service.OpenAIGatewayService{},
		billingCacheService:     &service.BillingCacheService{},
		apiKeyService:           &service.APIKeyService{},
		concurrencyHelper:       &ConcurrencyHelper{concurrencyService: service.NewConcurrencyService(&helperConcurrencyCacheStub{userSeq: []bool{true}})},
		errorPassthroughService: nil,
		cfg: &config.Config{Gateway: config.GatewayConfig{ImageConcurrency: config.ImageConcurrencyConfig{
			Enabled:               true,
			MaxConcurrentRequests: 1,
			OverflowMode:          config.ImageConcurrencyOverflowModeReject,
		}}},
		imageLimiter: &imageConcurrencyLimiter{},
	}
	release, acquired := h.acquireImageGenerationSlot(c, false)
	require.True(t, acquired)
	require.NotNil(t, release)
	defer release()
	rec.Body.Reset()
	rec.Code = 0

	h.Responses(c)

	require.Equal(t, http.StatusTooManyRequests, rec.Code)
	require.Equal(t, "rate_limit_error", gjson.GetBytes(rec.Body.Bytes(), "error.type").String())
	require.Contains(t, rec.Body.String(), "Image generation concurrency limit exceeded")
}

func TestOpenAIGatewayHandlerResponses_TextOnlyNotRejectedByImageConcurrency(t *testing.T) {
	gin.SetMode(gin.TestMode)
	body := `{"model":"gpt-5.4","input":"write code"}`
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(body))
	groupID := int64(1)
	c.Set(string(middleware2.ContextKeyAPIKey), &service.APIKey{
		ID:      10,
		GroupID: &groupID,
		Group: &service.Group{
			ID:                   groupID,
			AllowImageGeneration: true,
		},
		User: &service.User{ID: 20},
	})
	c.Set(string(middleware2.ContextKeyUser), middleware2.AuthSubject{UserID: 20, Concurrency: 1})

	h := &OpenAIGatewayHandler{
		gatewayService:      &service.OpenAIGatewayService{},
		billingCacheService: service.NewBillingCacheService(nil, nil, nil, nil, nil, nil, &config.Config{RunMode: config.RunModeSimple}, nil),
		apiKeyService:       &service.APIKeyService{},
		concurrencyHelper:   &ConcurrencyHelper{concurrencyService: service.NewConcurrencyService(&helperConcurrencyCacheStub{userSeq: []bool{true}})},
		cfg: &config.Config{Gateway: config.GatewayConfig{ImageConcurrency: config.ImageConcurrencyConfig{
			Enabled:               true,
			MaxConcurrentRequests: 1,
			OverflowMode:          config.ImageConcurrencyOverflowModeReject,
		}}},
		imageLimiter: &imageConcurrencyLimiter{},
	}
	release, acquired := h.acquireImageGenerationSlot(c, false)
	require.True(t, acquired)
	require.NotNil(t, release)
	defer release()
	rec.Body.Reset()
	rec.Code = 0

	h.Responses(c)

	require.NotEqual(t, http.StatusTooManyRequests, rec.Code)
	require.NotContains(t, rec.Body.String(), "Image generation concurrency limit exceeded")
}

func TestOpenAIGatewayHandlerDistributedImageAdmissionSharesCapacityAcrossInstances(t *testing.T) {
	redisServer := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: redisServer.Addr()})
	t.Cleanup(func() { _ = client.Close() })
	cache := repository.NewConcurrencyCache(client, 1, 60)
	cfg := &config.Config{Gateway: config.GatewayConfig{ImageConcurrency: config.ImageConcurrencyConfig{
		Enabled:               true,
		MaxConcurrentRequests: 1,
		OverflowMode:          config.ImageConcurrencyOverflowModeReject,
		DistributedEnabled:    true,
		LeaseTTLSeconds:       30,
	}}}
	first := NewOpenAIGatewayHandler(nil, service.NewConcurrencyService(cache), nil, nil, nil, nil, nil, nil, cfg)
	second := NewOpenAIGatewayHandler(nil, service.NewConcurrencyService(cache), nil, nil, nil, nil, nil, nil, cfg)

	firstRelease, acquired, observation := first.acquireImageTurnSlot(context.Background(), true)
	require.True(t, acquired)
	require.Equal(t, imageConcurrencyOutcomeAcquired, observation.Outcome)
	require.NotNil(t, firstRelease)

	blockedRelease, acquired, observation := second.acquireImageTurnSlot(context.Background(), true)
	require.False(t, acquired)
	require.Equal(t, imageConcurrencyOutcomeRejected, observation.Outcome)
	require.Nil(t, blockedRelease)

	firstRelease()
	secondRelease, acquired, observation := second.acquireImageTurnSlot(context.Background(), true)
	require.True(t, acquired)
	require.Equal(t, imageConcurrencyOutcomeAcquired, observation.Outcome)
	require.NotNil(t, secondRelease)
	secondRelease()
}

func TestOpenAIGatewayHandlerDistributedImageAdmissionFailsClosedButTextBypasses(t *testing.T) {
	cfg := &config.Config{Gateway: config.GatewayConfig{ImageConcurrency: config.ImageConcurrencyConfig{
		Enabled:               true,
		MaxConcurrentRequests: 1,
		OverflowMode:          config.ImageConcurrencyOverflowModeReject,
		DistributedEnabled:    true,
		LeaseTTLSeconds:       30,
	}}}
	h := NewOpenAIGatewayHandler(nil, service.NewConcurrencyService(nil), nil, nil, nil, nil, nil, nil, cfg)

	release, acquired, observation := h.acquireImageTurnSlot(context.Background(), false)
	require.True(t, acquired)
	require.Nil(t, release)
	require.Equal(t, imageConcurrencyOutcomeSkipped, observation.Outcome)

	release, acquired, observation = h.acquireImageTurnSlot(context.Background(), true)
	require.False(t, acquired)
	require.Nil(t, release)
	require.Equal(t, imageConcurrencyOutcomeRejected, observation.Outcome)
}

func TestOpenAIGatewayHandlerDistributedImageAdmissionWaitsForGlobalCapacity(t *testing.T) {
	redisServer := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: redisServer.Addr()})
	t.Cleanup(func() { _ = client.Close() })
	cache := repository.NewConcurrencyCache(client, 1, 60)
	cfg := &config.Config{Gateway: config.GatewayConfig{ImageConcurrency: config.ImageConcurrencyConfig{
		Enabled:               true,
		MaxConcurrentRequests: 1,
		OverflowMode:          config.ImageConcurrencyOverflowModeWait,
		WaitTimeoutSeconds:    2,
		MaxWaitingRequests:    1,
		DistributedEnabled:    true,
		LeaseTTLSeconds:       30,
	}}}
	first := NewOpenAIGatewayHandler(nil, service.NewConcurrencyService(cache), nil, nil, nil, nil, nil, nil, cfg)
	second := NewOpenAIGatewayHandler(nil, service.NewConcurrencyService(cache), nil, nil, nil, nil, nil, nil, cfg)

	firstRelease, acquired, _ := first.acquireImageTurnSlot(context.Background(), true)
	require.True(t, acquired)

	type admissionResult struct {
		release     func()
		acquired    bool
		observation imageConcurrencyObservation
	}
	resultCh := make(chan admissionResult, 1)
	go func() {
		release, ok, observation := second.acquireImageTurnSlot(context.Background(), true)
		resultCh <- admissionResult{release: release, acquired: ok, observation: observation}
	}()

	select {
	case <-resultCh:
		t.Fatal("distributed wait returned before global capacity was released")
	case <-time.After(100 * time.Millisecond):
	}
	firstRelease()

	select {
	case result := <-resultCh:
		require.True(t, result.acquired)
		require.Equal(t, imageConcurrencyOutcomeAcquired, result.observation.Outcome)
		require.NotNil(t, result.release)
		result.release()
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for distributed image capacity")
	}
}

func TestOpenAIGatewayHandlerDistributedImageAdmissionBoundsGlobalWaiters(t *testing.T) {
	redisServer := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: redisServer.Addr()})
	t.Cleanup(func() { _ = client.Close() })
	cache := repository.NewConcurrencyCache(client, 1, 60)
	cfg := &config.Config{Gateway: config.GatewayConfig{ImageConcurrency: config.ImageConcurrencyConfig{
		Enabled:               true,
		MaxConcurrentRequests: 1,
		OverflowMode:          config.ImageConcurrencyOverflowModeWait,
		WaitTimeoutSeconds:    2,
		MaxWaitingRequests:    1,
		DistributedEnabled:    true,
		LeaseTTLSeconds:       30,
	}}}
	holder := NewOpenAIGatewayHandler(nil, service.NewConcurrencyService(cache), nil, nil, nil, nil, nil, nil, cfg)
	waiter := NewOpenAIGatewayHandler(nil, service.NewConcurrencyService(cache), nil, nil, nil, nil, nil, nil, cfg)

	holderRelease, acquired, _ := holder.acquireImageTurnSlot(context.Background(), true)
	require.True(t, acquired)
	defer holderRelease()

	waitingDone := make(chan struct{})
	go func() {
		release, ok, _ := waiter.acquireImageTurnSlot(context.Background(), true)
		if ok && release != nil {
			release()
		}
		close(waitingDone)
	}()

	deadline := time.Now().Add(time.Second)
	for waiter.imageLimiter.Snapshot().Waiting != 1 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	require.Equal(t, 1, waiter.imageLimiter.Snapshot().Waiting)

	release, acquired, observation := waiter.acquireImageTurnSlot(context.Background(), true)
	require.False(t, acquired)
	require.Nil(t, release)
	require.Equal(t, imageConcurrencyOutcomeQueueFull, observation.Outcome)

	holderRelease()
	select {
	case <-waitingDone:
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for bounded distributed waiter")
	}
}

func TestOpenAIGatewayHandlerDistributedImageAdmissionWaitCancellationCleansState(t *testing.T) {
	redisServer := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: redisServer.Addr()})
	t.Cleanup(func() { _ = client.Close() })
	cache := repository.NewConcurrencyCache(client, 1, 60)
	cfg := &config.Config{Gateway: config.GatewayConfig{ImageConcurrency: config.ImageConcurrencyConfig{
		Enabled:               true,
		MaxConcurrentRequests: 1,
		OverflowMode:          config.ImageConcurrencyOverflowModeWait,
		WaitTimeoutSeconds:    2,
		MaxWaitingRequests:    1,
		DistributedEnabled:    true,
		LeaseTTLSeconds:       30,
	}}}
	holder := NewOpenAIGatewayHandler(nil, service.NewConcurrencyService(cache), nil, nil, nil, nil, nil, nil, cfg)
	waiter := NewOpenAIGatewayHandler(nil, service.NewConcurrencyService(cache), nil, nil, nil, nil, nil, nil, cfg)
	holderRelease, acquired, _ := holder.acquireImageTurnSlot(context.Background(), true)
	require.True(t, acquired)

	waitCtx, cancelWait := context.WithCancel(context.Background())
	resultCh := make(chan imageConcurrencyObservation, 1)
	go func() {
		release, ok, observation := waiter.acquireImageTurnSlot(waitCtx, true)
		if ok && release != nil {
			release()
		}
		resultCh <- observation
	}()
	deadline := time.Now().Add(time.Second)
	for waiter.imageLimiter.Snapshot().Waiting != 1 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	require.Equal(t, 1, waiter.imageLimiter.Snapshot().Waiting)
	cancelWait()

	select {
	case observation := <-resultCh:
		require.Equal(t, imageConcurrencyOutcomeCanceled, observation.Outcome)
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for distributed waiter cancellation")
	}
	require.Zero(t, waiter.imageLimiter.Snapshot().Waiting)
	members, err := client.ZCard(context.Background(), "concurrency:image:global").Result()
	require.NoError(t, err)
	require.EqualValues(t, 1, members)

	holderRelease()
	members, err = client.ZCard(context.Background(), "concurrency:image:global").Result()
	require.NoError(t, err)
	require.Zero(t, members)
}

func TestOpenAIGatewayHandlerDistributedImageLeaseLossCancelsRequestContext(t *testing.T) {
	gin.SetMode(gin.TestMode)
	redisServer := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: redisServer.Addr()})
	t.Cleanup(func() { _ = client.Close() })
	cache := repository.NewConcurrencyCache(client, 1, 60)
	cfg := &config.Config{Gateway: config.GatewayConfig{ImageConcurrency: config.ImageConcurrencyConfig{
		Enabled:               true,
		MaxConcurrentRequests: 1,
		OverflowMode:          config.ImageConcurrencyOverflowModeReject,
		DistributedEnabled:    true,
		LeaseTTLSeconds:       3,
	}}}
	h := NewOpenAIGatewayHandler(nil, service.NewConcurrencyService(cache), nil, nil, nil, nil, nil, nil, cfg)
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/images/generations", nil)

	release, acquired := h.acquireImageGenerationSlot(c, false)
	require.True(t, acquired)
	require.NotNil(t, release)
	members, err := client.ZRange(context.Background(), "concurrency:image:global", 0, -1).Result()
	require.NoError(t, err)
	require.Len(t, members, 1)
	require.NoError(t, client.ZRem(context.Background(), "concurrency:image:global", members[0]).Err())

	select {
	case <-c.Request.Context().Done():
		require.ErrorIs(t, context.Cause(c.Request.Context()), service.ErrImageConcurrencyLeaseLost)
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for image lease loss cancellation")
	}
	release()
}

func TestOpenAIGatewayHandlerProcessLocalImageAdmissionPreservesRequestContext(t *testing.T) {
	gin.SetMode(gin.TestMode)
	cfg := &config.Config{Gateway: config.GatewayConfig{ImageConcurrency: config.ImageConcurrencyConfig{
		Enabled:               true,
		MaxConcurrentRequests: 1,
		OverflowMode:          config.ImageConcurrencyOverflowModeReject,
		DistributedEnabled:    false,
	}}}
	h := NewOpenAIGatewayHandler(nil, nil, nil, nil, nil, nil, nil, nil, cfg)
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	originalCtx := context.WithValue(context.Background(), struct{}{}, "sentinel")
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/images/generations", nil).WithContext(originalCtx)

	release, acquired := h.acquireImageGenerationSlot(c, false)
	require.True(t, acquired)
	require.Same(t, originalCtx, c.Request.Context())
	require.NotNil(t, release)
	release()
}

func TestOpenAIGatewayHandlerDistributedImageNormalReleaseDoesNotReportLeaseLoss(t *testing.T) {
	gin.SetMode(gin.TestMode)
	redisServer := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: redisServer.Addr()})
	t.Cleanup(func() { _ = client.Close() })
	cache := repository.NewConcurrencyCache(client, 1, 60)
	cfg := &config.Config{Gateway: config.GatewayConfig{ImageConcurrency: config.ImageConcurrencyConfig{
		Enabled:               true,
		MaxConcurrentRequests: 1,
		OverflowMode:          config.ImageConcurrencyOverflowModeReject,
		DistributedEnabled:    true,
		LeaseTTLSeconds:       3,
	}}}
	h := NewOpenAIGatewayHandler(nil, service.NewConcurrencyService(cache), nil, nil, nil, nil, nil, nil, cfg)
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/images/generations", nil)

	release, acquired := h.acquireImageGenerationSlot(c, false)
	require.True(t, acquired)
	release()
	release()

	require.ErrorIs(t, context.Cause(c.Request.Context()), context.Canceled)
	require.NotErrorIs(t, context.Cause(c.Request.Context()), service.ErrImageConcurrencyLeaseLost)
	require.Zero(t, h.imageLimiter.Snapshot().Active)
}
