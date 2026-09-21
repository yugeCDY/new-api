package nova

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/service"
	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"github.com/google/uuid"
	amqp "github.com/rabbitmq/amqp091-go"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/mysql"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

func TestNovaMigrationIsIdempotent(t *testing.T) {
	db := openTestDatabase(t)
	testNovaMigrationContract(t, db)
}

func TestNovaMigrationIsIdempotentMySQL(t *testing.T) {
	dsn := strings.TrimSpace(os.Getenv("TEST_MYSQL_DSN"))
	if dsn == "" {
		t.Skip("TEST_MYSQL_DSN is not configured")
	}
	db, err := gorm.Open(mysql.Open(dsn), &gorm.Config{})
	require.NoError(t, err)
	testNovaMigrationContract(t, db)
}

func TestNovaMigrationIsIdempotentPostgreSQL(t *testing.T) {
	dsn := strings.TrimSpace(os.Getenv("TEST_POSTGRES_DSN"))
	if dsn == "" {
		t.Skip("TEST_POSTGRES_DSN is not configured")
	}
	db, err := gorm.Open(postgres.New(postgres.Config{DSN: dsn, PreferSimpleProtocol: true}), &gorm.Config{})
	require.NoError(t, err)
	testNovaMigrationContract(t, db)
}

func TestNovaUpgradeFromReleasedSchema(t *testing.T) {
	dsn := strings.TrimSpace(os.Getenv("TEST_NOVA_UPGRADE_DSN"))
	if dsn == "" {
		t.Skip("TEST_NOVA_UPGRADE_DSN is not configured")
	}
	t.Setenv("SQL_DSN", dsn)
	previousMaster := common.IsMasterNode
	common.IsMasterNode = true
	t.Cleanup(func() { common.IsMasterNode = previousMaster })
	require.NoError(t, model.InitDB())
	sqlDB, err := model.DB.DB()
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, sqlDB.Close()) })
	require.NoError(t, migrate(model.DB))
	require.NoError(t, migrate(model.DB))

	var fixture model.User
	require.NoError(t, model.DB.Where("username = ?", "nova-upgrade-fixture").First(&fixture).Error)
	assert.Equal(t, 12345, fixture.Quota)
	assert.Equal(t, common.UserStatusDisabled, fixture.Status)
}

func TestNovaUpgradePreservesExistingUserFixtureMySQL(t *testing.T) {
	dsn := strings.TrimSpace(os.Getenv("TEST_MYSQL_DSN"))
	if dsn == "" {
		t.Skip("TEST_MYSQL_DSN is not configured")
	}
	db, err := gorm.Open(mysql.Open(dsn), &gorm.Config{})
	require.NoError(t, err)
	assertNovaUpgradePreservesFixture(t, db)
}

func TestNovaUpgradePreservesExistingUserFixturePostgreSQL(t *testing.T) {
	dsn := strings.TrimSpace(os.Getenv("TEST_POSTGRES_DSN"))
	if dsn == "" {
		t.Skip("TEST_POSTGRES_DSN is not configured")
	}
	db, err := gorm.Open(postgres.New(postgres.Config{DSN: dsn, PreferSimpleProtocol: true}), &gorm.Config{})
	require.NoError(t, err)
	assertNovaUpgradePreservesFixture(t, db)
}

func assertNovaUpgradePreservesFixture(t *testing.T, db *gorm.DB) {
	t.Helper()
	for _, table := range []any{&QuotaOperation{}, &Attribution{}, &ReplayNonce{}, &IdempotencyRecord{}, &Outbox{}, &UsageEvent{}, &Tenant{}} {
		_ = db.Migrator().DropTable(table)
	}
	require.NoError(t, db.AutoMigrate(&model.User{}))
	_ = db.Where("username = ?", "nova-upgrade-fixture").Delete(&model.User{}).Error
	fixture := model.User{
		Username: "nova-upgrade-fixture", Password: "unused", Status: common.UserStatusDisabled,
		Quota: 12345, Group: "default", AffCode: "nova-upgrade-aff",
	}
	require.NoError(t, db.Create(&fixture).Error)
	require.NoError(t, migrate(db))
	require.NoError(t, migrate(db))

	var got model.User
	require.NoError(t, db.Where("username = ?", "nova-upgrade-fixture").First(&got).Error)
	assert.Equal(t, 12345, got.Quota)
	assert.Equal(t, common.UserStatusDisabled, got.Status)
	assert.True(t, db.Migrator().HasTable("nova_tenants"))
	assert.True(t, db.Migrator().HasTable("nova_outbox"))
	assert.True(t, db.Migrator().HasTable("nova_quota_operations"))
}

func testNovaMigrationContract(t *testing.T, db *gorm.DB) {
	t.Helper()
	for _, table := range []any{&QuotaOperation{}, &Attribution{}, &ReplayNonce{}, &IdempotencyRecord{}, &Outbox{}, &UsageEvent{}, &Tenant{}} {
		_ = db.Migrator().DropTable(table)
	}
	t.Cleanup(func() {
		for _, table := range []any{&QuotaOperation{}, &Attribution{}, &ReplayNonce{}, &IdempotencyRecord{}, &Outbox{}, &UsageEvent{}, &Tenant{}} {
			_ = db.Migrator().DropTable(table)
		}
	})
	require.NoError(t, migrate(db))
	require.NoError(t, migrate(db))

	for _, table := range []string{
		"nova_tenants",
		"nova_usage_events",
		"nova_outbox",
		"nova_idempotency",
		"nova_replay_nonces",
		"nova_attributions",
		"nova_quota_operations",
	} {
		assert.True(t, db.Migrator().HasTable(table), table)
	}

	now := time.Now().Unix()
	tenant := Tenant{UserID: 1001, CreatedAt: now, UpdatedAt: now}
	require.NoError(t, db.Create(&tenant).Error)
	duplicateTenant := Tenant{UserID: 1001, CreatedAt: now, UpdatedAt: now}
	assert.Error(t, db.Create(&duplicateTenant).Error)

	nonce := ReplayNonce{KeyID: "current", NonceHash: strings.Repeat("a", 64), RequestTimestamp: now, ExpiresAt: now + 600, CreatedAt: now}
	require.NoError(t, db.Create(&nonce).Error)
	assert.Error(t, db.Create(&ReplayNonce{KeyID: nonce.KeyID, NonceHash: nonce.NonceHash, RequestTimestamp: now, ExpiresAt: now + 600, CreatedAt: now}).Error)

	usage := UsageEvent{EventID: "matrix-event", SourceType: "relay", SourceKey: "matrix-request", TenantID: tenant.ID, TenantKey: "matrix-tenant", CreatedAt: now}
	require.NoError(t, db.Create(&usage).Error)
	duplicateUsage := usage
	duplicateUsage.ID = 0
	created := db.Clauses(clause.OnConflict{DoNothing: true}).Create(&duplicateUsage)
	require.NoError(t, created.Error)
	assert.EqualValues(t, 0, created.RowsAffected)
	require.NoError(t, reconcileMissingOutbox(db, Config{Exchange: "nova.events", RoutingKey: "nova.usage.reported", BatchSize: 10}, time.Now()))
	var reconciledOutboxCount int64
	require.NoError(t, db.Model(&Outbox{}).Where("event_id = ?", usage.EventID).Count(&reconciledOutboxCount).Error)
	assert.EqualValues(t, 1, reconciledOutboxCount)

	quotaOp := QuotaOperation{
		TenantKey: "matrix-tenant", TargetType: quotaTargetTenant, TargetRef: "",
		OrderNo: "op-unique-1", Delta: 10, Reason: "test", ResultQuota: 10, CreatedAt: now,
	}
	require.NoError(t, db.Create(&quotaOp).Error)
	assert.Error(t, db.Create(&QuotaOperation{
		TenantKey: "matrix-tenant", TargetType: quotaTargetTenant, TargetRef: "",
		OrderNo: "op-unique-1", Delta: 20, Reason: "dup", ResultQuota: 30, CreatedAt: now,
	}).Error)

	if db.Migrator().HasTable(&model.User{}) {
		var fixture model.User
		err := db.Where("username = ?", "nova-upgrade-fixture").First(&fixture).Error
		if err == nil {
			assert.Equal(t, 12345, fixture.Quota)
			assert.Equal(t, common.UserStatusDisabled, fixture.Status)
		} else {
			assert.ErrorIs(t, err, gorm.ErrRecordNotFound)
		}
	}
}

