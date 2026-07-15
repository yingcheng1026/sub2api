package handler

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	pkghttputil "github.com/Wei-Shaw/sub2api/internal/pkg/httputil"
	"github.com/Wei-Shaw/sub2api/internal/pkg/ip"
	"github.com/Wei-Shaw/sub2api/internal/pkg/logger"
	middleware2 "github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"go.uber.org/zap"
)

// Images handles OpenAI Images API requests.
// POST /v1/images/generations
// POST /v1/images/edits
func (h *OpenAIGatewayHandler) Images(c *gin.Context) {
	streamStarted := false
	defer h.recoverResponsesPanic(c, &streamStarted)

	requestStart := time.Now()

	apiKey, ok := middleware2.GetAPIKeyFromContext(c)
	if !ok {
		h.errorResponse(c, http.StatusUnauthorized, "authentication_error", "Invalid API key")
		return
	}

	subject, ok := middleware2.GetAuthSubjectFromContext(c)
	if !ok {
		h.errorResponse(c, http.StatusInternalServerError, "api_error", "User context not found")
		return
	}
	reqLog := requestLogger(
		c,
		"handler.openai_gateway.images",
		zap.Int64("user_id", subject.UserID),
		zap.Int64("api_key_id", apiKey.ID),
		zap.Any("group_id", apiKey.GroupID),
	)
	if !h.ensureResponsesDependencies(c, reqLog) {
		return
	}

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

	if isMultipartImagesContentType(c.GetHeader("Content-Type")) {
		setOpsRequestContext(c, "", false, nil)
	} else {
		setOpsRequestContext(c, "", false, body)
	}

	parsed, err := h.gatewayService.ParseOpenAIImagesRequest(c, body)
	if err != nil {
		h.errorResponse(c, http.StatusBadRequest, "invalid_request_error", err.Error())
		return
	}

	reqLog = reqLog.With(
		zap.String("model", parsed.Model),
		zap.Bool("stream", parsed.Stream),
		zap.Bool("multipart", parsed.Multipart),
		zap.String("capability", string(parsed.RequiredCapability)),
	)
	safetyIdentifier := service.BuildOpenAIImageSafetyIdentifier(subject.UserID, apiKey.ID, apiKey.GroupID)

	if safetyErr := service.ValidateOpenAIImagesSafetyRequest(parsed); safetyErr != nil {
		h.recordImageSafetyPolicyBlock(c, apiKey, subject, parsed, safetyIdentifier, safetyErr.PolicyRule, safetyErr.Message, safetyErr.StatusCode)
		h.errorResponse(c, safetyErr.StatusCode, safetyErr.Type, safetyErr.Message)
		return
	}

	if !service.GroupAllowsImageGeneration(apiKey.Group) {
		h.errorResponse(c, http.StatusForbidden, "permission_error", service.ImageGenerationPermissionMessage())
		return
	}
	if decision := h.checkImageContentModeration(c, reqLog, apiKey, subject, parsed.Model, parsed.ModerationBody(), safetyIdentifier); decision != nil && decision.Blocked {
		h.errorResponse(c, contentModerationStatus(decision), contentModerationErrorCode(decision), decision.Message)
		return
	}
	imageReleaseFunc, acquired := h.acquireImageGenerationSlot(c, streamStarted)
	if !acquired {
		return
	}
	if imageReleaseFunc != nil {
		defer imageReleaseFunc()
	}

	if parsed.Multipart {
		setOpsRequestContext(c, parsed.Model, parsed.Stream, nil)
	} else {
		setOpsRequestContext(c, parsed.Model, parsed.Stream, body)
	}
	setOpsEndpointContext(c, "", int16(service.RequestTypeFromLegacy(parsed.Stream, false)))

	channelMapping, _ := h.gatewayService.ResolveChannelMappingAndRestrict(c.Request.Context(), apiKey.GroupID, parsed.Model)

	if h.errorPassthroughService != nil {
		service.BindErrorPassthroughService(c, h.errorPassthroughService)
	}

	subscription, _ := middleware2.GetSubscriptionFromContext(c)
	billingCtx, err := service.PrepareUsageBillingRequestContext(c.Request.Context())
	if err != nil {
		h.errorResponse(c, http.StatusServiceUnavailable, "billing_service_error", "Billing service temporarily unavailable")
		return
	}
	c.Request = c.Request.WithContext(billingCtx)
	billingRequestBody := body
	if parsed.Multipart {
		billingRequestBody = []byte(parsed.StickySessionSeed())
	}
	usageBillingAdmission := &service.OpenAIUsageBillingAdmissionInput{
		APIKey: apiKey, User: apiKey.User, Subscription: subscription,
		RequestBody: append([]byte(nil), billingRequestBody...), RequestPayloadHash: service.HashUsageRequestPayload(billingRequestBody),
		RequestCount: parsed.N,
	}
	defer h.finalizeOpenAIUsageBillingLifecycle(c.Request.Context(), usageBillingAdmission, reqLog)

	service.SetOpsLatencyMs(c, service.OpsAuthLatencyMsKey, time.Since(requestStart).Milliseconds())
	routingStart := time.Now()

	userReleaseFunc, acquired := h.acquireResponsesUserSlot(c, subject.UserID, subject.Concurrency, parsed.Stream, &streamStarted, reqLog)
	if !acquired {
		return
	}
	if userReleaseFunc != nil {
		defer userReleaseFunc()
	}

	if err := h.billingCacheService.CheckBillingEligibility(c.Request.Context(), apiKey.User, apiKey, apiKey.Group, subscription); err != nil {
		reqLog.Info("openai.images.billing_eligibility_check_failed", zap.Error(err))
		status, code, message, retryAfter := billingErrorDetails(err)
		if retryAfter > 0 {
			c.Header("Retry-After", strconv.Itoa(retryAfter))
		}
		h.handleStreamingAwareError(c, status, code, message, streamStarted)
		return
	}

	sessionHash := h.gatewayService.GenerateExplicitSessionHash(c, body)

	maxAccountSwitches := h.maxAccountSwitches
	switchCount := 0
	failedAccountIDs := make(map[int64]struct{})
	sameAccountRetryCount := make(map[int64]int)
	var lastFailoverErr *service.UpstreamFailoverError

	for {
		reqLog.Debug("openai.images.account_selecting", zap.Int("excluded_account_count", len(failedAccountIDs)))
		selection, scheduleDecision, err := h.gatewayService.SelectAccountWithSchedulerForImages(
			c.Request.Context(),
			apiKey.GroupID,
			sessionHash,
			parsed.Model,
			failedAccountIDs,
			parsed.RequiredCapability,
		)
		if err != nil {
			reqLog.Warn("openai.images.account_select_failed",
				zap.Error(err),
				zap.Int("excluded_account_count", len(failedAccountIDs)),
			)
			if len(failedAccountIDs) == 0 {
				h.handleStreamingAwareError(c, http.StatusServiceUnavailable, "api_error", "No available compatible accounts", streamStarted)
				return
			}
			if lastFailoverErr != nil {
				h.handleFailoverExhausted(c, lastFailoverErr, streamStarted)
			} else {
				h.handleFailoverExhaustedSimple(c, 502, streamStarted)
			}
			return
		}
		if selection == nil || selection.Account == nil {
			h.handleStreamingAwareError(c, http.StatusServiceUnavailable, "api_error", "No available compatible accounts", streamStarted)
			return
		}

		reqLog.Debug("openai.images.account_schedule_decision",
			zap.String("layer", scheduleDecision.Layer),
			zap.Bool("sticky_session_hit", scheduleDecision.StickySessionHit),
			zap.Int("candidate_count", scheduleDecision.CandidateCount),
			zap.Int("top_k", scheduleDecision.TopK),
			zap.Int64("latency_ms", scheduleDecision.LatencyMs),
			zap.Float64("load_skew", scheduleDecision.LoadSkew),
		)

		account := selection.Account
		sessionHash = ensureOpenAIPoolModeSessionHash(sessionHash, account)
		reqLog.Debug("openai.images.account_selected", zap.Int64("account_id", account.ID), zap.String("account_name", account.Name))
		setOpsSelectedAccount(c, account.ID, account.Platform)

		accountReleaseFunc, acquired := h.acquireResponsesAccountSlot(c, apiKey.GroupID, sessionHash, selection, parsed.Stream, &streamStarted, reqLog)
		if !acquired {
			return
		}

		service.SetOpsLatencyMs(c, service.OpsRoutingLatencyMsKey, time.Since(routingStart).Milliseconds())
		forwardStart := time.Now()
		result, err := h.gatewayService.ForwardImages(
			c.Request.Context(),
			c,
			account,
			body,
			parsed,
			channelMapping.MappedModel,
			service.WithOpenAIImagesSafetyIdentifier(safetyIdentifier),
			service.WithOpenAIImagesOutputAuditor(h.openAIImageOutputAuditor(c, apiKey, subject, parsed, safetyIdentifier)),
			service.WithOpenAIImagesBillingPreflight(service.OpenAIForwardOptions{
				RequestedModel: parsed.Model, ChannelMapping: channelMapping, GroupID: apiKey.GroupID,
				ImagePriceConfig: openAIImagePriceConfig(apiKey.Group), RequirePricingPreflight: true,
				RequireBillingAdmission: true, UsageBilling: usageBillingAdmission,
			}),
		)
		forwardDurationMs := time.Since(forwardStart).Milliseconds()
		if accountReleaseFunc != nil {
			accountReleaseFunc()
		}
		upstreamLatencyMs, _ := getContextInt64(c, service.OpsUpstreamLatencyMsKey)
		responseLatencyMs := forwardDurationMs
		if upstreamLatencyMs > 0 && forwardDurationMs > upstreamLatencyMs {
			responseLatencyMs = forwardDurationMs - upstreamLatencyMs
		}
		service.SetOpsLatencyMs(c, service.OpsResponseLatencyMsKey, responseLatencyMs)
		if result != nil && result.FirstTokenMs != nil {
			service.SetOpsLatencyMs(c, service.OpsTimeToFirstTokenMsKey, int64(*result.FirstTokenMs))
		}
		if err != nil {
			if h.handleOpenAIUsageBillingAdmissionError(c, err, streamStarted, false) {
				return
			}
			if errors.Is(err, service.ErrOpenAIBillingPreflight) || errors.Is(err, service.ErrOpenAIPricingUnavailable) {
				h.handleStreamingAwareError(c, http.StatusBadRequest, "invalid_request_error", "OpenAI billing preflight failed", streamStarted)
				return
			}
			var outputAuditErr *service.OpenAIImageOutputAuditError
			if errors.As(err, &outputAuditErr) {
				decision := outputAuditErr.Decision
				if decision == nil {
					decision = &service.ContentModerationDecision{
						Blocked:    true,
						Message:    "Image safety review is temporarily unavailable",
						StatusCode: http.StatusServiceUnavailable,
						Action:     service.ContentModerationActionError,
					}
				}
				if !c.Writer.Written() {
					h.errorResponse(c, contentModerationStatus(decision), contentModerationErrorCode(decision), decision.Message)
				}
				reqLog.Warn("openai.images.output_audit_blocked",
					zap.Int64("account_id", account.ID),
					zap.Int("image_count", resultImageCount(result)),
					zap.String("policy_rule", decision.PolicyRule),
					zap.String("action", decision.Action),
					zap.Error(err),
				)
			} else if result != nil && result.ImageCount > 0 {
				reqLog.Warn("openai.images.forward_partial_error_with_image_result",
					zap.Int64("account_id", account.ID),
					zap.Int("image_count", result.ImageCount),
					zap.Error(err),
				)
			} else {
				var failoverErr *service.UpstreamFailoverError
				if errors.As(err, &failoverErr) {
					h.markOpenAIUsageBillingAttemptFailed(c.Request.Context(), usageBillingAdmission, reqLog)
					h.gatewayService.ReportOpenAIAccountScheduleResult(account.ID, false, nil)
					if failoverErr.RetryableOnSameAccount {
						retryLimit := account.GetPoolModeRetryCount()
						if sameAccountRetryCount[account.ID] < retryLimit {
							sameAccountRetryCount[account.ID]++
							reqLog.Warn("openai.images.pool_mode_same_account_retry",
								zap.Int64("account_id", account.ID),
								zap.Int("upstream_status", failoverErr.StatusCode),
								zap.Int("retry_limit", retryLimit),
								zap.Int("retry_count", sameAccountRetryCount[account.ID]),
							)
							select {
							case <-c.Request.Context().Done():
								return
							case <-time.After(sameAccountRetryDelay):
							}
							continue
						}
					}
					h.gatewayService.RecordOpenAIAccountSwitch()
					failedAccountIDs[account.ID] = struct{}{}
					lastFailoverErr = failoverErr
					if switchCount >= maxAccountSwitches {
						h.handleFailoverExhausted(c, failoverErr, streamStarted)
						return
					}
					switchCount++
					reqLog.Warn("openai.images.upstream_failover_switching",
						zap.Int64("account_id", account.ID),
						zap.Int("upstream_status", failoverErr.StatusCode),
						zap.Int("switch_count", switchCount),
						zap.Int("max_switches", maxAccountSwitches),
					)
					continue
				}
				h.gatewayService.ReportOpenAIAccountScheduleResult(account.ID, false, nil)
				wroteFallback := h.ensureForwardErrorResponse(c, streamStarted)
				fields := []zap.Field{
					zap.Int64("account_id", account.ID),
					zap.Bool("fallback_error_response_written", wroteFallback),
					zap.Error(err),
				}
				if shouldLogOpenAIForwardFailureAsWarn(c, wroteFallback) {
					reqLog.Warn("openai.images.forward_failed", fields...)
					return
				}
				reqLog.Error("openai.images.forward_failed", fields...)
				return
			}
		}
		h.gatewayService.MarkOpenAIUsageBillingAccepted(usageBillingAdmission)
		if result != nil {
			if account.Type == service.AccountTypeOAuth {
				h.gatewayService.UpdateCodexUsageSnapshotFromHeaders(c.Request.Context(), account.ID, result.ResponseHeaders)
			}
			h.gatewayService.ReportOpenAIAccountScheduleResult(account.ID, true, result.FirstTokenMs)
		} else {
			h.gatewayService.ReportOpenAIAccountScheduleResult(account.ID, true, nil)
		}

		userAgent := c.GetHeader("User-Agent")
		clientIP := ip.GetClientIP(c)
		requestPayloadHash := service.HashUsageRequestPayload(body)
		if parsed.Multipart {
			requestPayloadHash = service.HashUsageRequestPayload([]byte(parsed.StickySessionSeed()))
		}
		inboundEndpoint := GetInboundEndpoint(c)
		upstreamEndpoint := GetUpstreamEndpoint(c, account.Platform)

		upstreamModel := ""
		if result != nil {
			upstreamModel = result.UpstreamModel
		}
		h.submitOpenAIUsageRecordTask(c.Request.Context(), result, func(ctx context.Context) {
			if err := h.gatewayService.RecordUsage(ctx, &service.OpenAIRecordUsageInput{
				Result:             result,
				APIKey:             apiKey,
				User:               apiKey.User,
				Account:            account,
				Subscription:       subscription,
				InboundEndpoint:    inboundEndpoint,
				UpstreamEndpoint:   upstreamEndpoint,
				UserAgent:          userAgent,
				IPAddress:          clientIP,
				RequestPayloadHash: requestPayloadHash,
				APIKeyService:      h.apiKeyService,
				ChannelUsageFields: channelMapping.ToUsageFields(parsed.Model, upstreamModel),
			}); err != nil {
				h.markOpenAIUsageBillingResultOrphaned(ctx, result, reqLog)
				logger.L().With(
					zap.String("component", "handler.openai_gateway.images"),
					zap.Int64("user_id", subject.UserID),
					zap.Int64("api_key_id", apiKey.ID),
					zap.Any("group_id", apiKey.GroupID),
					zap.String("model", parsed.Model),
					zap.Int64("account_id", account.ID),
				).Error("openai.images.record_usage_failed", zap.Error(err))
			}
		})

		reqLog.Debug("openai.images.request_completed",
			zap.Int64("account_id", account.ID),
			zap.Int("switch_count", switchCount),
		)
		return
	}
}

