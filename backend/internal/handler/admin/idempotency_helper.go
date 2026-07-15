package admin

import (
	"context"
	"strconv"
	"time"

	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
	"github.com/Wei-Shaw/sub2api/internal/pkg/logger"
	"github.com/Wei-Shaw/sub2api/internal/pkg/response"
	middleware2 "github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"

	"github.com/gin-gonic/gin"
)

type idempotencyStoreUnavailableMode int

const (
	idempotencyStoreUnavailableFailClose idempotencyStoreUnavailableMode = iota
	idempotencyStoreUnavailableFailOpen
)

func executeAdminIdempotent(
	c *gin.Context,
	scope string,
	payload any,
	ttl time.Duration,
	execute func(context.Context) (any, error),
) (*service.IdempotencyExecuteResult, error) {
	coordinator := service.DefaultIdempotencyCoordinator()
	if coordinator == nil {
		data, err := execute(c.Request.Context())
		if err != nil {
			return nil, err
		}
		return &service.IdempotencyExecuteResult{Data: data}, nil
	}
	return executeAdminIdempotentWithCoordinator(c.Request.Context(), c, coordinator, scope, payload, ttl, c.GetHeader("Idempotency-Key"), false, execute)
}

func executeAdminIdempotentWithCoordinator(
	ctx context.Context,
	c *gin.Context,
	coordinator *service.IdempotencyCoordinator,
	scope string,
	payload any,
	ttl time.Duration,
	idempotencyKey string,
	persistent bool,
	execute func(context.Context) (any, error),
) (*service.IdempotencyExecuteResult, error) {

	actorScope := "admin:0"
	if subject, ok := middleware2.GetAuthSubjectFromContext(c); ok {
		actorScope = "admin:" + strconv.FormatInt(subject.UserID, 10)
	}

	return coordinator.Execute(ctx, service.IdempotencyExecuteOptions{
		Scope:          scope,
		ActorScope:     actorScope,
		Method:         c.Request.Method,
		Route:          c.FullPath(),
		IdempotencyKey: idempotencyKey,
		Payload:        payload,
		RequireKey:     true,
		TTL:            ttl,
		Persistent:     persistent,
	}, execute)
}

func executeAdminIdempotentJSON(
	c *gin.Context,
	scope string,
	payload any,
	ttl time.Duration,
	execute func(context.Context) (any, error),
) {
	executeAdminIdempotentJSONWithMode(c, scope, payload, ttl, idempotencyStoreUnavailableFailClose, execute)
}

// executeAdminStrictIdempotentJSON is the fail-closed variant for financial
// writes. It does not honor the global observe-only bypass: callers must send
// a valid Idempotency-Key and the coordinator must be available before any
// business side effect can run.
func executeAdminStrictIdempotentJSON(
	c *gin.Context,
	scope string,
	payload any,
	ttl time.Duration,
	runInTransaction func(context.Context, func(context.Context) error) error,
	execute func(context.Context) (any, error),
) {
	executeAdminStrictIdempotentJSONWithPostCommit(c, scope, payload, ttl, runInTransaction, nil, execute)
}

// executeAdminStrictIdempotentJSONWithPostCommit runs postCommit only after
// the business mutation and its persistent idempotency result have committed.
// A post-commit cache failure must not turn an already committed financial
// mutation into an API failure, so it is logged and the committed response is
// still returned.
func executeAdminStrictIdempotentJSONWithPostCommit(
	c *gin.Context,
	scope string,
	payload any,
	ttl time.Duration,
	runInTransaction func(context.Context, func(context.Context) error) error,
	postCommit func(context.Context) error,
	execute func(context.Context) (any, error),
) {
	key, err := service.NormalizeIdempotencyKey(c.GetHeader("Idempotency-Key"))
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	if key == "" {
		response.ErrorFrom(c, service.ErrIdempotencyKeyRequired)
		return
	}
	coordinator := service.DefaultIdempotencyCoordinator()
	if coordinator == nil {
		service.RecordIdempotencyStoreUnavailable(c.FullPath(), scope, "coordinator_nil")
		response.ErrorFrom(c, service.ErrIdempotencyStoreUnavail)
		return
	}
	if runInTransaction == nil {
		response.ErrorFrom(c, infraerrors.ServiceUnavailable("ASSIGNMENT_TRANSACTION_UNAVAILABLE", "assignment transaction is unavailable"))
		return
	}
	var result *service.IdempotencyExecuteResult
	err = runInTransaction(c.Request.Context(), func(txCtx context.Context) error {
		var executeErr error
		result, executeErr = executeAdminIdempotentWithCoordinator(txCtx, c, coordinator, scope, payload, ttl, key, true, execute)
		return executeErr
	})
	if err == nil && postCommit != nil {
		postCommitCtx, cancel := context.WithTimeout(context.WithoutCancel(c.Request.Context()), 5*time.Second)
		if postCommitErr := postCommit(postCommitCtx); postCommitErr != nil {
			logger.LegacyPrintf("handler.idempotency", "[Idempotency] post-commit hook failed: method=%s route=%s scope=%s error=%v", c.Request.Method, c.FullPath(), scope, postCommitErr)
		}
		cancel()
	}
	writeAdminIdempotentJSONResult(c, scope, idempotencyStoreUnavailableFailClose, execute, result, err)
}