func TestLoadConfigRequiresStrongRotatableKeys(t *testing.T) {
	secret := bytes.Repeat([]byte{0x42}, 32)
	t.Setenv("NOVA_INTEGRATION_ENABLED", "true")
	t.Setenv("NOVA_HMAC_KEYS", "current:"+base64.RawURLEncoding.EncodeToString(secret)+",previous:"+base64.RawURLEncoding.EncodeToString(bytes.Repeat([]byte{0x24}, 32)))

	config, err := loadConfig()
	require.NoError(t, err)
	assert.True(t, config.Enabled)
	assert.Equal(t, "current", config.CurrentKeyID)
	assert.Len(t, config.Keys, 2)
	assert.Equal(t, defaultMaxSkew, config.MaxSkew)
	assert.Equal(t, defaultNonceTTL, config.NonceTTL)
	t.Setenv("NOVA_OUTBOX_BATCH_SIZE", "1001")
	_, err = loadConfig()
	assert.EqualError(t, err, "NOVA_OUTBOX_BATCH_SIZE must be between 1 and 1000")
}

func TestRegisterRoutesExposesContractOnlyWhenEnabled(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := openTestDatabase(t)
	require.NoError(t, migrate(db))
	setTestRuntime(t, db, bytes.Repeat([]byte{0x43}, 32))
	router := gin.New()
	RegisterRoutes(router.Group("/api"))
	routes := router.Routes()
	require.Len(t, routes, 15)
	expected := map[string]bool{
		"GET /api/novapay/health": true, "POST /api/novapay/tenant": true,
		"GET /api/novapay/tenant/:tenant_key": true, "PUT /api/novapay/tenant/:tenant_key": true,
		"DELETE /api/novapay/tenant/:tenant_key": true, "GET /api/novapay/tenants": true,
		"POST /api/novapay/tenant/:tenant_key/quota": true, "POST /api/novapay/tenant/:tenant_key/disable": true,
		"POST /api/novapay/tenant/:tenant_key/enable": true, "POST /api/novapay/tenant/:tenant_key/token/rotate": true,
		"GET /api/novapay/tenant/:tenant_key/tokens": true, "POST /api/novapay/tenant/:tenant_key/tokens/:token_name/quota": true,
		"DELETE /api/novapay/tenant/:tenant_key/tokens/:token_name": true, "GET /api/novapay/tenant/:tenant_key/logs": true,
		"GET /api/novapay/models": true,
	}
	for _, route := range routes {
		assert.True(t, expected[route.Method+" "+route.Path], route.Method+" "+route.Path)
	}

	runtimeState.Lock()
	runtimeState.config.Enabled = false
	runtimeState.Unlock()
	disabledRouter := gin.New()
	RegisterRoutes(disabledRouter.Group("/api"))
	assert.Empty(t, disabledRouter.Routes())
}

func TestHealthReportsMiddlewareStatus(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := openTestDatabase(t)
	require.NoError(t, migrate(db))
	secret := bytes.Repeat([]byte{0x44}, 32)
	setTestRuntime(t, db, secret)
	previousRedisEnabled := common.RedisEnabled
	common.RedisEnabled = false
	t.Cleanup(func() { common.RedisEnabled = previousRedisEnabled })

	now := time.Now().Unix()
	require.NoError(t, db.Create(&Outbox{
		EventID: "health-pending-1", ExchangeName: "nova.events", RoutingKey: "nova.usage.reported",
		Payload: "{}", Status: "pending", NextAttemptAt: now, CreatedAt: now, UpdatedAt: now,
	}).Error)
	require.NoError(t, db.Create(&Outbox{
		EventID: "health-dead-1", ExchangeName: "nova.events", RoutingKey: "nova.usage.reported",
		Payload: "{}", Status: "dead", NextAttemptAt: now, CreatedAt: now, UpdatedAt: now,
	}).Error)

	router := gin.New()
	RegisterRoutes(router.Group("/api"))
	request := signedRequest(t, secret, http.MethodGet, "/api/novapay/health", strconvUnix(time.Now()), "healthmiddlewarestatus1", nil, nil)
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	require.Equal(t, http.StatusServiceUnavailable, response.Code)

	var body map[string]any
	require.NoError(t, common.Unmarshal(response.Body.Bytes(), &body))
	assert.Equal(t, false, body["success"])
	data := body["data"].(map[string]any)
	assert.Equal(t, "degraded", data["status"])
	assert.Equal(t, true, data["database"].(map[string]any)["connected"])
	redis := data["redis"].(map[string]any)
	assert.Equal(t, false, redis["enabled"])
	assert.Equal(t, false, redis["connected"])
	mq := data["mq"].(map[string]any)
	assert.Equal(t, false, mq["connected"])
	assert.EqualValues(t, 1, mq["outbox_pending"])
	assert.EqualValues(t, 1, mq["outbox_dead"])
}

func TestModelCatalogReturnsAvailabilityAndPricing(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := openTestDatabase(t)
	require.NoError(t, db.AutoMigrate(&model.Channel{}, &model.Ability{}))
	require.NoError(t, migrate(db))
	secret := bytes.Repeat([]byte{0x45}, 32)
	setTestRuntime(t, db, secret)
	previousDB := model.DB
	model.DB = db
	model.InvalidatePricingCache()
	t.Cleanup(func() {
		model.DB = previousDB
		model.InvalidatePricingCache()
	})

	require.NoError(t, db.Create(&model.Ability{Group: "default", Model: "demo-b", ChannelId: 4, Enabled: true}).Error)
	require.NoError(t, db.Create(&model.Ability{Group: "default", Model: "demo-a", ChannelId: 1, Enabled: true}).Error)
	require.NoError(t, db.Create(&model.Ability{Group: "vip", Model: "demo-a", ChannelId: 1, Enabled: true}).Error)
	require.NoError(t, db.Create(&model.Ability{Group: "default", Model: "demo-a", ChannelId: 2, Enabled: true}).Error)
	require.NoError(t, db.Create(&model.Ability{Group: "default", Model: "demo-a", ChannelId: 3, Enabled: false}).Error)

	router := gin.New()
	RegisterRoutes(router.Group("/api"))
	request := signedRequest(t, secret, http.MethodGet, "/api/novapay/models", strconvUnix(time.Now()), "modelcatalognoncevalue1", nil, nil)
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	require.Equal(t, http.StatusOK, response.Code, response.Body.String())

	var body map[string]any
	require.NoError(t, common.Unmarshal(response.Body.Bytes(), &body))
	assert.Equal(t, true, body["success"])
	items := body["data"].(map[string]any)["items"].([]any)
	require.Len(t, items, 2)
	first := items[0].(map[string]any)
	second := items[1].(map[string]any)
	assert.Equal(t, "demo-a", first["model_name"])
	assert.EqualValues(t, 2, first["channel_count"])
	assert.Equal(t, true, first["enabled"])
	assert.Equal(t, float64(0), first["quota_type"])
	groups := first["enable_groups"].([]any)
	assert.ElementsMatch(t, []any{"default", "vip"}, groups)
	assert.Equal(t, "demo-b", second["model_name"])
	assert.EqualValues(t, 1, second["channel_count"])
	assert.Equal(t, true, second["enabled"])
}

