package handler

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/domain"
	pkghttputil "github.com/Wei-Shaw/sub2api/internal/pkg/httputil"
	"github.com/Wei-Shaw/sub2api/internal/pkg/ip"
	middleware2 "github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/tidwall/gjson"
	"go.uber.org/zap"
	"golang.org/x/sync/semaphore"
)

const (
	cursorDefaultModel                = "cursor-auto"
	cursorSidecarAPIKeyHeader         = "X-Cursor-Sidecar-Key"
	cursorSidecarAccountRefHeader     = "X-Cursor-Account-Ref"
	cursorSidecarAccountIDHeader      = "X-Cursor-Account-ID"
	cursorSidecarOriginalPathHeader   = "X-Cursor-Original-Path"
	cursorSidecarClientRequestID      = "X-Request-ID"
	cursorMaxSidecarResponseBytes     = int64(16 * 1024 * 1024)
	cursorDefaultRequestTimeout       = 90 * time.Second
	cursorSidecarLimiterAcquireWeight = int64(1)
)

var cursorSidecarLimiters sync.Map // map[int]*semaphore.Weighted

func (h *GatewayHandler) CursorModels(c *gin.Context) {
	h.forwardCursorSidecarList(c, "/v1/models")
}

func (h *GatewayHandler) CursorMessages(c *gin.Context) {
	h.handleCursorSidecarPost(c, "/v1/messages", true)
}

func (h *GatewayHandler) CursorCountTokens(c *gin.Context) {
	h.handleCursorSidecarPost(c, "/v1/messages/count_tokens", false)
}

func (h *GatewayHandler) CursorResponses(c *gin.Context) {
	h.handleCursorSidecarPost(c, "/v1/responses", true)
}

func (h *GatewayHandler) CursorChatCompletions(c *gin.Context) {
	h.handleCursorSidecarPost(c, "/v1/chat/completions", true)
}

type cursorSidecarRequest struct {
	Method       string
	Path         string
	Model        string
	UpstreamBody []byte
	RequestBody  []byte
	Stream       bool
	RecordUsage  bool
	Parsed       *service.ParsedRequest
	Mapping      service.ChannelMappingResult
}

func (h *GatewayHandler) handleCursorSidecarPost(c *gin.Context, sidecarPath string, recordUsage bool) {
	body, err := pkghttputil.ReadRequestBodyWithPrealloc(c.Request)
	if err != nil {
		if maxErr, ok := extractMaxBytesError(err); ok {
			h.errorResponse(c, http.StatusRequestEntityTooLarge, "invalid_request_error", buildBodyTooLargeMessage(maxErr.Limit))
			return
		}
		h.errorResponse(c, http.StatusBadRequest, "invalid_request_error", "Failed to read request body")
		return
	}
	if len(body) == 0 {
		h.errorResponse(c, http.StatusBadRequest, "invalid_request_error", "Request body is empty")
		return
	}
	if !gjson.ValidBytes(body) {
		h.errorResponse(c, http.StatusBadRequest, "invalid_request_error", "Failed to parse request body")
		return
	}

	model := strings.TrimSpace(gjson.GetBytes(body, "model").String())
	if model == "" {
		model = cursorDefaultModel
	}
	stream := gjson.GetBytes(body, "stream").Bool()
	parsed, err := service.ParseGatewayRequest(body, domain.PlatformAnthropic)
	if err != nil {
		parsed = &service.ParsedRequest{Body: body, Model: model, Stream: stream}
	}
	parsed.Model = model
	parsed.Stream = stream
	parsed.SessionContext = &service.SessionContext{
		ClientIP:  ip.GetClientIP(c),
		UserAgent: c.GetHeader("User-Agent"),
	}

	upstreamBody := body
	mapping := service.ChannelMappingResult{MappedModel: model}
	if apiKey, ok := middleware2.GetAPIKeyFromContext(c); ok && h != nil && h.gatewayService != nil {
		mapping, _ = h.gatewayService.ResolveChannelMappingAndRestrict(c.Request.Context(), apiKey.GroupID, model)
		if mapping.Mapped && strings.TrimSpace(mapping.MappedModel) != "" && mapping.MappedModel != model {
			upstreamBody = h.gatewayService.ReplaceModelInBody(body, mapping.MappedModel)
		}
	}

	h.handleCursorSidecar(c, cursorSidecarRequest{
		Method:       http.MethodPost,
		Path:         sidecarPath,
		Model:        model,
		UpstreamBody: upstreamBody,
		RequestBody:  body,
		Stream:       stream,
		RecordUsage:  recordUsage,
		Parsed:       parsed,
		Mapping:      mapping,
	})
}

