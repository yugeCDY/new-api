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
	tenant := Tenant{TenantKey: "matrix-tenant", UserID: 1001, DisplayName: "Matrix", Status: tenantStatusEnabled, CreatedAt: now, UpdatedAt: now}
	require.NoError(t, db.Create(&tenant).Error)
	duplicateTenant := Tenant{TenantKey: tenant.TenantKey, UserID: 1002, Status: tenantStatusEnabled, CreatedAt: now, UpdatedAt: now}
	assert.Error(t, db.Create(&duplicateTenant).Error)

	nonce := ReplayNonce{KeyID: "current", NonceHash: strings.Repeat("a", 64), RequestTimestamp: now, ExpiresAt: now + 600, CreatedAt: now}
	require.NoError(t, db.Create(&nonce).Error)
	assert.Error(t, db.Create(&ReplayNonce{KeyID: nonce.KeyID, NonceHash: nonce.NonceHash, RequestTimestamp: now, ExpiresAt: now + 600, CreatedAt: now}).Error)

	usage := UsageEvent{EventID: "matrix-event", SourceType: "relay", SourceKey: "matrix-request", TenantID: tenant.ID, TenantKey: tenant.TenantKey, CreatedAt: now}
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
		TenantKey: tenant.TenantKey, TargetType: quotaTargetTenant, TargetRef: "",
		OperationID: "op-unique-1", Delta: 10, Reason: "test", ResultQuota: 10, CreatedAt: now,
	}
	require.NoError(t, db.Create(&quotaOp).Error)
	assert.Error(t, db.Create(&QuotaOperation{
		TenantKey: tenant.TenantKey, TargetType: quotaTargetTenant, TargetRef: "",
		OperationID: "op-unique-1", Delta: 20, Reason: "dup", ResultQuota: 30, CreatedAt: now,
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

	createBody := []byte(`{"tenant_key":"tenant-a","display_name":"Tenant A","quota":1000,"token_name":"primary","token_quota":500}`)
	create := signedRequest(t, secret, http.MethodPost, "/api/novapay/tenant", strconvUnix(time.Now()), "createtenantnoncevalue1", createBody, createBody)
	create.Header.Set("Idempotency-Key", "create-tenant-a")
	createResponse := httptest.NewRecorder()
	router.ServeHTTP(createResponse, create)
	require.Equal(t, http.StatusOK, createResponse.Code, createResponse.Body.String())
	var created map[string]any
	require.NoError(t, common.Unmarshal(createResponse.Body.Bytes(), &created))
	createdToken := created["token"].(map[string]any)
	tokenSecret := createdToken["token"].(string)
	assert.True(t, strings.HasPrefix(tokenSecret, "sk-"))
	assert.True(t, createdToken["secret_visible"].(bool))

	replay := signedRequest(t, secret, http.MethodPost, "/api/novapay/tenant", strconvUnix(time.Now()), "replaycreatenoncevalue1", createBody, createBody)
	replay.Header.Set("Idempotency-Key", "create-tenant-a")
	replayResponse := httptest.NewRecorder()
	router.ServeHTTP(replayResponse, replay)
	require.Equal(t, http.StatusOK, replayResponse.Code, replayResponse.Body.String())
	assert.Equal(t, "true", replayResponse.Header().Get("Idempotency-Replayed"))
	assert.NotContains(t, replayResponse.Body.String(), tokenSecret)
	assert.NotContains(t, replayResponse.Body.String(), `"token":"sk-`)

	quotaBody := []byte(`{"operation_id":"credit-1","delta":250,"reason":"test credit"}`)
	quota := signedRequest(t, secret, http.MethodPost, "/api/novapay/tenant/tenant-a/quota", strconvUnix(time.Now()), "tenantquotanoncevalue1", quotaBody, quotaBody)
	quota.Header.Set("Idempotency-Key", "tenant-credit-1")
	quotaResponse := httptest.NewRecorder()
	router.ServeHTTP(quotaResponse, quota)
	require.Equal(t, http.StatusOK, quotaResponse.Code, quotaResponse.Body.String())

	quotaReplay := signedRequest(t, secret, http.MethodPost, "/api/novapay/tenant/tenant-a/quota", strconvUnix(time.Now()), "quotareplaynoncevalue1", quotaBody, quotaBody)
	quotaReplay.Header.Set("Idempotency-Key", "tenant-credit-1")
	quotaReplayResponse := httptest.NewRecorder()
	router.ServeHTTP(quotaReplayResponse, quotaReplay)
	require.Equal(t, http.StatusOK, quotaReplayResponse.Code, quotaReplayResponse.Body.String())
	assert.Equal(t, "true", quotaReplayResponse.Header().Get("Idempotency-Replayed"))

	conflictingQuotaBody := []byte(`{"operation_id":"credit-1","delta":251,"reason":"changed credit"}`)
	conflictingQuota := signedRequest(t, secret, http.MethodPost, "/api/novapay/tenant/tenant-a/quota", strconvUnix(time.Now()), "quotaconflictnonceval1", conflictingQuotaBody, conflictingQuotaBody)
	conflictingQuota.Header.Set("Idempotency-Key", "tenant-credit-1")
	conflictingQuotaResponse := httptest.NewRecorder()
	router.ServeHTTP(conflictingQuotaResponse, conflictingQuota)
	assert.Equal(t, http.StatusConflict, conflictingQuotaResponse.Code)
	assert.Contains(t, conflictingQuotaResponse.Body.String(), "idempotency_conflict")

	// Same operation_id with a new Idempotency-Key must not double-apply (business unique).
	quotaBizReplay := signedRequest(t, secret, http.MethodPost, "/api/novapay/tenant/tenant-a/quota", strconvUnix(time.Now()), "quotabizreplaynonceval1", quotaBody, quotaBody)
	quotaBizReplay.Header.Set("Idempotency-Key", "tenant-credit-1-retry-key")
	quotaBizReplayResponse := httptest.NewRecorder()
	router.ServeHTTP(quotaBizReplayResponse, quotaBizReplay)
	require.Equal(t, http.StatusOK, quotaBizReplayResponse.Code, quotaBizReplayResponse.Body.String())
	assert.Contains(t, quotaBizReplayResponse.Body.String(), `"replayed":true`)
	assert.NotEqual(t, "true", quotaBizReplayResponse.Header().Get("Idempotency-Replayed"))

	// Same operation_id + new Key + different delta → business conflict.
	quotaOpConflict := signedRequest(t, secret, http.MethodPost, "/api/novapay/tenant/tenant-a/quota", strconvUnix(time.Now()), "quotaopconflictnonceval", conflictingQuotaBody, conflictingQuotaBody)
	quotaOpConflict.Header.Set("Idempotency-Key", "tenant-credit-1-conflict-key")
	quotaOpConflictResponse := httptest.NewRecorder()
	router.ServeHTTP(quotaOpConflictResponse, quotaOpConflict)
	assert.Equal(t, http.StatusConflict, quotaOpConflictResponse.Code)
	assert.Contains(t, quotaOpConflictResponse.Body.String(), "operation_conflict")

	var user model.User
	require.NoError(t, db.First(&user).Error)
	assert.Equal(t, 1250, user.Quota)

	list := signedRequest(t, secret, http.MethodGet, "/api/novapay/tenant/tenant-a/tokens", strconvUnix(time.Now()), "listtokensnoncevalue12", nil, nil)
	listResponse := httptest.NewRecorder()
	router.ServeHTTP(listResponse, list)
	require.Equal(t, http.StatusOK, listResponse.Code, listResponse.Body.String())
	assert.NotContains(t, listResponse.Body.String(), tokenSecret)
	assert.Contains(t, listResponse.Body.String(), "masked_token")

	disable := signedRequest(t, secret, http.MethodPost, "/api/novapay/tenant/tenant-a/disable", strconvUnix(time.Now()), "disabletenantnoncevalue", nil, nil)
	disable.Header.Set("Idempotency-Key", "disable-tenant-a")
	disableResponse := httptest.NewRecorder()
	router.ServeHTTP(disableResponse, disable)
	require.Equal(t, http.StatusOK, disableResponse.Code, disableResponse.Body.String())
	var enabledTokens int64
	require.NoError(t, db.Model(&model.Token{}).Where("user_id = ? AND status = ?", user.Id, common.TokenStatusEnabled).Count(&enabledTokens).Error)
	assert.Zero(t, enabledTokens)

	enable := signedRequest(t, secret, http.MethodPost, "/api/novapay/tenant/tenant-a/enable", strconvUnix(time.Now()), "enabletenantnoncevalue1", nil, nil)
	enable.Header.Set("Idempotency-Key", "enable-tenant-a")
	enableResponse := httptest.NewRecorder()
	router.ServeHTTP(enableResponse, enable)
	require.Equal(t, http.StatusOK, enableResponse.Code, enableResponse.Body.String())
	require.NoError(t, db.Model(&model.Token{}).Where("user_id = ? AND status = ?", user.Id, common.TokenStatusEnabled).Count(&enabledTokens).Error)
	assert.Zero(t, enabledTokens, "enabling a tenant must not reactivate individually disabled tokens")

	rotateBody := []byte(`{"old_token_name":"primary","new_token_name":"rotated","token_quota":400}`)
	rotate := signedRequest(t, secret, http.MethodPost, "/api/novapay/tenant/tenant-a/token/rotate", strconvUnix(time.Now()), "rotatetokennoncevalue1", rotateBody, rotateBody)
	rotate.Header.Set("Idempotency-Key", "rotate-primary-token")
	rotateResponse := httptest.NewRecorder()
	router.ServeHTTP(rotateResponse, rotate)
	require.Equal(t, http.StatusOK, rotateResponse.Code, rotateResponse.Body.String())
	assert.Contains(t, rotateResponse.Body.String(), `"secret_visible":true`)
	assert.Contains(t, rotateResponse.Body.String(), `"token":"sk-`)

	rotateReplay := signedRequest(t, secret, http.MethodPost, "/api/novapay/tenant/tenant-a/token/rotate", strconvUnix(time.Now()), "rotatereplaynoncevalue1", rotateBody, rotateBody)
	rotateReplay.Header.Set("Idempotency-Key", "rotate-primary-token")
	rotateReplayResponse := httptest.NewRecorder()
	router.ServeHTTP(rotateReplayResponse, rotateReplay)
	require.Equal(t, http.StatusOK, rotateReplayResponse.Code, rotateReplayResponse.Body.String())
	assert.NotContains(t, rotateReplayResponse.Body.String(), `"token":"sk-`)
	assert.Contains(t, rotateReplayResponse.Body.String(), `"secret_visible":false`)

	tokenQuotaBody := []byte(`{"operation_id":"token-credit-1","delta":25,"reason":"test"}`)
	tokenQuota := signedRequest(t, secret, http.MethodPost, "/api/novapay/tenant/tenant-a/tokens/rotated/quota", strconvUnix(time.Now()), "tokenquotanoncevalue12", tokenQuotaBody, tokenQuotaBody)
	tokenQuota.Header.Set("Idempotency-Key", "token-credit-rotated")
	tokenQuotaResponse := httptest.NewRecorder()
	router.ServeHTTP(tokenQuotaResponse, tokenQuota)
	require.Equal(t, http.StatusOK, tokenQuotaResponse.Code, tokenQuotaResponse.Body.String())
	assert.Contains(t, tokenQuotaResponse.Body.String(), `"remain_quota":425`)

	tokenQuotaBizReplay := signedRequest(t, secret, http.MethodPost, "/api/novapay/tenant/tenant-a/tokens/rotated/quota", strconvUnix(time.Now()), "tokenquotabizreplayn12", tokenQuotaBody, tokenQuotaBody)
	tokenQuotaBizReplay.Header.Set("Idempotency-Key", "token-credit-rotated-new-key")
	tokenQuotaBizReplayResponse := httptest.NewRecorder()
	router.ServeHTTP(tokenQuotaBizReplayResponse, tokenQuotaBizReplay)
	require.Equal(t, http.StatusOK, tokenQuotaBizReplayResponse.Code, tokenQuotaBizReplayResponse.Body.String())
	assert.Contains(t, tokenQuotaBizReplayResponse.Body.String(), `"replayed":true`)
	assert.Contains(t, tokenQuotaBizReplayResponse.Body.String(), `"remain_quota":425`)

	deleteTokenRequest := signedRequest(t, secret, http.MethodDelete, "/api/novapay/tenant/tenant-a/tokens/rotated", strconvUnix(time.Now()), "deletetokennoncevalue1", nil, nil)
	deleteTokenRequest.Header.Set("Idempotency-Key", "delete-rotated-token")
	deleteTokenResponse := httptest.NewRecorder()
	router.ServeHTTP(deleteTokenResponse, deleteTokenRequest)
	require.Equal(t, http.StatusOK, deleteTokenResponse.Code, deleteTokenResponse.Body.String())

	deleteTenantRequest := signedRequest(t, secret, http.MethodDelete, "/api/novapay/tenant/tenant-a", strconvUnix(time.Now()), "deletetenantnoncevalue", nil, nil)
	deleteTenantRequest.Header.Set("Idempotency-Key", "delete-tenant-a")
	deleteTenantResponse := httptest.NewRecorder()
	router.ServeHTTP(deleteTenantResponse, deleteTenantRequest)
	require.Equal(t, http.StatusOK, deleteTenantResponse.Code, deleteTenantResponse.Body.String())

	reenableDeleted := signedRequest(t, secret, http.MethodPost, "/api/novapay/tenant/tenant-a/enable", strconvUnix(time.Now()), "reenabledeletednoncevalue", nil, nil)
	reenableDeleted.Header.Set("Idempotency-Key", "reenable-deleted-tenant")
	reenableDeletedResponse := httptest.NewRecorder()
	router.ServeHTTP(reenableDeletedResponse, reenableDeleted)
	assert.Equal(t, http.StatusConflict, reenableDeletedResponse.Code)
	assert.Contains(t, reenableDeletedResponse.Body.String(), "tenant_deleted")
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
	require.NoError(t, db.Create(&Tenant{TenantKey: "relay-tenant", UserID: user.Id, DisplayName: "Relay", Status: tenantStatusEnabled, CreatedAt: time.Now().Unix(), UpdatedAt: time.Now().Unix()}).Error)

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
	valid.Header.Set("X-Nova-Tenant-Key", "relay-tenant")
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
	failure.Header.Set("X-Nova-Tenant-Key", "relay-tenant")
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
	tenant := Tenant{TenantKey: "ledger-tenant", UserID: user.Id, DisplayName: "Ledger", Status: tenantStatusEnabled, CreatedAt: time.Now().Unix(), UpdatedAt: time.Now().Unix()}
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
		Attribution:      &service.UsageAttribution{Provider: "nova", Subject: tenant.TenantKey, RequestID: "nova-request-1", Verified: true},
	}
	require.NoError(t, recordFinalizedUsage(event))
	require.NoError(t, recordFinalizedUsage(event))

	var eventCount int64
	var outboxCount int64
	require.NoError(t, db.Model(&UsageEvent{}).Count(&eventCount).Error)
	require.NoError(t, db.Model(&Outbox{}).Count(&outboxCount).Error)
	assert.EqualValues(t, 1, eventCount)
	assert.EqualValues(t, 1, outboxCount)

	var outbox Outbox
	require.NoError(t, db.First(&outbox).Error)
	assert.Equal(t, "pending", outbox.Status)
	assert.Contains(t, outbox.Payload, `"event_type":"nova.usage.reported"`)
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
	tenant := Tenant{TenantKey: "task-tenant", UserID: user.Id, DisplayName: "Task", Status: tenantStatusEnabled, CreatedAt: time.Now().Unix(), UpdatedAt: time.Now().Unix()}
	require.NoError(t, db.Create(&tenant).Error)
	require.NoError(t, db.Create(&Attribution{SourceType: "task", SourceKey: "task-success", TenantID: tenant.ID, TenantKey: tenant.TenantKey, UserID: user.Id, TokenID: token.Id, NovaRequestID: "nova-task-request", CreatedAt: time.Now().Unix()}).Error)

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

	tenant := Tenant{TenantKey: "negative-tenant", UserID: user.Id, DisplayName: "Negative", Status: tenantStatusEnabled, CreatedAt: time.Now().Unix(), UpdatedAt: time.Now().Unix()}
	require.NoError(t, db.Create(&tenant).Error)
	require.NoError(t, recordFinalizedUsage(service.UsageLifecycleEvent{
		SourceType: "relay", SourceKey: "unverified-request", UserID: user.Id, TokenID: token.Id,
		Quota: 11, Attribution: &service.UsageAttribution{Provider: "nova", Subject: tenant.TenantKey, Verified: false},
	}))
	require.EqualError(t, recordFinalizedUsage(service.UsageLifecycleEvent{
		SourceType: "relay", SourceKey: "negative-quota", UserID: user.Id, TokenID: token.Id, Quota: -1,
		Attribution: &service.UsageAttribution{Provider: "nova", Subject: tenant.TenantKey, Verified: true},
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