func TestListTenantsKeepsOrphanWhenUserIsGone(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := openTestDatabase(t)
	require.NoError(t, db.AutoMigrate(&model.User{}))
	require.NoError(t, migrate(db))
	secret := bytes.Repeat([]byte{0x46}, 32)
	setTestRuntime(t, db, secret)

	now := time.Now().Unix()
	user := model.User{Username: "nova-list-user", Password: "unused", DisplayName: "ok", Status: common.UserStatusEnabled, Quota: 42, UsedQuota: 7, LastLoginAt: now - 50, Group: "default", AffCode: "nova-list-aff"}
	require.NoError(t, db.Create(&user).Error)
	okTenant := Tenant{UserID: user.Id, CreatedAt: now, UpdatedAt: now}
	require.NoError(t, db.Create(&okTenant).Error)
	orphan := model.User{Username: "list-orphan", Password: "unused", DisplayName: "orphan", Status: common.UserStatusDisabled, Group: "default", AffCode: "nova-list-orph"}
	require.NoError(t, db.Create(&orphan).Error)
	require.NoError(t, db.Delete(&orphan).Error)
	require.NoError(t, db.Create(&Tenant{UserID: orphan.Id, CreatedAt: now, UpdatedAt: now}).Error)
	require.NoError(t, db.Create(&UsageEvent{EventID: "list-active-1", SourceType: "relay", SourceKey: "list-active-1", TenantID: okTenant.ID, TenantKey: user.Username, OccurredAt: now, CreatedAt: now}).Error)

	router := gin.New()
	RegisterRoutes(router.Group("/api"))
	request := signedRequest(t, secret, http.MethodGet, "/api/novapay/tenants?page=1&page_size=20", strconvUnix(time.Now()), "listorphanntenantsnonce1", nil, nil)
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	require.Equal(t, http.StatusOK, response.Code, response.Body.String())

	var body map[string]any
	require.NoError(t, common.Unmarshal(response.Body.Bytes(), &body))
	assert.Equal(t, true, body["success"])
	data := body["data"].(map[string]any)
	assert.EqualValues(t, 2, data["total"])
	assert.EqualValues(t, 1, data["page"])
	assert.EqualValues(t, 20, data["page_size"])
	items := data["items"].([]any)
	require.Len(t, items, 2)
	byKey := map[string]map[string]any{}
	for _, item := range items {
		row := item.(map[string]any)
		byKey[row["username"].(string)] = row
	}
	assert.Equal(t, "ok", byKey["nova-list-user"]["display_name"])
	assert.EqualValues(t, 42, byKey["nova-list-user"]["quota"])
	assert.EqualValues(t, 7, byKey["nova-list-user"]["used_quota"])
	assert.EqualValues(t, now, byKey["nova-list-user"]["last_active_at"])
	assert.Equal(t, "orphan", byKey["list-orphan"]["display_name"])
	assert.Equal(t, tenantStatusDeleted, byKey["list-orphan"]["status"])

	filtered := signedRequest(t, secret, http.MethodGet, "/api/novapay/tenants?page=1&page_size=20&keyword=orphan&status=deleted", strconvUnix(time.Now()), "listfiltertenantsnonce1", nil, nil)
	filteredResponse := httptest.NewRecorder()
	router.ServeHTTP(filteredResponse, filtered)
	require.Equal(t, http.StatusOK, filteredResponse.Code, filteredResponse.Body.String())
	var filteredBody map[string]any
	require.NoError(t, common.Unmarshal(filteredResponse.Body.Bytes(), &filteredBody))
	filteredItems := filteredBody["data"].(map[string]any)["items"].([]any)
	require.Len(t, filteredItems, 1)
	assert.Equal(t, "list-orphan", filteredItems[0].(map[string]any)["username"])
}

func TestHMACAuthenticationAndReplayProtection(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := openTestDatabase(t)
	require.NoError(t, migrate(db))
	secret := bytes.Repeat([]byte{0x5a}, 32)
	setTestRuntime(t, db, secret)

	router := gin.New()
	router.Use(HMACAuth())
	router.POST("/api/novapay/test", func(c *gin.Context) {
		body, err := c.GetRawData()
		require.NoError(t, err)
		c.String(http.StatusOK, string(body))
	})

	timestamp := strconvUnix(time.Now())
	nonce := "abcdefghijklmnopqrstuv"
	body := []byte(`{"tenant_key":"tenant-a"}`)
	request := signedRequest(t, secret, http.MethodPost, "/api/novapay/test?b=2&a=z&a=a", timestamp, nonce, body, body)
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	assert.Equal(t, http.StatusOK, response.Code)
	assert.Equal(t, string(body), response.Body.String())

	replay := signedRequest(t, secret, http.MethodPost, "/api/novapay/test?b=2&a=z&a=a", timestamp, nonce, body, body)
	replayResponse := httptest.NewRecorder()
	router.ServeHTTP(replayResponse, replay)
	assert.Equal(t, http.StatusUnauthorized, replayResponse.Code)
	assert.NotContains(t, replayResponse.Body.String(), "replay")
}

func TestHMACAuthenticationAllowsMissingKeyID(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := openTestDatabase(t)
	require.NoError(t, migrate(db))
	secret := bytes.Repeat([]byte{0x5a}, 32)
	setTestRuntime(t, db, secret)

	router := gin.New()
	router.Use(HMACAuth())
	router.POST("/api/novapay/test", func(c *gin.Context) {
		assert.Equal(t, "current", c.GetString("nova_key_id"))
		c.Status(http.StatusNoContent)
	})

	request := signedRequest(t, secret, http.MethodPost, "/api/novapay/test", strconvUnix(time.Now()), "missingkeyidnoncevalue1", nil, nil)
	request.Header.Del(headerKeyID)
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	assert.Equal(t, http.StatusNoContent, response.Code)
}

func TestCanonicalRequestGolden(t *testing.T) {
	request, err := http.NewRequest(http.MethodPost, "https://example.test/api/novapay/a%2Fb?z=hello+world&a=2&a=1&empty=", nil)
	require.NoError(t, err)
	assert.Equal(t, strings.Join([]string{
		"POST",
		"/api/novapay/a%2Fb",
		"a=1&a=2&empty=&z=hello+world",
		"1700000000",
		"abcdefghijklmnopqrstuv",
		"e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855",
	}, "\n"), canonicalRequest(request, "1700000000", "abcdefghijklmnopqrstuv", nil))
}

func TestHMACAuthenticationRejectsInvalidRequests(t *testing.T) {
	gin.SetMode(gin.TestMode)
	secret := bytes.Repeat([]byte{0x7b}, 32)
	now := time.Now()

	tests := []struct {
		name          string
		timestamp     string
		nonce         string
		signedBody    []byte
		transportBody []byte
		mutate        func(*http.Request)
	}{
		{name: "unknown key", timestamp: strconvUnix(now), nonce: "unknownkeynoncevalue12", signedBody: []byte(`{}`), transportBody: []byte(`{}`), mutate: func(request *http.Request) { request.Header.Set(headerKeyID, "unknown") }},
		{name: "expired timestamp", timestamp: strconvUnix(now.Add(-defaultMaxSkew - time.Second)), nonce: "expiredtimestampnonce1", signedBody: []byte(`{}`), transportBody: []byte(`{}`)},
		{name: "future timestamp", timestamp: strconvUnix(now.Add(defaultMaxSkew + time.Second)), nonce: "futuretimestampnonce12", signedBody: []byte(`{}`), transportBody: []byte(`{}`)},
		{name: "invalid nonce", timestamp: strconvUnix(now), nonce: "contains invalid spaces", signedBody: []byte(`{}`), transportBody: []byte(`{}`)},
		{name: "tampered body", timestamp: strconvUnix(now), nonce: "tamperedbodynoncevalue", signedBody: []byte(`{"value":1}`), transportBody: []byte(`{"value":2}`)},
		{name: "invalid signature encoding", timestamp: strconvUnix(now), nonce: "invalidsignaturenonce12", signedBody: []byte(`{}`), transportBody: []byte(`{}`), mutate: func(request *http.Request) { request.Header.Set(headerSignature, "not-hex") }},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			db := openTestDatabase(t)
			require.NoError(t, migrate(db))
			setTestRuntime(t, db, secret)
			router := gin.New()
			router.Use(HMACAuth())
			router.POST("/api/novapay/test", func(c *gin.Context) { c.Status(http.StatusNoContent) })

			request := signedRequest(t, secret, http.MethodPost, "/api/novapay/test", test.timestamp, test.nonce, test.signedBody, test.transportBody)
			if test.mutate != nil {
				test.mutate(request)
			}
			response := httptest.NewRecorder()
			router.ServeHTTP(response, request)

			assert.Equal(t, http.StatusUnauthorized, response.Code)
			assert.Contains(t, response.Body.String(), "nova_authentication_failed")
			assert.NotContains(t, response.Body.String(), test.name)
		})
	}
}

func TestHMACAcceptsPreviousKeyAndAtomicallyRejectsConcurrentReplay(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := openTestDatabase(t)
	require.NoError(t, migrate(db))
	currentSecret := bytes.Repeat([]byte{0x31}, 32)
	previousSecret := bytes.Repeat([]byte{0x32}, 32)
	setTestRuntime(t, db, currentSecret)
	runtimeState.Lock()
	runtimeState.config.Keys["previous"] = previousSecret
	runtimeState.Unlock()

	router := gin.New()
	router.Use(HMACAuth())
	router.POST("/api/novapay/test", func(c *gin.Context) { c.Status(http.StatusNoContent) })
	previous := signedRequest(t, previousSecret, http.MethodPost, "/api/novapay/test", strconvUnix(time.Now()), "previouskeynoncevalue1", nil, nil)
	previous.Header.Set(headerKeyID, "previous")
	previousResponse := httptest.NewRecorder()
	router.ServeHTTP(previousResponse, previous)
	assert.Equal(t, http.StatusNoContent, previousResponse.Code)

	timestamp := strconvUnix(time.Now())
	nonce := "concurrentnoncevalue12"
	responses := make(chan int, 2)
	var waitGroup sync.WaitGroup
	for range 2 {
		waitGroup.Go(func() {
			request := signedRequest(t, currentSecret, http.MethodPost, "/api/novapay/test", timestamp, nonce, nil, nil)
			response := httptest.NewRecorder()
			router.ServeHTTP(response, request)
			responses <- response.Code
		})
	}
	waitGroup.Wait()
	close(responses)
	statusCounts := map[int]int{}
	for status := range responses {
		statusCounts[status]++
	}
	assert.Equal(t, 1, statusCounts[http.StatusNoContent])
	assert.Equal(t, 1, statusCounts[http.StatusUnauthorized])
}