func (h *GatewayHandler) handleCursorSidecar(c *gin.Context, req cursorSidecarRequest) {
	requestStart := time.Now()
	streamStarted := false

	apiKey, ok := middleware2.GetAPIKeyFromContext(c)
	if !ok {
		h.errorResponse(c, http.StatusUnauthorized, "authentication_error", "Invalid API key")
		return
	}
	if apiKey.Group == nil || apiKey.Group.Platform != service.PlatformCursor {
		h.errorResponse(c, http.StatusForbidden, "authentication_error", "Cursor endpoint requires an API key assigned to a cursor group")
		return
	}
	if h == nil || h.gatewayService == nil {
		h.errorResponse(c, http.StatusServiceUnavailable, "api_error", "Cursor bridge is not configured")
		return
	}

	subject, hasSubject := middleware2.GetAuthSubjectFromContext(c)
	if !hasSubject && req.RecordUsage {
		h.errorResponse(c, http.StatusInternalServerError, "api_error", "User context not found")
		return
	}
	reqLog := requestLogger(
		c,
		"handler.gateway.cursor",
		zap.Int64("api_key_id", apiKey.ID),
		zap.Any("group_id", apiKey.GroupID),
		zap.String("model", req.Model),
		zap.Bool("stream", req.Stream),
	)

	if req.RecordUsage {
		setOpsRequestContext(c, req.Model, req.Stream, req.RequestBody)
		setOpsEndpointContext(c, "", int16(service.RequestTypeFromLegacy(req.Stream, false)))
	} else {
		setOpsEndpointContext(c, "", int16(service.RequestTypeFromLegacy(false, false)))
	}
	if h.errorPassthroughService != nil {
		service.BindErrorPassthroughService(c, h.errorPassthroughService)
	}

	subscription, _ := middleware2.GetSubscriptionFromContext(c)
	if req.RecordUsage {
		userRelease, ok := h.acquireCursorUserSlot(c, subject, req.Stream, &streamStarted, reqLog)
		if !ok {
			return
		}
		if userRelease != nil {
			defer userRelease()
		}

		if h.billingCacheService != nil {
			if err := h.billingCacheService.CheckBillingEligibility(c.Request.Context(), apiKey.User, apiKey, apiKey.Group, subscription); err != nil {
				reqLog.Info("gateway.cursor.billing_eligibility_check_failed", zap.Error(err))
				status, code, message, retryAfter := billingErrorDetails(err)
				if retryAfter > 0 {
					c.Header("Retry-After", strconv.Itoa(retryAfter))
				}
				h.handleStreamingAwareError(c, status, code, message, streamStarted)
				return
			}
		}
	}

	sessionHash := ""
	if req.Parsed != nil {
		req.Parsed.GroupID = apiKey.GroupID
		if req.Parsed.SessionContext == nil {
			req.Parsed.SessionContext = &service.SessionContext{}
		}
		req.Parsed.SessionContext.APIKeyID = apiKey.ID
		sessionHash = h.gatewayService.GenerateSessionHash(req.Parsed)
	}
	if sessionHash != "" {
		sessionHash = "cursor:" + sessionHash
	}

	fs := NewFailoverState(h.maxAccountSwitches, false)
	for {
		selection, err := h.gatewayService.SelectAccountWithLoadAwareness(c.Request.Context(), apiKey.GroupID, sessionHash, req.Model, fs.FailedAccountIDs, "", subject.UserID)
		if err != nil {
			if len(fs.FailedAccountIDs) == 0 {
				reqLog.Warn("gateway.cursor.select_account_no_available", zap.Error(err))
				h.handleStreamingAwareError(c, http.StatusServiceUnavailable, "api_error", "No available Cursor accounts: "+err.Error(), streamStarted)
				return
			}
			action := fs.HandleSelectionExhausted(c.Request.Context())
			switch action {
			case FailoverContinue:
				continue
			case FailoverCanceled:
				return
			default:
				if fs.LastFailoverErr != nil {
					h.handleFailoverExhausted(c, fs.LastFailoverErr, service.PlatformCursor, streamStarted)
				} else {
					h.handleFailoverExhaustedSimple(c, http.StatusBadGateway, streamStarted)
				}
				return
			}
		}

		account := selection.Account
		if account == nil || account.Platform != service.PlatformCursor {
			if selection.Acquired && selection.ReleaseFunc != nil {
				selection.ReleaseFunc()
			}
			h.handleStreamingAwareError(c, http.StatusServiceUnavailable, "api_error", "Selected account is not a Cursor account", streamStarted)
			return
		}
		setOpsSelectedAccount(c, account.ID, account.Platform)

		accountRelease, acquired := h.acquireCursorAccountSlot(c, selection, req.Stream, &streamStarted, reqLog)
		if !acquired {
			return
		}

		writerSizeBeforeForward := c.Writer.Size()
		result, err := h.forwardCursorSidecar(c, account, req)
		if accountRelease != nil {
			accountRelease()
		}

		if err != nil {
			var failoverErr *service.UpstreamFailoverError
			if errors.As(err, &failoverErr) {
				if c.Writer.Size() != writerSizeBeforeForward {
					h.handleFailoverExhausted(c, failoverErr, service.PlatformCursor, true)
					return
				}
				action := fs.HandleFailoverError(c.Request.Context(), h.gatewayService, account.ID, account.Platform, failoverErr)
				switch action {
				case FailoverContinue:
					continue
				case FailoverExhausted:
					h.handleFailoverExhausted(c, fs.LastFailoverErr, service.PlatformCursor, streamStarted)
					return
				case FailoverCanceled:
					return
				}
			}
			h.ensureForwardErrorResponse(c, streamStarted)
			reqLog.Error("gateway.cursor.forward_failed", zap.Int64("account_id", account.ID), zap.Error(err))
			return
		}

		if req.RecordUsage {
			userAgent := c.GetHeader("User-Agent")
			clientIP := ip.GetClientIP(c)
			requestPayloadHash := service.HashUsageRequestPayload(req.RequestBody)
			inboundEndpoint := GetInboundEndpoint(c)
			upstreamEndpoint := req.Path
			mappingFields := req.Mapping.ToUsageFields(req.Model, result.UpstreamModel)

			h.submitUsageRecordTask(func(ctx context.Context) {
				if err := h.gatewayService.RecordUsage(ctx, &service.RecordUsageInput{
					Result:             result,
					ParsedRequest:      req.Parsed,
					APIKey:             apiKey,
					User:               apiKey.User,
					Account:            account,
					Subscription:       subscription,
					InboundEndpoint:    inboundEndpoint,
					UpstreamEndpoint:   upstreamEndpoint,
					UserAgent:          userAgent,
					IPAddress:          clientIP,
					RequestPayloadHash: requestPayloadHash,
					ForceCacheBilling:  fs.ForceCacheBilling,
					APIKeyService:      h.apiKeyService,
					ChannelUsageFields: mappingFields,
				}); err != nil {
					reqLog.Error("gateway.cursor.record_usage_failed", zap.Int64("account_id", account.ID), zap.Error(err))
				}
			})
		}

		service.SetOpsLatencyMs(c, service.OpsUpstreamLatencyMsKey, time.Since(requestStart).Milliseconds())
		return
	}
}

