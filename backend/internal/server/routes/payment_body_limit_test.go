package routes

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestLimitPaymentJSONBodyRejectsOversizedPayload(t *testing.T) {
	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.POST("/payment", limitPaymentJSONBody(8), func(c *gin.Context) {
		_, err := io.ReadAll(c.Request.Body)
		if err != nil {
			c.Status(http.StatusRequestEntityTooLarge)
			return
		}
		c.Status(http.StatusNoContent)
	})

	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "/payment", strings.NewReader("123456789"))
	router.ServeHTTP(recorder, request)
	require.Equal(t, http.StatusRequestEntityTooLarge, recorder.Code)
}

func TestPaymentStatusRateLimitSupportsConcurrentNormalPolling(t *testing.T) {
	const pollsPerClientPerMinute = 20
	const concurrentClientsBehindNAT = 2
	require.GreaterOrEqual(t, paymentStatusRequestsPerMinute, pollsPerClientPerMinute*concurrentClientsBehindNAT)
}