func TestTenantCreationReturnsSecretOnceAndQuotaIsIdempotent(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := openTestDatabase(t)
	require.NoError(t, db.AutoMigrate(&model.User{}, &model.Token{}))
	require.NoError(t, migrate(db))
	secret := bytes.Repeat([]byte{0x35}, 32)
	setTestRuntime(t, db, secret)
	previousModelDB := model.DB
	previousRedisEnabled := common.RedisEnabled
	model.DB = db
	common.RedisEnabled = false
	t.Cleanup(func() {
		model.DB = previousModelDB
		common.RedisEnabled = previousRedisEnabled
	})

	router := gin.New()
	api := router.Group("/api")
	RegisterRoutes(api)

	createBody := []byte(`{"tenant_key":"tenant-a","tenant_name":"Tenant A","initial_quota":1000,"request_id":"create-tenant-a"}`)
	create := signedRequest(t, secret, http.MethodPost, "/api/novapay/tenant", strconvUnix(time.Now()), "createtenantnoncevalue1", createBody, createBody)
	createResponse := httptest.NewRecorder()
	router.ServeHTTP(createResponse, create)
	require.Equal(t, http.StatusOK, createResponse.Code, createResponse.Body.String())
	var created map[string]any
	require.NoError(t, common.Unmarshal(createResponse.Body.Bytes(), &created))
	require.Equal(t, true, created["success"])
	assert.Equal(t, "ok", created["message"])
	createdData := created["data"].(map[string]any)
	assert.Equal(t, "tenant-a", createdData["username"])
	assert.Equal(t, "nova-tenant-a", createdData["token_name"])
	assert.EqualValues(t, 1000, createdData["quota"])
	tokenSecret := createdData["token_key"].(string)
	assert.True(t, strings.HasPrefix(tokenSecret, "sk-"))

	replay := signedRequest(t, secret, http.MethodPost, "/api/novapay/tenant", strconvUnix(time.Now()), "replaycreatenoncevalue1", createBody, createBody)
	replayResponse := httptest.NewRecorder()
	router.ServeHTTP(replayResponse, replay)
	require.Equal(t, http.StatusOK, replayResponse.Code, replayResponse.Body.String())
	assert.Equal(t, "true", replayResponse.Header().Get("Idempotency-Replayed"))
	var replayed map[string]any
	require.NoError(t, common.Unmarshal(replayResponse.Body.Bytes(), &replayed))
	assert.Equal(t, tokenSecret, replayed["data"].(map[string]any)["token_key"])

	getTenantReq := signedRequest(t, secret, http.MethodGet, "/api/novapay/tenant/tenant-a", strconvUnix(time.Now()), "gettenantdetailnonce12", nil, nil)
	getTenantResponse := httptest.NewRecorder()
	router.ServeHTTP(getTenantResponse, getTenantReq)
	require.Equal(t, http.StatusOK, getTenantResponse.Code, getTenantResponse.Body.String())
	var detail map[string]any
	require.NoError(t, common.Unmarshal(getTenantResponse.Body.Bytes(), &detail))
	assert.Equal(t, "ok", detail["message"])
	detailData := detail["data"].(map[string]any)
	assert.Equal(t, "tenant-a", detailData["username"])
	assert.Equal(t, "Tenant A", detailData["display_name"])
	assert.Equal(t, tenantStatusEnabled, detailData["status"])
	assert.EqualValues(t, 1000, detailData["quota"])
	assert.EqualValues(t, 0, detailData["used_quota"])
	assert.Equal(t, "nova-tenant-a", detailData["token_name"])
	maskedKey, _ := detailData["token_key"].(string)
	assert.True(t, strings.HasPrefix(maskedKey, "sk-"))
	assert.NotEqual(t, tokenSecret, maskedKey)
	assert.NotContains(t, maskedKey, strings.TrimPrefix(tokenSecret, "sk-"))
	assert.Contains(t, detailData["created_at"].(string), "T")
	assert.Contains(t, detailData["last_active_at"].(string), "T")
	_, hasTenantKey := detailData["tenant_key"]
	assert.False(t, hasTenantKey)

	updateBody := []byte(`{"request_id":"update-tenant-a-name","display_name":"Tenant A Renamed"}`)
	update := signedRequest(t, secret, http.MethodPut, "/api/novapay/tenant/tenant-a", strconvUnix(time.Now()), "updatetenantnoncevalue1", updateBody, updateBody)
	updateResponse := httptest.NewRecorder()
	router.ServeHTTP(updateResponse, update)
	require.Equal(t, http.StatusOK, updateResponse.Code, updateResponse.Body.String())
	var updated map[string]any
	require.NoError(t, common.Unmarshal(updateResponse.Body.Bytes(), &updated))
	assert.Equal(t, "Tenant A Renamed", updated["data"].(map[string]any)["display_name"])

	var storedUser model.User
	require.NoError(t, db.Select("id", "display_name").First(&storedUser, createdData["user_id"]).Error)
	assert.Equal(t, "Tenant A Renamed", storedUser.DisplayName)

	getAfterUpdate := signedRequest(t, secret, http.MethodGet, "/api/novapay/tenant/tenant-a", strconvUnix(time.Now()), "gettenantafterupdaten1", nil, nil)
	getAfterUpdateResponse := httptest.NewRecorder()
	router.ServeHTTP(getAfterUpdateResponse, getAfterUpdate)
	require.Equal(t, http.StatusOK, getAfterUpdateResponse.Code, getAfterUpdateResponse.Body.String())
	var afterUpdate map[string]any
	require.NoError(t, common.Unmarshal(getAfterUpdateResponse.Body.Bytes(), &afterUpdate))
	assert.Equal(t, "Tenant A Renamed", afterUpdate["data"].(map[string]any)["display_name"])

	quotaBody := []byte(`{"request_id":"tenant-credit-1","order_no":"credit-1","delta_quota":250,"reason":"test credit"}`)
	quota := signedRequest(t, secret, http.MethodPost, "/api/novapay/tenant/tenant-a/quota", strconvUnix(time.Now()), "tenantquotanoncevalue1", quotaBody, quotaBody)
	quotaResponse := httptest.NewRecorder()
	router.ServeHTTP(quotaResponse, quota)
	require.Equal(t, http.StatusOK, quotaResponse.Code, quotaResponse.Body.String())
	assert.Contains(t, quotaResponse.Body.String(), `"message":"ok"`)
	assert.Contains(t, quotaResponse.Body.String(), `"quota_before":1000`)
	assert.Contains(t, quotaResponse.Body.String(), `"quota_after":1250`)
	assert.Contains(t, quotaResponse.Body.String(), `"delta_quota":250`)
	assert.Contains(t, quotaResponse.Body.String(), `"order_no":"credit-1"`)

	quotaReplay := signedRequest(t, secret, http.MethodPost, "/api/novapay/tenant/tenant-a/quota", strconvUnix(time.Now()), "quotareplaynoncevalue1", quotaBody, quotaBody)
	quotaReplayResponse := httptest.NewRecorder()
	router.ServeHTTP(quotaReplayResponse, quotaReplay)
	require.Equal(t, http.StatusOK, quotaReplayResponse.Code, quotaReplayResponse.Body.String())
	assert.Equal(t, "true", quotaReplayResponse.Header().Get("Idempotency-Replayed"))

	conflictingQuotaBody := []byte(`{"request_id":"tenant-credit-1","order_no":"credit-1","delta_quota":251,"reason":"changed credit"}`)
	conflictingQuota := signedRequest(t, secret, http.MethodPost, "/api/novapay/tenant/tenant-a/quota", strconvUnix(time.Now()), "quotaconflictnonceval1", conflictingQuotaBody, conflictingQuotaBody)
	conflictingQuotaResponse := httptest.NewRecorder()
	router.ServeHTTP(conflictingQuotaResponse, conflictingQuota)
	assert.Equal(t, http.StatusConflict, conflictingQuotaResponse.Code)
	assert.Contains(t, conflictingQuotaResponse.Body.String(), "idempotency_conflict")

	// Same order_no with a new request_id must not double-apply (business unique).
	quotaBizReplayBody := []byte(`{"request_id":"tenant-credit-1-retry-key","order_no":"credit-1","delta_quota":250,"reason":"test credit"}`)
	quotaBizReplay := signedRequest(t, secret, http.MethodPost, "/api/novapay/tenant/tenant-a/quota", strconvUnix(time.Now()), "quotabizreplaynonceval1", quotaBizReplayBody, quotaBizReplayBody)
	quotaBizReplayResponse := httptest.NewRecorder()
	router.ServeHTTP(quotaBizReplayResponse, quotaBizReplay)
	require.Equal(t, http.StatusOK, quotaBizReplayResponse.Code, quotaBizReplayResponse.Body.String())
	assert.Contains(t, quotaBizReplayResponse.Body.String(), `"replayed":true`)
	assert.NotEqual(t, "true", quotaBizReplayResponse.Header().Get("Idempotency-Replayed"))
	assert.Contains(t, quotaBizReplayResponse.Body.String(), `"quota_before":1000`)
	assert.Contains(t, quotaBizReplayResponse.Body.String(), `"quota_after":1250`)

	// Same order_no + new request_id + different delta → business conflict.
	quotaOpConflictBody := []byte(`{"request_id":"tenant-credit-1-conflict-key","order_no":"credit-1","delta_quota":251,"reason":"changed credit"}`)
	quotaOpConflict := signedRequest(t, secret, http.MethodPost, "/api/novapay/tenant/tenant-a/quota", strconvUnix(time.Now()), "quotaopconflictnonceval", quotaOpConflictBody, quotaOpConflictBody)
	quotaOpConflictResponse := httptest.NewRecorder()
	router.ServeHTTP(quotaOpConflictResponse, quotaOpConflict)
	assert.Equal(t, http.StatusConflict, quotaOpConflictResponse.Code)
	assert.Contains(t, quotaOpConflictResponse.Body.String(), "operation_conflict")

	absoluteQuotaBody := []byte(`{"request_id":"tenant-absolute-1","order_no":"absolute-1","absolute_quota":2000,"reason":"reconcile"}`)
	absoluteQuota := signedRequest(t, secret, http.MethodPost, "/api/novapay/tenant/tenant-a/quota", strconvUnix(time.Now()), "tenantabsolutequota001", absoluteQuotaBody, absoluteQuotaBody)
	absoluteQuotaResponse := httptest.NewRecorder()
	router.ServeHTTP(absoluteQuotaResponse, absoluteQuota)
	require.Equal(t, http.StatusOK, absoluteQuotaResponse.Code, absoluteQuotaResponse.Body.String())
	assert.Contains(t, absoluteQuotaResponse.Body.String(), `"quota_before":1250`)
	assert.Contains(t, absoluteQuotaResponse.Body.String(), `"quota_after":2000`)
	assert.Contains(t, absoluteQuotaResponse.Body.String(), `"delta_quota":750`)

	var user model.User
	require.NoError(t, db.First(&user).Error)
	assert.Equal(t, 2000, user.Quota)

	list := signedRequest(t, secret, http.MethodGet, "/api/novapay/tenant/tenant-a/tokens", strconvUnix(time.Now()), "listtokensnoncevalue12", nil, nil)
	listResponse := httptest.NewRecorder()
	router.ServeHTTP(listResponse, list)
	require.Equal(t, http.StatusOK, listResponse.Code, listResponse.Body.String())
	assert.Contains(t, listResponse.Body.String(), `"token_name":"nova-tenant-a"`)
	assert.Contains(t, listResponse.Body.String(), `"key":"sk-`)
	assert.Contains(t, listResponse.Body.String(), `"status":"enabled"`)
	assert.Contains(t, listResponse.Body.String(), `"items"`)
	assert.Contains(t, listResponse.Body.String(), `"message":"ok"`)
	assert.Contains(t, listResponse.Body.String(), tokenSecret)

	disableBody := []byte(`{"request_id":"disable-tenant-a"}`)
	disable := signedRequest(t, secret, http.MethodPost, "/api/novapay/tenant/tenant-a/disable", strconvUnix(time.Now()), "disabletenantnoncevalue", disableBody, disableBody)
	disableResponse := httptest.NewRecorder()
	router.ServeHTTP(disableResponse, disable)
	require.Equal(t, http.StatusOK, disableResponse.Code, disableResponse.Body.String())
	var enabledTokens int64
	require.NoError(t, db.Model(&model.Token{}).Where("user_id = ? AND status = ?", user.Id, common.TokenStatusEnabled).Count(&enabledTokens).Error)
	assert.Zero(t, enabledTokens)

	enableBody := []byte(`{"request_id":"enable-tenant-a"}`)
	enable := signedRequest(t, secret, http.MethodPost, "/api/novapay/tenant/tenant-a/enable", strconvUnix(time.Now()), "enabletenantnoncevalue1", enableBody, enableBody)
	enableResponse := httptest.NewRecorder()
	router.ServeHTTP(enableResponse, enable)
	require.Equal(t, http.StatusOK, enableResponse.Code, enableResponse.Body.String())
	require.NoError(t, db.Model(&model.Token{}).Where("user_id = ? AND status = ?", user.Id, common.TokenStatusEnabled).Count(&enabledTokens).Error)
	assert.Zero(t, enabledTokens, "enabling a tenant must not reactivate individually disabled tokens")

	rotateBody := []byte(`{"request_id":"rotate-primary-token","reason":"suspected leak"}`)
	rotate := signedRequest(t, secret, http.MethodPost, "/api/novapay/tenant/tenant-a/token/rotate", strconvUnix(time.Now()), "rotatetokennoncevalue1", rotateBody, rotateBody)
	rotateResponse := httptest.NewRecorder()
	router.ServeHTTP(rotateResponse, rotate)
	require.Equal(t, http.StatusOK, rotateResponse.Code, rotateResponse.Body.String())
	assert.Contains(t, rotateResponse.Body.String(), `"old_token_name":"nova-tenant-a"`)
	assert.Contains(t, rotateResponse.Body.String(), `"token_name":"nova-tenant-a"`)
	assert.Contains(t, rotateResponse.Body.String(), `"token_key":"sk-`)
	assert.Contains(t, rotateResponse.Body.String(), `"message":"ok"`)
	var rotatePayload map[string]any
	require.NoError(t, common.Unmarshal(rotateResponse.Body.Bytes(), &rotatePayload))
	rotatedSecret := rotatePayload["data"].(map[string]any)["token_key"].(string)
	assert.NotEqual(t, tokenSecret, rotatedSecret)

	rotateReplay := signedRequest(t, secret, http.MethodPost, "/api/novapay/tenant/tenant-a/token/rotate", strconvUnix(time.Now()), "rotatereplaynoncevalue1", rotateBody, rotateBody)
	rotateReplayResponse := httptest.NewRecorder()
	router.ServeHTTP(rotateReplayResponse, rotateReplay)
	require.Equal(t, http.StatusOK, rotateReplayResponse.Code, rotateReplayResponse.Body.String())
	assert.Equal(t, "true", rotateReplayResponse.Header().Get("Idempotency-Replayed"))
	assert.Contains(t, rotateReplayResponse.Body.String(), rotatedSecret)

	tokenQuotaBody := []byte(`{"request_id":"token-set-quota-1","remain_quota":500000,"unlimited_quota":false,"reason":"employee quota"}`)
	tokenQuota := signedRequest(t, secret, http.MethodPost, "/api/novapay/tenant/tenant-a/tokens/nova-tenant-a/quota", strconvUnix(time.Now()), "tokenquotanoncevalue12", tokenQuotaBody, tokenQuotaBody)
	tokenQuotaResponse := httptest.NewRecorder()
	router.ServeHTTP(tokenQuotaResponse, tokenQuota)
	require.Equal(t, http.StatusOK, tokenQuotaResponse.Code, tokenQuotaResponse.Body.String())
	assert.Contains(t, tokenQuotaResponse.Body.String(), `"token_name":"nova-tenant-a"`)
	assert.Contains(t, tokenQuotaResponse.Body.String(), `"remain_quota":500000`)
	assert.Contains(t, tokenQuotaResponse.Body.String(), `"unlimited_quota":false`)

	tokenQuotaReplay := signedRequest(t, secret, http.MethodPost, "/api/novapay/tenant/tenant-a/tokens/nova-tenant-a/quota", strconvUnix(time.Now()), "tokenquotareplaynonce1", tokenQuotaBody, tokenQuotaBody)
	tokenQuotaReplayResponse := httptest.NewRecorder()
	router.ServeHTTP(tokenQuotaReplayResponse, tokenQuotaReplay)
	require.Equal(t, http.StatusOK, tokenQuotaReplayResponse.Code, tokenQuotaReplayResponse.Body.String())
	assert.Equal(t, "true", tokenQuotaReplayResponse.Header().Get("Idempotency-Replayed"))
	assert.Contains(t, tokenQuotaReplayResponse.Body.String(), `"remain_quota":500000`)

	var rotatedToken model.Token
	require.NoError(t, db.Where("user_id = ? AND name = ?", user.Id, "nova-tenant-a").First(&rotatedToken).Error)
	assert.Equal(t, 500000, rotatedToken.RemainQuota)
	assert.False(t, rotatedToken.UnlimitedQuota)

	deleteTokenBody := []byte(`{"request_id":"delete-rotated-token"}`)
	deleteTokenRequest := signedRequest(t, secret, http.MethodDelete, "/api/novapay/tenant/tenant-a/tokens/nova-tenant-a", strconvUnix(time.Now()), "deletetokennoncevalue1", deleteTokenBody, deleteTokenBody)
	deleteTokenResponse := httptest.NewRecorder()
	router.ServeHTTP(deleteTokenResponse, deleteTokenRequest)
	require.Equal(t, http.StatusOK, deleteTokenResponse.Code, deleteTokenResponse.Body.String())

	deleteTenantBody := []byte(`{"request_id":"delete-tenant-a"}`)
	deleteTenantRequest := signedRequest(t, secret, http.MethodDelete, "/api/novapay/tenant/tenant-a", strconvUnix(time.Now()), "deletetenantnoncevalue", deleteTenantBody, deleteTenantBody)
	deleteTenantResponse := httptest.NewRecorder()
	router.ServeHTTP(deleteTenantResponse, deleteTenantRequest)
	require.Equal(t, http.StatusOK, deleteTenantResponse.Code, deleteTenantResponse.Body.String())

	reenableDeletedBody := []byte(`{"request_id":"reenable-deleted-tenant"}`)
	reenableDeleted := signedRequest(t, secret, http.MethodPost, "/api/novapay/tenant/tenant-a/enable", strconvUnix(time.Now()), "reenabledeletednoncevalue", reenableDeletedBody, reenableDeletedBody)
	reenableDeletedResponse := httptest.NewRecorder()
	router.ServeHTTP(reenableDeletedResponse, reenableDeleted)
	assert.Equal(t, http.StatusConflict, reenableDeletedResponse.Code)
	assert.Contains(t, reenableDeletedResponse.Body.String(), "tenant_deleted")
	assert.Contains(t, reenableDeletedResponse.Body.String(), `"message":`)
}