func (h *GatewayHandler) acquireCursorUserSlot(c *gin.Context, subject middleware2.AuthSubject, stream bool, streamStarted *bool, reqLog *zap.Logger) (func(), bool) {
	if h.concurrencyHelper == nil || h.concurrencyHelper.concurrencyService == nil {
		return nil, true
	}
	maxWait := service.CalculateMaxWait(subject.Concurrency)
	canWait, err := h.concurrencyHelper.IncrementWaitCount(c.Request.Context(), subject.UserID, maxWait)
	waitCounted := false
	if err != nil {
		reqLog.Warn("gateway.cursor.user_wait_counter_increment_failed", zap.Error(err))
	} else if !canWait {
		h.handleStreamingAwareError(c, http.StatusTooManyRequests, "rate_limit_error", "Too many pending requests, please retry later", *streamStarted)
		return nil, false
	}
	if err == nil && canWait {
		waitCounted = true
	}
	defer func() {
		if waitCounted {
			h.concurrencyHelper.DecrementWaitCount(c.Request.Context(), subject.UserID)
		}
	}()

	userRelease, err := h.concurrencyHelper.AcquireUserSlotWithWait(c, subject.UserID, subject.Concurrency, stream, streamStarted)
	if err != nil {
		reqLog.Warn("gateway.cursor.user_slot_acquire_failed", zap.Error(err))
		h.handleConcurrencyError(c, err, "user", *streamStarted)
		return nil, false
	}
	if waitCounted {
		h.concurrencyHelper.DecrementWaitCount(c.Request.Context(), subject.UserID)
		waitCounted = false
	}
	return wrapReleaseOnDone(c.Request.Context(), userRelease), true
}

