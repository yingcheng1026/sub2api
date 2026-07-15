package server

import (
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestProvideHTTPServerKeepsGlobalBodyLimitWhenH2CEnabled(t *testing.T) {
	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.POST("/upload", func(c *gin.Context) {
		_, err := io.ReadAll(c.Request.Body)
		var maxBytesError *http.MaxBytesError
		if errors.As(err, &maxBytesError) {
			c.Status(http.StatusRequestEntityTooLarge)
			return
		}
		require.NoError(t, err)
		c.Status(http.StatusNoContent)
	})

	server := ProvideHTTPServer(&config.Config{
		Server: config.ServerConfig{
			MaxRequestBodySize: 8,
			H2C: config.H2CConfig{
				Enabled:                      true,
				MaxConcurrentStreams:         10,
				IdleTimeout:                  10,
				MaxReadFrameSize:             1 << 14,
				MaxUploadBufferPerConnection: 1 << 16,
				MaxUploadBufferPerStream:     1 << 15,
			},
		},
	}, router)

	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "/upload", strings.NewReader("123456789"))
	server.Handler.ServeHTTP(recorder, request)
	require.Equal(t, http.StatusRequestEntityTooLarge, recorder.Code)
}