func TestRelayAttributionRejectsTenantMismatch(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := openTestDatabase(t)
	require.NoError(t, db.AutoMigrate(&model.User{}, &model.Token{}))
	require.NoError(t, migrate(db))
	secret := bytes.Repeat([]byte{0x47}, 32)
	setTestRuntime(t, db, secret)
	service.RegisterUsageLifecycleObserver(recordUsageLifecycle)
	t.Cleanup(func() { service.RegisterUsageLifecycleObserver(nil) })

	user := model.User{Username: "nova-relay-user", Password: "unused", Status: common.UserStatusEnabled, Quota: 1000, Group: "default", AffCode: "nova-relay-aff"}
	require.NoError(t, db.Create(&user).Error)
	token := model.Token{UserId: user.Id, Key: "relay-token-key", Name: "primary", Status: common.TokenStatusEnabled, RemainQuota: 1000}
	require.NoError(t, db.Create(&token).Error)
	require.NoError(t, db.Create(&Tenant{UserID: user.Id, CreatedAt: time.Now().Unix(), UpdatedAt: time.Now().Unix()}).Error)

	router := gin.New()
	router.Use(func(c *gin.Context) {
		c.Set("id", user.Id)
		c.Set("token_id", token.Id)
		c.Set(common.RequestIdKey, c.GetHeader("X-Test-Request-Id"))
		c.Next()
	}, RelayAttribution())
	router.POST("/v1/chat/completions", func(c *gin.Context) {
		attribution, exists := c.Get("usage_attribution")
		assert.True(t, exists)
		assert.NotNil(t, attribution)
		c.Status(http.StatusNoContent)
	})

	body := []byte(`{"model":"test"}`)
	wrong := signedRequest(t, secret, http.MethodPost, "/v1/chat/completions", strconvUnix(time.Now()), "wrongtenantnoncevalue1", body, body)
	wrong.Header.Set("X-Test-Request-Id", "gateway-wrong-1")
	wrong.Header.Set("X-Nova-Tenant-Key", "other-tenant")
	wrongResponse := httptest.NewRecorder()
	router.ServeHTTP(wrongResponse, wrong)
	assert.Equal(t, http.StatusUnauthorized, wrongResponse.Code)

	valid := signedRequest(t, secret, http.MethodPost, "/v1/chat/completions", strconvUnix(time.Now()), "validtenantnoncevalue1", body, body)
	valid.Header.Set("X-Test-Request-Id", "gateway-request-1")
	valid.Header.Set("X-Nova-Tenant-Key", "nova-relay-user")
	validResponse := httptest.NewRecorder()
	router.ServeHTTP(validResponse, valid)
	assert.Equal(t, http.StatusNoContent, validResponse.Code)
	var attributionCount int64
	require.NoError(t, db.Model(&Attribution{}).Where("source_type = ? AND source_key = ?", "relay", "gateway-request-1").Count(&attributionCount).Error)
	assert.EqualValues(t, 1, attributionCount)

	failureRouter := gin.New()
	failureRouter.Use(func(c *gin.Context) {
		c.Set("id", user.Id)
		c.Set("token_id", token.Id)
		c.Set(common.RequestIdKey, "gateway-failure-1")
		c.Next()
	}, RelayAttribution())
	failureRouter.POST("/v1/chat/completions", func(c *gin.Context) { c.Status(http.StatusBadGateway) })
	failure := signedRequest(t, secret, http.MethodPost, "/v1/chat/completions", strconvUnix(time.Now()), "failedrelaynoncevalue12", body, body)
	failure.Header.Set("X-Nova-Tenant-Key", "nova-relay-user")
	failureResponse := httptest.NewRecorder()
	failureRouter.ServeHTTP(failureResponse, failure)
	assert.Equal(t, http.StatusBadGateway, failureResponse.Code)
	require.NoError(t, db.Model(&Attribution{}).Where("source_key = ?", "gateway-failure-1").Count(&attributionCount).Error)
	assert.Zero(t, attributionCount)
}