func (h *GatewayHandler) acquireCursorAccountSlot(c *gin.Context, selection *service.AccountSelectionResult, stream bool, streamStarted *bool, reqLog *zap.Logger) (func(), bool) {
	if selection == nil || selection.Account == nil {
		h.handleStreamingAwareError(c, http.StatusServiceUnavailable, "api_error", "No available Cursor accounts", *streamStarted)
		return nil, false
	}
	if selection.Acquired {
		return wrapReleaseOnDone(c.Request.Context(), selection.ReleaseFunc), true
	}
	if h.concurrencyHelper == nil || h.concurrencyHelper.concurrencyService == nil {
		return nil, true
	}
	if selection.WaitPlan == nil {
		h.handleStreamingAwareError(c, http.StatusServiceUnavailable, "api_error", "No available Cursor accounts", *streamStarted)
		return nil, false
	}

	account := selection.Account
	accountWaitCounted := false
	canWait, err := h.concurrencyHelper.IncrementAccountWaitCount(c.Request.Context(), account.ID, selection.WaitPlan.MaxWaiting)
	if err != nil {
		reqLog.Warn("gateway.cursor.account_wait_counter_increment_failed", zap.Int64("account_id", account.ID), zap.Error(err))
	} else if !canWait {
		h.handleStreamingAwareError(c, http.StatusTooManyRequests, "rate_limit_error", "Too many pending requests, please retry later", *streamStarted)
		return nil, false
	}
	if err == nil && canWait {
		accountWaitCounted = true
	}
	releaseWait := func() {
		if accountWaitCounted {
			h.concurrencyHelper.DecrementAccountWaitCount(c.Request.Context(), account.ID)
			accountWaitCounted = false
		}
	}

	accountRelease, err := h.concurrencyHelper.AcquireAccountSlotWithWaitTimeout(
		c,
		account.ID,
		selection.WaitPlan.MaxConcurrency,
		selection.WaitPlan.Timeout,
		stream,
		streamStarted,
	)
	if err != nil {
		releaseWait()
		reqLog.Warn("gateway.cursor.account_slot_acquire_failed", zap.Int64("account_id", account.ID), zap.Error(err))
		h.handleConcurrencyError(c, err, "account", *streamStarted)
		return nil, false
	}
	releaseWait()
	return wrapReleaseOnDone(c.Request.Context(), accountRelease), true
}

