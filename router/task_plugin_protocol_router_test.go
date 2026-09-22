package router

import (
	"bytes"
	"encoding/base64"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sort"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/integration/nova"
	"github.com/QuantumNous/new-api/model"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestHostProtocolRegistryDrivesProtocolRoutesOnce(t *testing.T) {
	engine := gin.New()
	SetTaskPluginProtocolRouter(engine)

	expected := []string{
		"POST /v1/responses",
		"GET /v1/responses/:response_id",
		"POST /v1/videos",
		"GET /v1/videos/:task_id",
		"GET /v1/videos/:task_id/content",
		"HEAD /v1/videos/:task_id/content",
		"POST /v1/images/generations",
		"POST /v1/images/edits",
	}
	actual := make([]string, 0, len(engine.Routes()))
	for _, route := range engine.Routes() {
		actual = append(actual, fmt.Sprintf("%s %s", route.Method, route.Path))
	}
	sort.Strings(expected)
	sort.Strings(actual)
	assert.Equal(t, expected, actual)
}

func TestTaskSubmissionRoutesRequireNovaAttribution(t *testing.T) {
	setupRelayRouterTestDB(t)
	secret := base64.RawURLEncoding.EncodeToString(bytes.Repeat([]byte{0x5a}, 32))
	t.Setenv("NOVA_INTEGRATION_ENABLED", "true")
	t.Setenv("NOVA_HMAC_KEYS", "current:"+secret)
	require.NoError(t, nova.Initialize(model.DB))
	t.Cleanup(nova.Close)

	user := model.User{
		Username: "nova-task-route-user",
		Status:   common.UserStatusEnabled,
		Group:    "default",
		Quota:    100,
	}
	require.NoError(t, model.DB.Create(&user).Error)
	token := model.Token{
		UserId:      user.Id,
		Key:         "novataskroutekey",
		Status:      common.TokenStatusEnabled,
		ExpiredTime: -1,
		RemainQuota: 100,
	}
	require.NoError(t, model.DB.Create(&token).Error)
	require.NoError(t, model.DB.Create(&nova.Tenant{UserID: user.Id}).Error)

	engine := gin.New()
	SetTaskPluginProtocolRouter(engine)
	SetVideoRouter(engine)

	for _, route := range []string{
		"/v1/responses",
		"/v1/images/generations",
		"/v1/images/edits",
		"/v1/videos",
		"/v1/video/generations",
		"/v1/videos/video-123/remix",
	} {
		t.Run(route, func(t *testing.T) {
			request := httptest.NewRequest(http.MethodPost, route, bytes.NewBufferString(`{"model":"unconfigured"}`))
			request.Header.Set("Authorization", "Bearer sk-"+token.Key)
			request.Header.Set("Content-Type", "application/json")
			response := httptest.NewRecorder()

			engine.ServeHTTP(response, request)

			require.Equal(t, http.StatusUnauthorized, response.Code, response.Body.String())
			assert.Contains(t, response.Body.String(), "nova_authentication_failed")
		})
	}
}