func TestFinalizedUsageCreatesOneLedgerAndOutboxRecord(t *testing.T) {
	db := openTestDatabase(t)
	require.NoError(t, db.AutoMigrate(&model.User{}, &model.Token{}))
	require.NoError(t, migrate(db))
	secret := bytes.Repeat([]byte{0x61}, 32)
	setTestRuntime(t, db, secret)

	user := model.User{Username: "nova-ledger-user", Password: "unused", Status: common.UserStatusEnabled, Quota: 1000, Group: "default", AffCode: "nova-ledger-aff"}
	require.NoError(t, db.Create(&user).Error)
	token := model.Token{UserId: user.Id, Key: "ledger-token-key", Name: "primary", Status: common.TokenStatusEnabled, RemainQuota: 1000}
	require.NoError(t, db.Create(&token).Error)
	tenant := Tenant{UserID: user.Id, CreatedAt: time.Now().Unix(), UpdatedAt: time.Now().Unix()}
	require.NoError(t, db.Create(&tenant).Error)

	event := service.UsageLifecycleEvent{
		SourceType:       "relay",
		SourceKey:        "request-123",
		UserID:           user.Id,
		TokenID:          token.Id,
		TokenName:        token.Name,
		RequestID:        "request-123",
		ModelName:        "test-model",
		Quota:            42,
		PromptTokens:     10,
		CompletionTokens: 5,
		TotalTokens:      15,
		Attribution:      &service.UsageAttribution{Provider: "nova", Subject: user.Username, RequestID: "nova-request-1", Verified: true},
	}
	require.NoError(t, recordFinalizedUsage(event))
	require.NoError(t, recordFinalizedUsage(event))

	var eventCount int64
	var outboxCount int64
	require.NoError(t, db.Model(&UsageEvent{}).Count(&eventCount).Error)
	require.NoError(t, db.Model(&Outbox{}).Count(&outboxCount).Error)
	assert.EqualValues(t, 1, eventCount)
	assert.EqualValues(t, 1, outboxCount)

	var stored UsageEvent
	require.NoError(t, db.First(&stored).Error)
	assert.Equal(t, event.SourceKey, stored.SourceKey)
	assert.Equal(t, 42, stored.Quota)
	assert.Equal(t, 10, stored.PromptTokens)
	assert.Equal(t, 5, stored.CompletionTokens)

	var outbox Outbox
	require.NoError(t, db.First(&outbox).Error)
	assert.Equal(t, "pending", outbox.Status)
	assert.Contains(t, outbox.Payload, `"event_type":"nova.usage.reported"`)
	assert.NotContains(t, outbox.Payload, `"usage_payload"`)
	assert.NotContains(t, outbox.Payload, token.Key)
	require.NoError(t, db.Delete(&outbox).Error)
	require.NoError(t, reconcileMissingOutbox(db, Config{Exchange: "nova.events", RoutingKey: "nova.usage.reported", BatchSize: 10}, time.Now()))
	outbox = Outbox{}
	require.NoError(t, db.First(&outbox).Error)
	var message struct {
		Source struct {
			Key string `json:"key"`
		} `json:"source"`
	}
	require.NoError(t, common.UnmarshalJsonStr(outbox.Payload, &message))
	assert.Equal(t, event.SourceKey, message.Source.Key)
}