func (h *GatewayHandler) forwardCursorSidecar(c *gin.Context, account *service.Account, req cursorSidecarRequest) (*service.ForwardResult, error) {
	cfg := h.cursorConfig()
	baseURL, err := validateConfiguredCursorSidecarURL(cfg.SidecarURL)
	if err != nil {
		h.errorResponse(c, http.StatusServiceUnavailable, "api_error", "Cursor sidecar is not configured")
		return nil, err
	}

	releaseSidecarSlot, err := acquireCursorSidecarSlot(c.Request.Context(), cfg.MaxConcurrency)
	if err != nil {
		h.handleStreamingAwareError(c, http.StatusTooManyRequests, "rate_limit_error", "Cursor sidecar is busy, please retry later", false)
		return nil, err
	}
	defer releaseSidecarSlot()

	sidecarURL, err := joinCursorSidecarURL(baseURL, req.Path)
	if err != nil {
		h.errorResponse(c, http.StatusServiceUnavailable, "api_error", "Cursor sidecar URL is invalid")
		return nil, err
	}

	start := time.Now()
	ctx, cancel := context.WithTimeout(c.Request.Context(), cfg.requestTimeout())
	defer cancel()

	var bodyReader io.Reader = http.NoBody
	if req.Method != http.MethodGet {
		bodyReader = bytes.NewReader(req.UpstreamBody)
	}
	sidecarReq, err := http.NewRequestWithContext(ctx, req.Method, sidecarURL, bodyReader)
	if err != nil {
		return nil, err
	}
	h.applyCursorSidecarHeaders(c, sidecarReq, account)

	httpClient := &http.Client{Timeout: cfg.requestTimeout()}
	resp, err := httpClient.Do(sidecarReq)
	if err != nil {
		service.SetOpsUpstreamError(c, http.StatusServiceUnavailable, err.Error(), "")
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()

	if cursorShouldFailoverStatus(resp.StatusCode) {
		body, _ := readCursorSidecarBody(resp.Body)
		service.SetOpsUpstreamError(c, resp.StatusCode, service.ExtractUpstreamErrorMessage(body), "")
		return nil, &service.UpstreamFailoverError{
			StatusCode:      resp.StatusCode,
			ResponseBody:    body,
			ResponseHeaders: resp.Header.Clone(),
		}
	}

	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		body, _ := readCursorSidecarBody(resp.Body)
		upstreamMsg := service.ExtractUpstreamErrorMessage(body)
		service.SetOpsUpstreamError(c, resp.StatusCode, upstreamMsg, "")
		copyCursorSidecarHeaders(c.Writer.Header(), resp.Header)
		c.Data(resp.StatusCode, resp.Header.Get("Content-Type"), body)
		if upstreamMsg != "" {
			return nil, fmt.Errorf("cursor sidecar upstream error: %d %s", resp.StatusCode, upstreamMsg)
		}
		return nil, fmt.Errorf("cursor sidecar upstream error: %d", resp.StatusCode)
	}

	copyCursorSidecarHeaders(c.Writer.Header(), resp.Header)
	c.Status(resp.StatusCode)
	if req.Stream || isCursorStreamingContentType(resp.Header.Get("Content-Type")) {
		if _, err := io.Copy(c.Writer, resp.Body); err != nil {
			return nil, err
		}
		if flusher, ok := c.Writer.(http.Flusher); ok {
			flusher.Flush()
		}
		return &service.ForwardResult{
			RequestID:     resp.Header.Get("X-Request-ID"),
			Usage:         service.ClaudeUsage{},
			Model:         req.Model,
			UpstreamModel: resolvedCursorUpstreamModel(req),
			Stream:        true,
			Duration:      time.Since(start),
		}, nil
	}

	body, err := readCursorSidecarBody(resp.Body)
	if err != nil {
		return nil, err
	}
	if _, err := c.Writer.Write(body); err != nil {
		return nil, err
	}

	return &service.ForwardResult{
		RequestID:     resp.Header.Get("X-Request-ID"),
		Usage:         extractCursorUsage(body),
		Model:         req.Model,
		UpstreamModel: resolvedCursorUpstreamModel(req),
		Stream:        false,
		Duration:      time.Since(start),
	}, nil
}

