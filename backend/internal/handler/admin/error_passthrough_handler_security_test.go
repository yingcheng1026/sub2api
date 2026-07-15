package admin

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/model"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type errorPassthroughSecurityRepo struct {
	created *model.ErrorPassthroughRule
}

func (r *errorPassthroughSecurityRepo) List(context.Context) ([]*model.ErrorPassthroughRule, error) {
	return nil, nil
}

func (r *errorPassthroughSecurityRepo) GetByID(context.Context, int64) (*model.ErrorPassthroughRule, error) {
	return nil, nil
}

func (r *errorPassthroughSecurityRepo) Create(_ context.Context, rule *model.ErrorPassthroughRule) (*model.ErrorPassthroughRule, error) {
	r.created = rule
	return rule, nil
}

func (r *errorPassthroughSecurityRepo) Update(_ context.Context, rule *model.ErrorPassthroughRule) (*model.ErrorPassthroughRule, error) {
	return rule, nil
}

func (r *errorPassthroughSecurityRepo) Delete(context.Context, int64) error { return nil }

func TestErrorPassthroughCreateDefaultsToSafeCustomMessage(t *testing.T) {
	repo := &errorPassthroughSecurityRepo{}
	handler := NewErrorPassthroughHandler(service.NewErrorPassthroughService(repo, nil))
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/", bytes.NewBufferString(`{
		"name":"safe default",
		"error_codes":[500]
	}`))
	c.Request.Header.Set("Content-Type", "application/json")

	handler.Create(c)

	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	require.NotNil(t, repo.created)
	assert.False(t, repo.created.PassthroughBody)
	require.NotNil(t, repo.created.CustomMessage)
	assert.Equal(t, "Upstream request failed", *repo.created.CustomMessage)
}

func TestErrorPassthroughCreateRejectsRawBodyPassthrough(t *testing.T) {
	repo := &errorPassthroughSecurityRepo{}
	handler := NewErrorPassthroughHandler(service.NewErrorPassthroughService(repo, nil))
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/", bytes.NewBufferString(`{
		"name":"unsafe raw body",
		"error_codes":[500],
		"passthrough_body":true
	}`))
	c.Request.Header.Set("Content-Type", "application/json")

	handler.Create(c)

	assert.Equal(t, http.StatusBadRequest, rec.Code)
	assert.Nil(t, repo.created)
}