func TestGenericTaskObserverRecordsOnlyFinalSuccess(t *testing.T) {
	db := openTestDatabase(t)
	require.NoError(t, db.AutoMigrate(&model.User{}, &model.Token{}))
	require.NoError(t, migrate(db))
	setTestRuntime(t, db, bytes.Repeat([]byte{0x62}, 32))
	service.RegisterUsageLifecycleObserver(recordUsageLifecycle)
	t.Cleanup(func() { service.RegisterUsageLifecycleObserver(nil) })

	user := model.User{Username: "nova-task-user", Password: "unused", Status: common.UserStatusEnabled, Quota: 1000, Group: "default", AffCode: "nova-task-aff"}
	require.NoError(t, db.Create(&user).Error)
	token := model.Token{UserId: user.Id, Key: "task-token-key", Name: "primary", Status: common.TokenStatusEnabled, RemainQuota: 1000}
	require.NoError(t, db.Create(&token).Error)
	tenant := Tenant{UserID: user.Id, CreatedAt: time.Now().Unix(), UpdatedAt: time.Now().Unix()}
	require.NoError(t, db.Create(&tenant).Error)
	require.NoError(t, db.Create(&Attribution{SourceType: "task", SourceKey: "task-success", TenantID: tenant.ID, TenantKey: user.Username, UserID: user.Id, TokenID: token.Id, NovaRequestID: "nova-task-request", CreatedAt: time.Now().Unix()}).Error)

	task := &model.Task{TaskID: "task-success", UserId: user.Id, Status: model.TaskStatusSuccess, Quota: 77, Group: "default"}
	task.PrivateData.TokenId = token.Id
	service.RecordTaskFinalizedUsage(context.Background(), task, &relaycommon.TaskInfo{Status: model.TaskStatusSuccess, TotalTokens: 12, CompletionTokens: 5})

	failedTask := &model.Task{TaskID: "task-failure", UserId: user.Id, Status: model.TaskStatusFailure, Quota: 10}
	failedTask.PrivateData.TokenId = token.Id
	service.RecordTaskFinalizedUsage(context.Background(), failedTask, &relaycommon.TaskInfo{Status: model.TaskStatusFailure})

	var events []UsageEvent
	require.NoError(t, db.Find(&events).Error)
	require.Len(t, events, 1)
	assert.Equal(t, "task-success", events[0].SourceKey)
	assert.Equal(t, 77, events[0].Quota)
	assert.Equal(t, 12, events[0].TotalTokens)
	var attributionCount int64
	require.NoError(t, db.Model(&Attribution{}).Count(&attributionCount).Error)
	assert.Zero(t, attributionCount)
}

func TestRabbitMQPublisherConfirm(t *testing.T) {
	rabbitURL := strings.TrimSpace(os.Getenv("TEST_RABBITMQ_URL"))
	if rabbitURL == "" {
		t.Skip("TEST_RABBITMQ_URL is not configured")
	}
	closePublisherConnection()
	t.Cleanup(closePublisherConnection)

	connection, err := amqp.Dial(rabbitURL)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, connection.Close()) })
	channel, err := connection.Channel()
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, channel.Close()) })

	exchange := "nova.events.test." + uuid.NewString()
	routingKey := "nova.usage.reported"
	require.NoError(t, channel.ExchangeDeclare(exchange, "topic", true, false, false, false, nil))
	t.Cleanup(func() { _ = channel.ExchangeDelete(exchange, false, false) })
	queue, err := channel.QueueDeclare("", false, true, true, false, nil)
	require.NoError(t, err)
	require.NoError(t, channel.QueueBind(queue.Name, routingKey, exchange, false, nil))
	deliveries, err := channel.Consume(queue.Name, "", true, true, false, false, nil)
	require.NoError(t, err)

	db := openTestDatabase(t)
	require.NoError(t, migrate(db))
	now := time.Now().Unix()
	eventID := uuid.NewString()
	require.NoError(t, db.Create(&Outbox{
		EventID: eventID, ExchangeName: exchange, RoutingKey: routingKey,
		Payload: `{"schema_version":1}`, Status: "pending", NextAttemptAt: now,
		CreatedAt: now, UpdatedAt: now,
	}).Error)
	config := Config{RabbitMQURL: rabbitURL, Exchange: exchange, RoutingKey: routingKey, BatchSize: 10, MaxAttempts: 3}
	publishOutboxBatch(context.Background(), config, db)

	select {
	case delivery := <-deliveries:
		assert.Equal(t, eventID, delivery.MessageId)
		assert.Equal(t, uint8(amqp.Persistent), delivery.DeliveryMode)
		assert.JSONEq(t, `{"schema_version":1}`, string(delivery.Body))
	case <-time.After(10 * time.Second):
		t.Fatal("timed out waiting for RabbitMQ delivery")
	}
	var outbox Outbox
	require.NoError(t, db.Where("event_id = ?", eventID).First(&outbox).Error)
	assert.Equal(t, "published", outbox.Status)
	assert.NotZero(t, outbox.PublishedAt)

	recoveryEventID := uuid.NewString()
	require.NoError(t, db.Create(&Outbox{
		EventID: recoveryEventID, ExchangeName: exchange, RoutingKey: routingKey,
		Payload: `{"schema_version":1}`, Status: "pending", NextAttemptAt: time.Now().Unix(),
		CreatedAt: time.Now().Unix(), UpdatedAt: time.Now().Unix(),
	}).Error)
	closePublisherConnection()
	failingConfig := config
	failingConfig.RabbitMQURL = "amqp://127.0.0.1:1/"
	failingConfig.MaxAttempts = 3
	publishOutboxBatch(context.Background(), failingConfig, db)
	require.NoError(t, db.Model(&Outbox{}).Where("event_id = ?", recoveryEventID).Update("next_attempt_at", time.Now().Unix()).Error)
	publishOutboxBatch(context.Background(), config, db)

	select {
	case delivery := <-deliveries:
		assert.Equal(t, recoveryEventID, delivery.MessageId)
	case <-time.After(10 * time.Second):
		t.Fatal("timed out waiting for recovered RabbitMQ delivery")
	}
	outbox = Outbox{}
	require.NoError(t, db.Where("event_id = ?", recoveryEventID).First(&outbox).Error)
	assert.Equal(t, "published", outbox.Status)
	assert.Equal(t, 1, outbox.Attempts)
}

func TestOutboxExpiredLeaseMovesToDeadAfterPublishFailure(t *testing.T) {
	db := openTestDatabase(t)
	require.NoError(t, migrate(db))
	now := time.Now().Unix()
	require.NoError(t, db.Create(&Outbox{
		EventID: uuid.NewString(), ExchangeName: "nova.events", RoutingKey: "nova.usage.reported",
		Payload: `{}`, Status: "publishing", LockedBy: "dead-worker", LockedUntil: now - 1,
		NextAttemptAt: now - 1, CreatedAt: now - 60, UpdatedAt: now - 60,
	}).Error)
	publishOutboxBatch(context.Background(), Config{
		RabbitMQURL: "amqp://127.0.0.1:1/", Exchange: "nova.events", RoutingKey: "nova.usage.reported",
		PublishTimeout: time.Second, BatchSize: 10, MaxAttempts: 1,
	}, db)

	var outbox Outbox
	require.NoError(t, db.First(&outbox).Error)
	assert.Equal(t, "dead", outbox.Status)
	assert.Equal(t, 1, outbox.Attempts)
	assert.Empty(t, outbox.LockedBy)
	assert.Zero(t, outbox.LockedUntil)
}