func (h *GatewayHandler) forwardCursorSidecarList(c *gin.Context, sidecarPath string) {
	cfg := h.cursorConfig()
	baseURL, err := validateConfiguredCursorSidecarURL(cfg.SidecarURL)
	if err != nil {
		h.errorResponse(c, http.StatusServiceUnavailable, "api_error", "Cursor sidecar is not configured")
		return
	}
	sidecarURL, err := joinCursorSidecarURL(baseURL, sidecarPath)
	if err != nil {
		h.errorResponse(c, http.StatusServiceUnavailable, "api_error", "Cursor sidecar URL is invalid")
		return
	}

	ctx, cancel := context.WithTimeout(c.Request.Context(), cfg.requestTimeout())
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, sidecarURL, http.NoBody)
	if err != nil {
		h.errorResponse(c, http.StatusInternalServerError, "api_error", "Failed to create Cursor sidecar request")
		return
	}
	h.applyCursorSidecarHeaders(c, req, nil)

	resp, err := (&http.Client{Timeout: cfg.requestTimeout()}).Do(req)
	if err != nil {
		service.SetOpsUpstreamError(c, http.StatusServiceUnavailable, err.Error(), "")
		h.errorResponse(c, http.StatusServiceUnavailable, "api_error", "Cursor sidecar request failed")
		return
	}
	defer func() { _ = resp.Body.Close() }()

	body, err := readCursorSidecarBody(resp.Body)
	if err != nil {
		h.errorResponse(c, http.StatusBadGateway, "api_error", "Cursor sidecar response is too large or invalid")
		return
	}
	copyCursorSidecarHeaders(c.Writer.Header(), resp.Header)
	c.Data(resp.StatusCode, resp.Header.Get("Content-Type"), body)
}

func (h *GatewayHandler) applyCursorSidecarHeaders(c *gin.Context, req *http.Request, account *service.Account) {
	req.Header.Set("Content-Type", "application/json")
	cfg := h.cursorConfig()
	if cfg.SidecarAPIKey != "" {
		req.Header.Set(cursorSidecarAPIKeyHeader, cfg.SidecarAPIKey)
	}
	if account != nil {
		req.Header.Set(cursorSidecarAccountIDHeader, strconv.FormatInt(account.ID, 10))
		req.Header.Set(cursorSidecarAccountRefHeader, service.CursorSidecarAccountRef(account))
	}
	req.Header.Set(cursorSidecarOriginalPathHeader, c.Request.URL.Path)
	if rid := c.GetHeader(cursorSidecarClientRequestID); strings.TrimSpace(rid) != "" {
		req.Header.Set(cursorSidecarClientRequestID, rid)
	}
	if ua := c.GetHeader("User-Agent"); strings.TrimSpace(ua) != "" {
		req.Header.Set("User-Agent", ua)
	}
}

func resolvedCursorUpstreamModel(req cursorSidecarRequest) string {
	if req.Mapping.Mapped && strings.TrimSpace(req.Mapping.MappedModel) != "" && req.Mapping.MappedModel != req.Model {
		return req.Mapping.MappedModel
	}
	return ""
}

type cursorRuntimeConfig struct {
	SidecarURL            string
	SidecarAPIKey         string
	MaxConcurrency        int
	RequestTimeoutSeconds int
}

func (h *GatewayHandler) cursorConfig() cursorRuntimeConfig {
	cfg := cursorRuntimeConfig{
		MaxConcurrency:        1,
		RequestTimeoutSeconds: int(cursorDefaultRequestTimeout / time.Second),
	}
	if h != nil && h.cfg != nil {
		cfg.SidecarURL = h.cfg.Cursor.SidecarURL
		cfg.SidecarAPIKey = h.cfg.Cursor.SidecarAPIKey
		cfg.MaxConcurrency = h.cfg.Cursor.MaxConcurrency
		cfg.RequestTimeoutSeconds = h.cfg.Cursor.RequestTimeoutSeconds
	}
	if sidecarURL := strings.TrimSpace(os.Getenv("CURSOR_SIDECAR_URL")); sidecarURL != "" {
		cfg.SidecarURL = sidecarURL
	}
	if sidecarAPIKey := strings.TrimSpace(os.Getenv("CURSOR_SIDECAR_API_KEY")); sidecarAPIKey != "" {
		cfg.SidecarAPIKey = sidecarAPIKey
	}
	if maxConcurrency := envPositiveInt("CURSOR_MAX_CONCURRENCY"); maxConcurrency > 0 {
		cfg.MaxConcurrency = maxConcurrency
	}
	if timeoutSeconds := envPositiveInt("CURSOR_REQUEST_TIMEOUT_SECONDS"); timeoutSeconds > 0 {
		cfg.RequestTimeoutSeconds = timeoutSeconds
	}
	return cfg
}