func isMultipartImagesContentType(contentType string) bool {
	return strings.HasPrefix(strings.ToLower(strings.TrimSpace(contentType)), "multipart/form-data")
}

func (h *OpenAIGatewayHandler) recordImageSafetyPolicyBlock(c *gin.Context, apiKey *service.APIKey, subject middleware2.AuthSubject, parsed *service.OpenAIImagesRequest, safetyIdentifier string, policyRule string, message string, statusCode int) {
	if h == nil || h.contentModerationService == nil || c == nil || parsed == nil {
		return
	}
	input := buildContentModerationInput(c, apiKey, subject, service.ContentModerationProtocolOpenAIImages, parsed.Model, parsed.ModerationBody())
	input.Stage = service.ContentModerationStageInput
	input.PolicyRule = strings.TrimSpace(policyRule)
	input.SafetyIdentifier = strings.TrimSpace(safetyIdentifier)
	h.contentModerationService.RecordPolicyBlock(c.Request.Context(), input, message, statusCode)
}

func (h *OpenAIGatewayHandler) openAIImageOutputAuditor(c *gin.Context, apiKey *service.APIKey, subject middleware2.AuthSubject, parsed *service.OpenAIImagesRequest, safetyIdentifier string) service.OpenAIImageOutputAuditor {
	return service.OpenAIImageOutputAuditorFunc(func(ctx context.Context, req service.OpenAIImageOutputAuditRequest) (*service.ContentModerationDecision, error) {
		if h == nil || h.contentModerationService == nil {
			return &service.ContentModerationDecision{
				Allowed:    false,
				Blocked:    true,
				Message:    "Image safety review is temporarily unavailable",
				StatusCode: http.StatusServiceUnavailable,
				Action:     service.ContentModerationActionError,
				PolicyRule: "moderation_service_unavailable",
			}, nil
		}
		model := ""
		if parsed != nil {
			model = parsed.Model
		}
		input := buildContentModerationInput(c, apiKey, subject, service.ContentModerationProtocolOpenAIImages, model, req.ModerationBody)
		input.Stage = service.ContentModerationStageOutput
		input.FailClosed = true
		input.PolicyRule = strings.TrimSpace(req.PolicyRule)
		input.SafetyIdentifier = strings.TrimSpace(safetyIdentifier)
		input.UpstreamRequestID = strings.TrimSpace(req.UpstreamRequestID)
		input.OutputHashes = append([]string(nil), req.OutputHashes...)
		if strings.TrimSpace(req.UnavailableReason) != "" {
			input.FailClosedStatusCode = http.StatusServiceUnavailable
			input.FailClosedMessage = "Image safety review is temporarily unavailable"
			input.FailClosedError = req.UnavailableReason
			if input.PolicyRule == "" {
				input.PolicyRule = "output_audit_unavailable"
			}
		}
		return h.contentModerationService.Check(ctx, input)
	})
}

func resultImageCount(result *service.OpenAIForwardResult) int {
	if result == nil {
		return 0
	}
	return result.ImageCount
}