func TestOutboxClaimAllowsOnlyOneConcurrentWorker(t *testing.T) {
	db := openTestDatabase(t)
	require.NoError(t, migrate(db))
	assertSingleOutboxClaim(t, db)
}

func TestOutboxClaimAllowsOnlyOneConcurrentWorkerMySQL(t *testing.T) {
	dsn := strings.TrimSpace(os.Getenv("TEST_MYSQL_DSN"))
	if dsn == "" {
		t.Skip("TEST_MYSQL_DSN is not configured")
	}
	db, err := gorm.Open(mysql.Open(dsn), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, migrate(db))
	t.Cleanup(func() {
		_ = db.Where("event_id LIKE ?", "claim-%").Delete(&Outbox{}).Error
	})
	assertSingleOutboxClaim(t, db)
}

func assertSingleOutboxClaim(t *testing.T, db *gorm.DB) {
	t.Helper()
	now := time.Now().Unix()
	eventID := "claim-" + uuid.NewString()
	require.NoError(t, db.Create(&Outbox{
		EventID: eventID, ExchangeName: "nova.events", RoutingKey: "nova.usage.reported",
		Payload: `{"schema_version":1}`, Status: "pending", NextAttemptAt: now,
		CreatedAt: now, UpdatedAt: now,
	}).Error)

	var outbox Outbox
	require.NoError(t, db.Where("event_id = ?", eventID).First(&outbox).Error)
	var claimed int64
	var wg sync.WaitGroup
	for range 8 {
		wg.Go(func() {
			claim := db.Model(&Outbox{}).
				Where("id = ? AND status = ? AND next_attempt_at <= ?", outbox.ID, "pending", now).
				Updates(map[string]any{
					"status": "publishing", "locked_by": uuid.NewString(),
					"locked_until": now + 30, "updated_at": now,
				})
			require.NoError(t, claim.Error)
			if claim.RowsAffected == 1 {
				atomic.AddInt64(&claimed, 1)
			}
		})
	}
	wg.Wait()
	assert.EqualValues(t, 1, claimed)

	require.NoError(t, db.Where("event_id = ?", eventID).First(&outbox).Error)
	assert.Equal(t, "publishing", outbox.Status)
	assert.NotEmpty(t, outbox.LockedBy)
}

func TestNonNovaAndFailedRelayDoNotCreateUsageEvents(t *testing.T) {
	db := openTestDatabase(t)
	require.NoError(t, db.AutoMigrate(&model.User{}, &model.Token{}))
	require.NoError(t, migrate(db))
	setTestRuntime(t, db, bytes.Repeat([]byte{0x63}, 32))

	user := model.User{Username: "nova-negative-user", Password: "unused", Status: common.UserStatusEnabled, Quota: 1000, Group: "default", AffCode: "nova-neg-aff"}
	require.NoError(t, db.Create(&user).Error)
	token := model.Token{UserId: user.Id, Key: "negative-token-key", Name: "primary", Status: common.TokenStatusEnabled, RemainQuota: 1000}
	require.NoError(t, db.Create(&token).Error)

	require.NoError(t, recordFinalizedUsage(service.UsageLifecycleEvent{
		SourceType: "relay", SourceKey: "non-nova-request", UserID: user.Id, TokenID: token.Id,
		Quota: 9, Attribution: &service.UsageAttribution{Provider: "nova", Subject: "missing-tenant", Verified: true},
	}))

	tenant := Tenant{UserID: user.Id, CreatedAt: time.Now().Unix(), UpdatedAt: time.Now().Unix()}
	require.NoError(t, db.Create(&tenant).Error)
	require.NoError(t, recordFinalizedUsage(service.UsageLifecycleEvent{
		SourceType: "relay", SourceKey: "unverified-request", UserID: user.Id, TokenID: token.Id,
		Quota: 11, Attribution: &service.UsageAttribution{Provider: "nova", Subject: user.Username, Verified: false},
	}))
	require.EqualError(t, recordFinalizedUsage(service.UsageLifecycleEvent{
		SourceType: "relay", SourceKey: "negative-quota", UserID: user.Id, TokenID: token.Id, Quota: -1,
		Attribution: &service.UsageAttribution{Provider: "nova", Subject: user.Username, Verified: true},
	}), "invalid finalized usage event")

	var eventCount int64
	require.NoError(t, db.Model(&UsageEvent{}).Count(&eventCount).Error)
	assert.Zero(t, eventCount)
}

func TestRetentionCleanupRemovesOnlyExpiredOperationalRecords(t *testing.T) {
	db := openTestDatabase(t)
	require.NoError(t, migrate(db))
	now := time.Now().UTC()
	old := now.Add(-100 * 24 * time.Hour).Unix()
	require.NoError(t, db.Create(&ReplayNonce{KeyID: "old", NonceHash: strings.Repeat("b", 64), ExpiresAt: now.Add(-time.Hour).Unix(), CreatedAt: old}).Error)
	require.NoError(t, db.Create(&IdempotencyRecord{Scope: "old", IdempotencyKey: "expired-key", RequestHash: strings.Repeat("c", 64), Status: "completed", ExpiresAt: now.Add(-time.Hour).Unix(), CreatedAt: old, UpdatedAt: old}).Error)
	require.NoError(t, db.Create(&Outbox{EventID: "published-old", Status: "published", PublishedAt: old, CreatedAt: old, UpdatedAt: old}).Error)
	require.NoError(t, db.Create(&Outbox{EventID: "dead-old", Status: "dead", CreatedAt: old, UpdatedAt: old}).Error)
	require.NoError(t, db.Create(&Outbox{EventID: "pending-old", Status: "pending", CreatedAt: old, UpdatedAt: old}).Error)
	require.NoError(t, db.Create(&UsageEvent{EventID: "usage-old", SourceType: "relay", SourceKey: "usage-old", TenantID: 1, OccurredAt: old, CreatedAt: old}).Error)

	require.NoError(t, cleanupExpiredRecords(db, Config{OutboxRetention: 30 * 24 * time.Hour, DeadRetention: 90 * 24 * time.Hour, EventRetention: 90 * 24 * time.Hour}, now))
	var remaining []Outbox
	require.NoError(t, db.Order("event_id").Find(&remaining).Error)
	require.Len(t, remaining, 1)
	assert.Equal(t, "pending-old", remaining[0].EventID)
	var usageCount int64
	require.NoError(t, db.Model(&UsageEvent{}).Count(&usageCount).Error)
	assert.Zero(t, usageCount)
}

func openTestDatabase(t *testing.T) *gorm.DB {
	t.Helper()
	dsnName := strings.NewReplacer("/", "_", " ", "_").Replace(t.Name()) + "_" + uuid.NewString()
	db, err := gorm.Open(sqlite.Open(fmt.Sprintf("file:%s?mode=memory&cache=shared", dsnName)), &gorm.Config{})
	require.NoError(t, err)
	return db
}

func setTestRuntime(t *testing.T, db *gorm.DB, secret []byte) {
	t.Helper()
	runtimeState.Lock()
	previousConfig := runtimeState.config
	previousDB := runtimeState.db
	runtimeState.config = Config{
		Enabled:      true,
		Keys:         map[string][]byte{"current": secret},
		CurrentKeyID: "current",
		MaxSkew:      defaultMaxSkew,
		NonceTTL:     defaultNonceTTL,
	}
	runtimeState.db = db
	runtimeState.Unlock()
	t.Cleanup(func() {
		runtimeState.Lock()
		runtimeState.config = previousConfig
		runtimeState.db = previousDB
		runtimeState.Unlock()
	})
}

func signedRequest(t *testing.T, secret []byte, method, target, timestamp, nonce string, signedBody, transportBody []byte) *http.Request {
	t.Helper()
	requestForSignature, err := http.NewRequest(method, target, bytes.NewReader(signedBody))
	require.NoError(t, err)
	signature := calculateSignature(secret, canonicalRequest(requestForSignature, timestamp, nonce, signedBody))

	request, err := http.NewRequest(method, target, bytes.NewReader(transportBody))
	require.NoError(t, err)
	request.Header.Set(headerKeyID, "current")
	request.Header.Set(headerTimestamp, timestamp)
	request.Header.Set(headerNonce, nonce)
	request.Header.Set(headerSignature, hex.EncodeToString(signature))
	return request
}

func strconvUnix(value time.Time) string {
	return fmt.Sprintf("%d", value.Unix())
}