func (c cursorRuntimeConfig) requestTimeout() time.Duration {
	if c.RequestTimeoutSeconds <= 0 {
		return cursorDefaultRequestTimeout
	}
	return time.Duration(c.RequestTimeoutSeconds) * time.Second
}

func validateConfiguredCursorSidecarURL(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", errors.New("cursor sidecar url is empty")
	}
	if err := config.ValidateAbsoluteHTTPURL(raw); err != nil {
		return "", err
	}
	u, err := url.Parse(raw)
	if err != nil {
		return "", err
	}
	if u.User != nil || u.RawQuery != "" || u.ForceQuery {
		return "", errors.New("cursor sidecar url must not include userinfo or query")
	}
	return strings.TrimRight(raw, "/"), nil
}

func joinCursorSidecarURL(baseURL, path string) (string, error) {
	u, err := url.Parse(baseURL)
	if err != nil {
		return "", err
	}
	if strings.TrimSpace(path) == "" || !strings.HasPrefix(path, "/") {
		return "", errors.New("cursor sidecar path must be absolute")
	}
	u.Path = strings.TrimRight(u.Path, "/") + path
	u.RawQuery = ""
	u.Fragment = ""
	return u.String(), nil
}

func acquireCursorSidecarSlot(ctx context.Context, maxConcurrency int) (func(), error) {
	if maxConcurrency <= 0 {
		maxConcurrency = 1
	}
	raw, _ := cursorSidecarLimiters.LoadOrStore(maxConcurrency, semaphore.NewWeighted(int64(maxConcurrency)))
	limiter := raw.(*semaphore.Weighted)
	if err := limiter.Acquire(ctx, cursorSidecarLimiterAcquireWeight); err != nil {
		return nil, err
	}
	return func() {
		limiter.Release(cursorSidecarLimiterAcquireWeight)
	}, nil
}

func readCursorSidecarBody(body io.Reader) ([]byte, error) {
	limited := io.LimitReader(body, cursorMaxSidecarResponseBytes+1)
	data, err := io.ReadAll(limited)
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > cursorMaxSidecarResponseBytes {
		return nil, fmt.Errorf("cursor sidecar response exceeds %d bytes", cursorMaxSidecarResponseBytes)
	}
	return data, nil
}

func extractCursorUsage(body []byte) service.ClaudeUsage {
	return service.ClaudeUsage{
		InputTokens:              firstKiroInt(body, "usage.input_tokens", "usage.prompt_tokens", "usage.inputTokens", "usage.promptTokenCount"),
		OutputTokens:             firstKiroInt(body, "usage.output_tokens", "usage.completion_tokens", "usage.outputTokens", "usage.candidatesTokenCount"),
		CacheCreationInputTokens: firstKiroInt(body, "usage.cache_creation_input_tokens", "usage.cache_creation_tokens"),
		CacheReadInputTokens:     firstKiroInt(body, "usage.cache_read_input_tokens", "usage.cache_read_tokens"),
	}
}

func copyCursorSidecarHeaders(dst, src http.Header) {
	for key, values := range src {
		if !shouldCopyCursorSidecarHeader(key) {
			continue
		}
		for _, value := range values {
			dst.Add(key, value)
		}
	}
}

func shouldCopyCursorSidecarHeader(key string) bool {
	switch strings.ToLower(strings.TrimSpace(key)) {
	case "connection", "keep-alive", "proxy-authenticate", "proxy-authorization",
		"te", "trailer", "transfer-encoding", "upgrade", "content-length",
		"authorization", "set-cookie", strings.ToLower(cursorSidecarAPIKeyHeader):
		return false
	default:
		return true
	}
}

func isCursorStreamingContentType(contentType string) bool {
	contentType = strings.ToLower(contentType)
	return strings.Contains(contentType, "text/event-stream") || strings.Contains(contentType, "application/x-ndjson")
}

func cursorShouldFailoverStatus(statusCode int) bool {
	switch statusCode {
	case http.StatusUnauthorized,
		http.StatusForbidden,
		http.StatusTooManyRequests,
		http.StatusInternalServerError,
		http.StatusBadGateway,
		http.StatusServiceUnavailable,
		http.StatusGatewayTimeout,
		529:
		return true
	default:
		return false
	}
}