// executeAdminStrictIdempotentJSONNonTransactional protects independently
// atomic financial operations and partial batches that cannot share one outer
// transaction. A crash after the business commit leaves the persistent claim
// in manual-recovery state instead of ever executing it again.
func executeAdminStrictIdempotentJSONNonTransactional(
	c *gin.Context,
	scope string,
	payload any,
	ttl time.Duration,
	execute func(context.Context) (any, error),
) {
	executeAdminStrictIdempotentJSONNonTransactionalWithKey(
		c,
		scope,
		payload,
		ttl,
		c.GetHeader("Idempotency-Key"),
		execute,
	)
}

func executeAdminStrictIdempotentJSONNonTransactionalWithKey(
	c *gin.Context,
	scope string,
	payload any,
	ttl time.Duration,
	rawKey string,
	execute func(context.Context) (any, error),
) {
	key, err := service.NormalizeIdempotencyKey(rawKey)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	if key == "" {
		response.ErrorFrom(c, service.ErrIdempotencyKeyRequired)
		return
	}
	coordinator := service.DefaultIdempotencyCoordinator()
	if coordinator == nil {
		service.RecordIdempotencyStoreUnavailable(c.FullPath(), scope, "coordinator_nil")
		response.ErrorFrom(c, service.ErrIdempotencyStoreUnavail)
		return
	}
	result, err := executeAdminIdempotentWithCoordinator(
		c.Request.Context(),
		c,
		coordinator,
		scope,
		payload,
		ttl,
		key,
		true,
		execute,
	)
	writeAdminIdempotentJSONResult(c, scope, idempotencyStoreUnavailableFailClose, execute, result, err)
}

func executeAdminIdempotentJSONFailOpenOnStoreUnavailable(
	c *gin.Context,
	scope string,
	payload any,
	ttl time.Duration,
	execute func(context.Context) (any, error),
) {
	executeAdminIdempotentJSONWithMode(c, scope, payload, ttl, idempotencyStoreUnavailableFailOpen, execute)
}

func executeAdminIdempotentJSONWithMode(
	c *gin.Context,
	scope string,
	payload any,
	ttl time.Duration,
	mode idempotencyStoreUnavailableMode,
	execute func(context.Context) (any, error),
) {
	result, err := executeAdminIdempotent(c, scope, payload, ttl, execute)
	writeAdminIdempotentJSONResult(c, scope, mode, execute, result, err)
}

func writeAdminIdempotentJSONResult(
	c *gin.Context,
	scope string,
	mode idempotencyStoreUnavailableMode,
	execute func(context.Context) (any, error),
	result *service.IdempotencyExecuteResult,
	err error,
) {
	if err != nil {
		if infraerrors.Code(err) == infraerrors.Code(service.ErrIdempotencyStoreUnavail) {
			strategy := "fail_close"
			if mode == idempotencyStoreUnavailableFailOpen {
				strategy = "fail_open"
			}
			service.RecordIdempotencyStoreUnavailable(c.FullPath(), scope, "handler_"+strategy)
			logger.LegacyPrintf("handler.idempotency", "[Idempotency] store unavailable: method=%s route=%s scope=%s strategy=%s", c.Request.Method, c.FullPath(), scope, strategy)
			if mode == idempotencyStoreUnavailableFailOpen {
				data, fallbackErr := execute(c.Request.Context())
				if fallbackErr != nil {
					response.ErrorFrom(c, fallbackErr)
					return
				}
				c.Header("X-Idempotency-Degraded", "store-unavailable")
				response.Success(c, data)
				return
			}
		}
		if retryAfter := service.RetryAfterSecondsFromError(err); retryAfter > 0 {
			c.Header("Retry-After", strconv.Itoa(retryAfter))
		}
		response.ErrorFrom(c, err)
		return
	}
	if result != nil && result.Replayed {
		c.Header("X-Idempotency-Replayed", "true")
	}
	response.Success(c, result.Data)
}
