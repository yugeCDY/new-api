package nova

import (
	"crypto/sha256"
	"errors"
	"fmt"
	"math"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

const (
	tenantStatusEnabled  = "enabled"
	tenantStatusDisabled = "disabled"
	tenantStatusDeleted  = "deleted"
)

var (
	tenantKeyPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{2,63}$`)
	tokenNamePattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,49}$`)
)

type apiError struct {
	status  int
	code    string
	message string
}

func (e *apiError) Error() string { return e.message }

func newAPIError(status int, code, message string) error {
	return &apiError{status: status, code: code, message: message}
}

type createTenantRequest struct {
	TenantKey   string         `json:"tenant_key"`
	DisplayName string         `json:"display_name"`
	Quota       int            `json:"quota"`
	TokenName   string         `json:"token_name"`
	TokenQuota  int            `json:"token_quota"`
	Unlimited   bool           `json:"unlimited_quota"`
	Metadata    map[string]any `json:"metadata"`
}

type updateTenantRequest struct {
	DisplayName string         `json:"display_name"`
	Metadata    map[string]any `json:"metadata"`
}

type quotaAdjustmentRequest struct {
	OperationID string `json:"operation_id"`
	Delta       int    `json:"delta"`
	Reason      string `json:"reason"`
}

type rotateTokenRequest struct {
	OldTokenName string `json:"old_token_name"`
	NewTokenName string `json:"new_token_name"`
	TokenQuota   *int   `json:"token_quota,omitempty"`
	Unlimited    *bool  `json:"unlimited_quota,omitempty"`
}

type tenantResponse struct {
	TenantKey   string `json:"tenant_key"`
	DisplayName string `json:"display_name"`
	Status      string `json:"status"`
	UserID      int    `json:"user_id"`
	Quota       int    `json:"quota"`
	CreatedAt   int64  `json:"created_at"`
	UpdatedAt   int64  `json:"updated_at"`
}

type tokenResponse struct {
	Name           string `json:"name"`
	Token          string `json:"token,omitempty"`
	MaskedToken    string `json:"masked_token,omitempty"`
	Status         int    `json:"status"`
	RemainQuota    int    `json:"remain_quota"`
	UnlimitedQuota bool   `json:"unlimited_quota"`
	CreatedAt      int64  `json:"created_at"`
	SecretVisible  bool   `json:"secret_visible"`
}

func createTenant(c *gin.Context) {
	var request createTenantRequest
	if err := c.ShouldBindJSON(&request); err != nil {
		writeAPIError(c, newAPIError(http.StatusBadRequest, "invalid_request", "invalid request body"))
		return
	}
	request.TenantKey = strings.TrimSpace(request.TenantKey)
	request.DisplayName = strings.TrimSpace(request.DisplayName)
	request.TokenName = strings.TrimSpace(request.TokenName)
	if request.TokenName == "" {
		request.TokenName = "default"
	}
	if !tenantKeyPattern.MatchString(request.TenantKey) || !tokenNamePattern.MatchString(request.TokenName) || len(request.DisplayName) > 128 {
		writeAPIError(c, newAPIError(http.StatusBadRequest, "invalid_request", "tenant or token fields are invalid"))
		return
	}
	if err := validateQuota(request.Quota); err != nil {
		writeAPIError(c, err)
		return
	}
	if err := validateQuota(request.TokenQuota); err != nil {
		writeAPIError(c, err)
		return
	}

	metadata, err := common.Marshal(request.Metadata)
	if err != nil {
		writeAPIError(c, newAPIError(http.StatusBadRequest, "invalid_metadata", "metadata is invalid"))
		return
	}
	tokenKey, err := common.GenerateKey()
	if err != nil {
		writeAPIError(c, err)
		return
	}
	password, err := common.GenerateKey()
	if err != nil {
		writeAPIError(c, err)
		return
	}
	passwordHash, err := common.HashAccountPassword(password)
	if err != nil {
		writeAPIError(c, err)
		return
	}

	response, err := executeIdempotent(c, "create_tenant:"+request.TenantKey, func(tx *gorm.DB) (mutationResult, error) {
		now := time.Now().Unix()
		digest := sha256.Sum256([]byte(request.TenantKey))
		user := model.User{
			Username:    "nv_" + fmt.Sprintf("%x", digest[:8]),
			Password:    passwordHash,
			DisplayName: truncateString(request.DisplayName, 20),
			Role:        common.RoleCommonUser,
			Status:      common.UserStatusEnabled,
			Quota:       request.Quota,
			Group:       "default",
			AffCode:     "nv" + fmt.Sprintf("%x", digest[:15]),
			CreatedAt:   now,
			AuthVersion: 1,
		}
		if err := tx.Create(&user).Error; err != nil {
			return mutationResult{}, newAPIError(http.StatusConflict, "tenant_exists", "tenant already exists")
		}
		tenant := Tenant{
			TenantKey:   request.TenantKey,
			UserID:      user.Id,
			DisplayName: request.DisplayName,
			Status:      tenantStatusEnabled,
			Metadata:    string(metadata),
			CreatedAt:   now,
			UpdatedAt:   now,
		}
		if err := tx.Create(&tenant).Error; err != nil {
			return mutationResult{}, newAPIError(http.StatusConflict, "tenant_exists", "tenant already exists")
		}
		token := model.Token{
			UserId:         user.Id,
			Key:            tokenKey,
			Status:         common.TokenStatusEnabled,
			Name:           request.TokenName,
			CreatedTime:    now,
			AccessedTime:   now,
			ExpiredTime:    -1,
			RemainQuota:    request.TokenQuota,
			UnlimitedQuota: request.Unlimited,
			Group:          "default",
		}
		if err := tx.Create(&token).Error; err != nil {
			return mutationResult{}, err
		}

		fresh := gin.H{
			"success": true,
			"tenant":  toTenantResponse(tenant, user.Quota),
			"token":   toTokenResponse(token, "sk-"+tokenKey, true),
		}
		stored := gin.H{
			"success": true,
			"tenant":  toTenantResponse(tenant, user.Quota),
			"token":   toTokenResponse(token, "", false),
			"message": "token secret was returned only on the original response",
		}
		return mutationResult{FreshResponse: fresh, StoredResponse: stored, ResourceRef: request.TenantKey}, nil
	})
	writeIdempotentResponse(c, response, err)
}

func getTenant(c *gin.Context) {
	tenant, user, err := loadTenant(c.Param("tenant_key"))
	if err != nil {
		writeAPIError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"success": true, "tenant": toTenantResponse(*tenant, user.Quota)})
}

func listTenants(c *gin.Context) {
	page, pageSize, err := pagination(c)
	if err != nil {
		writeAPIError(c, err)
		return
	}
	_, db := currentState()
	var tenants []Tenant
	var total int64
	query := db.Model(&Tenant{})
	if err := query.Count(&total).Error; err != nil {
		writeAPIError(c, err)
		return
	}
	if err := query.Order("id DESC").Limit(pageSize).Offset((page - 1) * pageSize).Find(&tenants).Error; err != nil {
		writeAPIError(c, err)
		return
	}
	items := make([]tenantResponse, 0, len(tenants))
	for _, tenant := range tenants {
		var user model.User
		if err := db.Select("id", "quota").First(&user, tenant.UserID).Error; err != nil {
			writeAPIError(c, err)
			return
		}
		items = append(items, toTenantResponse(tenant, user.Quota))
	}
	c.JSON(http.StatusOK, gin.H{"success": true, "data": items, "page": page, "page_size": pageSize, "total": total})
}

func updateTenant(c *gin.Context) {
	var request updateTenantRequest
	if err := c.ShouldBindJSON(&request); err != nil || len(request.DisplayName) > 128 {
		writeAPIError(c, newAPIError(http.StatusBadRequest, "invalid_request", "invalid request body"))
		return
	}
	metadata, err := common.Marshal(request.Metadata)
	if err != nil {
		writeAPIError(c, newAPIError(http.StatusBadRequest, "invalid_metadata", "metadata is invalid"))
		return
	}
	tenantKey := c.Param("tenant_key")
	response, err := executeIdempotent(c, "update_tenant:"+tenantKey, func(tx *gorm.DB) (mutationResult, error) {
		tenant, err := lockTenant(tx, tenantKey)
		if err != nil {
			return mutationResult{}, err
		}
		if err := ensureTenantMutable(tenant); err != nil {
			return mutationResult{}, err
		}
		now := time.Now().Unix()
		if err := tx.Model(&tenant).Updates(map[string]any{"display_name": strings.TrimSpace(request.DisplayName), "metadata": string(metadata), "updated_at": now}).Error; err != nil {
			return mutationResult{}, err
		}
		tenant.DisplayName = strings.TrimSpace(request.DisplayName)
		tenant.UpdatedAt = now
		var user model.User
		if err := tx.Select("id", "quota").First(&user, tenant.UserID).Error; err != nil {
			return mutationResult{}, err
		}
		body := gin.H{"success": true, "tenant": toTenantResponse(tenant, user.Quota)}
		return mutationResult{FreshResponse: body, StoredResponse: body, ResourceRef: tenantKey}, nil
	})
	writeIdempotentResponse(c, response, err)
}

func setTenantStatus(c *gin.Context, status string) {
	tenantKey := c.Param("tenant_key")
	response, err := executeIdempotent(c, "tenant_status:"+status+":"+tenantKey, func(tx *gorm.DB) (mutationResult, error) {
		tenant, err := lockTenant(tx, tenantKey)
		if err != nil {
			return mutationResult{}, err
		}
		if tenant.Status == tenantStatusDeleted && status != tenantStatusDeleted {
			return mutationResult{}, newAPIError(http.StatusConflict, "tenant_deleted", "deleted tenant cannot be re-enabled or modified")
		}
		userStatus := common.UserStatusEnabled
		if status != tenantStatusEnabled {
			userStatus = common.UserStatusDisabled
		}
		if _, err := model.IncrementUserAuthVersionWithTx(tx, tenant.UserID); err != nil {
			return mutationResult{}, err
		}
		if err := tx.Model(&model.User{}).Where("id = ?", tenant.UserID).Update("status", userStatus).Error; err != nil {
			return mutationResult{}, err
		}
		if status != tenantStatusEnabled {
			if err := tx.Model(&model.Token{}).Where("user_id = ?", tenant.UserID).Update("status", common.TokenStatusDisabled).Error; err != nil {
				return mutationResult{}, err
			}
		}
		now := time.Now().Unix()
		if err := tx.Model(&tenant).Updates(map[string]any{"status": status, "updated_at": now}).Error; err != nil {
			return mutationResult{}, err
		}
		tenant.Status = status
		tenant.UpdatedAt = now
		body := gin.H{"success": true, "tenant_key": tenantKey, "status": status}
		return mutationResult{
			FreshResponse:  body,
			StoredResponse: body,
			ResourceRef:    tenantKey,
			AfterCommit: func() {
				_ = model.InvalidateUserCache(tenant.UserID)
				_ = model.InvalidateUserTokensCache(tenant.UserID)
			},
		}, nil
	})
	writeIdempotentResponse(c, response, err)
}

func deleteTenant(c *gin.Context) {
	setTenantStatus(c, tenantStatusDeleted)
}

func adjustTenantQuota(c *gin.Context) {
	var request quotaAdjustmentRequest
	if err := c.ShouldBindJSON(&request); err != nil || !validOperationID(request.OperationID) || strings.TrimSpace(request.Reason) == "" || len(request.Reason) > 255 {
		writeAPIError(c, newAPIError(http.StatusBadRequest, "invalid_request", "invalid quota adjustment"))
		return
	}
	if err := validateQuotaDelta(request.Delta); err != nil {
		writeAPIError(c, err)
		return
	}
	tenantKey := c.Param("tenant_key")
	response, err := executeIdempotent(c, "tenant_quota:"+tenantKey, func(tx *gorm.DB) (mutationResult, error) {
		tenant, err := lockTenant(tx, tenantKey)
		if err != nil {
			return mutationResult{}, err
		}
		if err := ensureTenantMutable(tenant); err != nil {
			return mutationResult{}, err
		}
		if existing, replay, err := loadQuotaOperation(tx, tenantKey, quotaTargetTenant, "", request); err != nil {
			return mutationResult{}, err
		} else if replay {
			body := gin.H{"success": true, "tenant_key": tenantKey, "quota": existing.ResultQuota, "operation_id": request.OperationID, "replayed": true}
			return mutationResult{FreshResponse: body, StoredResponse: body, ResourceRef: tenantKey}, nil
		}

		query := tx.Model(&model.User{}).Where("id = ?", tenant.UserID)
		if request.Delta > 0 {
			query = query.Where("quota <= ?", common.MaxWalletQuota-request.Delta)
		} else {
			query = query.Where("quota >= ?", -request.Delta)
		}
		result := query.Update("quota", gorm.Expr("quota + ?", request.Delta))
		if result.Error != nil {
			return mutationResult{}, result.Error
		}
		if result.RowsAffected != 1 {
			return mutationResult{}, newAPIError(http.StatusConflict, "quota_out_of_range", "quota adjustment would exceed allowed bounds")
		}
		var user model.User
		if err := tx.Select("id", "quota").First(&user, tenant.UserID).Error; err != nil {
			return mutationResult{}, err
		}
		if err := createQuotaOperation(tx, tenantKey, quotaTargetTenant, "", request, user.Quota); err != nil {
			return mutationResult{}, err
		}
		body := gin.H{"success": true, "tenant_key": tenantKey, "quota": user.Quota, "operation_id": request.OperationID}
		return mutationResult{FreshResponse: body, StoredResponse: body, ResourceRef: tenantKey, AfterCommit: func() { _ = model.InvalidateUserCache(tenant.UserID) }}, nil
	})
	writeIdempotentResponse(c, response, err)
}

func listTokens(c *gin.Context) {
	tenant, _, err := loadTenant(c.Param("tenant_key"))
	if err != nil {
		writeAPIError(c, err)
		return
	}
	_, db := currentState()
	var tokens []model.Token
	if err := db.Where("user_id = ?", tenant.UserID).Order("id DESC").Find(&tokens).Error; err != nil {
		writeAPIError(c, err)
		return
	}
	items := make([]tokenResponse, 0, len(tokens))
	for _, token := range tokens {
		items = append(items, toTokenResponse(token, "", false))
	}
	c.JSON(http.StatusOK, gin.H{"success": true, "data": items})
}

func rotateToken(c *gin.Context) {
	var request rotateTokenRequest
	if err := c.ShouldBindJSON(&request); err != nil || !tokenNamePattern.MatchString(request.OldTokenName) || !tokenNamePattern.MatchString(request.NewTokenName) {
		writeAPIError(c, newAPIError(http.StatusBadRequest, "invalid_request", "invalid token rotation request"))
		return
	}
	if request.TokenQuota != nil {
		if err := validateQuota(*request.TokenQuota); err != nil {
			writeAPIError(c, err)
			return
		}
	}
	newKey, err := common.GenerateKey()
	if err != nil {
		writeAPIError(c, err)
		return
	}
	tenantKey := c.Param("tenant_key")
	response, err := executeIdempotent(c, "rotate_token:"+tenantKey+":"+request.OldTokenName, func(tx *gorm.DB) (mutationResult, error) {
		tenant, err := lockTenant(tx, tenantKey)
		if err != nil {
			return mutationResult{}, err
		}
		if err := ensureTenantMutable(tenant); err != nil {
			return mutationResult{}, err
		}
		var oldToken model.Token
		if err := tx.Where("user_id = ? AND name = ?", tenant.UserID, request.OldTokenName).First(&oldToken).Error; err != nil {
			return mutationResult{}, newAPIError(http.StatusNotFound, "token_not_found", "token not found")
		}
		var duplicateCount int64
		if err := tx.Model(&model.Token{}).Where("user_id = ? AND name = ?", tenant.UserID, request.NewTokenName).Count(&duplicateCount).Error; err != nil {
			return mutationResult{}, err
		}
		if duplicateCount > 0 {
			return mutationResult{}, newAPIError(http.StatusConflict, "token_exists", "new token name already exists")
		}
		quota := oldToken.RemainQuota
		if request.TokenQuota != nil {
			quota = *request.TokenQuota
		}
		unlimited := oldToken.UnlimitedQuota
		if request.Unlimited != nil {
			unlimited = *request.Unlimited
		}
		now := time.Now().Unix()
		newToken := model.Token{UserId: tenant.UserID, Key: newKey, Status: common.TokenStatusEnabled, Name: request.NewTokenName, CreatedTime: now, AccessedTime: now, ExpiredTime: -1, RemainQuota: quota, UnlimitedQuota: unlimited, Group: oldToken.Group}
		if err := tx.Create(&newToken).Error; err != nil {
			return mutationResult{}, newAPIError(http.StatusConflict, "token_exists", "new token name already exists")
		}
		if err := tx.Model(&oldToken).Update("status", common.TokenStatusDisabled).Error; err != nil {
			return mutationResult{}, err
		}
		fresh := gin.H{"success": true, "token": toTokenResponse(newToken, "sk-"+newKey, true)}
		stored := gin.H{"success": true, "token": toTokenResponse(newToken, "", false), "message": "token secret was returned only on the original response"}
		return mutationResult{FreshResponse: fresh, StoredResponse: stored, ResourceRef: request.NewTokenName, AfterCommit: func() { _ = model.InvalidateUserTokensCache(tenant.UserID) }}, nil
	})
	writeIdempotentResponse(c, response, err)
}

func adjustTokenQuota(c *gin.Context) {
	var request quotaAdjustmentRequest
	if err := c.ShouldBindJSON(&request); err != nil || !validOperationID(request.OperationID) || strings.TrimSpace(request.Reason) == "" || len(request.Reason) > 255 || !tokenNamePattern.MatchString(c.Param("token_name")) {
		writeAPIError(c, newAPIError(http.StatusBadRequest, "invalid_request", "invalid quota adjustment"))
		return
	}
	if err := validateQuotaDelta(request.Delta); err != nil {
		writeAPIError(c, err)
		return
	}
	tenantKey := c.Param("tenant_key")
	tokenName := c.Param("token_name")
	response, err := executeIdempotent(c, "token_quota:"+tenantKey+":"+tokenName, func(tx *gorm.DB) (mutationResult, error) {
		tenant, err := lockTenant(tx, tenantKey)
		if err != nil {
			return mutationResult{}, err
		}
		if err := ensureTenantMutable(tenant); err != nil {
			return mutationResult{}, err
		}
		if existing, replay, err := loadQuotaOperation(tx, tenantKey, quotaTargetToken, tokenName, request); err != nil {
			return mutationResult{}, err
		} else if replay {
			var token model.Token
			if err := tx.Where("user_id = ? AND name = ?", tenant.UserID, tokenName).First(&token).Error; err != nil {
				return mutationResult{}, err
			}
			token.RemainQuota = existing.ResultQuota
			body := gin.H{"success": true, "tenant_key": tenantKey, "token": toTokenResponse(token, "", false), "operation_id": request.OperationID, "replayed": true}
			return mutationResult{FreshResponse: body, StoredResponse: body, ResourceRef: tokenName}, nil
		}

		query := tx.Model(&model.Token{}).Where("user_id = ? AND name = ?", tenant.UserID, tokenName)
		if request.Delta > 0 {
			query = query.Where("remain_quota <= ?", common.MaxWalletQuota-request.Delta)
		} else {
			query = query.Where("remain_quota >= ?", -request.Delta)
		}
		result := query.Update("remain_quota", gorm.Expr("remain_quota + ?", request.Delta))
		if result.Error != nil {
			return mutationResult{}, result.Error
		}
		if result.RowsAffected != 1 {
			return mutationResult{}, newAPIError(http.StatusConflict, "quota_out_of_range", "quota adjustment would exceed allowed bounds")
		}
		var token model.Token
		if err := tx.Where("user_id = ? AND name = ?", tenant.UserID, tokenName).First(&token).Error; err != nil {
			return mutationResult{}, err
		}
		if err := createQuotaOperation(tx, tenantKey, quotaTargetToken, tokenName, request, token.RemainQuota); err != nil {
			return mutationResult{}, err
		}
		body := gin.H{"success": true, "tenant_key": tenantKey, "token": toTokenResponse(token, "", false), "operation_id": request.OperationID}
		return mutationResult{FreshResponse: body, StoredResponse: body, ResourceRef: tokenName, AfterCommit: func() { _ = model.InvalidateUserTokensCache(tenant.UserID) }}, nil
	})
	writeIdempotentResponse(c, response, err)
}

func deleteToken(c *gin.Context) {
	tenantKey := c.Param("tenant_key")
	tokenName := c.Param("token_name")
	response, err := executeIdempotent(c, "delete_token:"+tenantKey+":"+tokenName, func(tx *gorm.DB) (mutationResult, error) {
		tenant, err := lockTenant(tx, tenantKey)
		if err != nil {
			return mutationResult{}, err
		}
		if err := ensureTenantMutable(tenant); err != nil {
			return mutationResult{}, err
		}
		result := tx.Model(&model.Token{}).Where("user_id = ? AND name = ?", tenant.UserID, tokenName).Update("status", common.TokenStatusDisabled)
		if result.Error != nil {
			return mutationResult{}, result.Error
		}
		if result.RowsAffected != 1 {
			return mutationResult{}, newAPIError(http.StatusNotFound, "token_not_found", "token not found")
		}
		body := gin.H{"success": true, "tenant_key": tenantKey, "token_name": tokenName, "status": common.TokenStatusDisabled}
		return mutationResult{FreshResponse: body, StoredResponse: body, ResourceRef: tokenName, AfterCommit: func() { _ = model.InvalidateUserTokensCache(tenant.UserID) }}, nil
	})
	writeIdempotentResponse(c, response, err)
}

func listModels(c *gin.Context) {
	c.JSON(http.StatusOK, gin.H{"success": true, "data": model.GetPricing()})
}

func listUsageLogs(c *gin.Context) {
	page, pageSize, err := pagination(c)
	if err != nil {
		writeAPIError(c, err)
		return
	}
	tenant, _, err := loadTenant(c.Param("tenant_key"))
	if err != nil {
		writeAPIError(c, err)
		return
	}
	_, db := currentState()
	query := db.Model(&UsageEvent{}).Where("tenant_id = ?", tenant.ID)
	var total int64
	if err := query.Count(&total).Error; err != nil {
		writeAPIError(c, err)
		return
	}
	var events []UsageEvent
	if err := query.Order("id DESC").Limit(pageSize).Offset((page - 1) * pageSize).Find(&events).Error; err != nil {
		writeAPIError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"success": true, "data": events, "page": page, "page_size": pageSize, "total": total})
}

func loadTenant(tenantKey string) (*Tenant, *model.User, error) {
	if !tenantKeyPattern.MatchString(tenantKey) {
		return nil, nil, newAPIError(http.StatusNotFound, "tenant_not_found", "tenant not found")
	}
	_, db := currentState()
	var tenant Tenant
	if err := db.Where("tenant_key = ?", tenantKey).First(&tenant).Error; err != nil {
		return nil, nil, tenantLookupError(err)
	}
	var user model.User
	if err := db.Select("id", "quota").First(&user, tenant.UserID).Error; err != nil {
		return nil, nil, err
	}
	return &tenant, &user, nil
}

func tenantLookupError(err error) error {
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return newAPIError(http.StatusNotFound, "tenant_not_found", "tenant not found")
	}
	return err
}

func lockTenant(tx *gorm.DB, tenantKey string) (Tenant, error) {
	query := tx
	if tx.Dialector.Name() != "sqlite" {
		query = query.Clauses(clause.Locking{Strength: "UPDATE"})
	}
	var tenant Tenant
	if err := query.Where("tenant_key = ?", tenantKey).First(&tenant).Error; err != nil {
		return Tenant{}, tenantLookupError(err)
	}
	return tenant, nil
}

func ensureTenantMutable(tenant Tenant) error {
	if tenant.Status == tenantStatusDeleted {
		return newAPIError(http.StatusConflict, "tenant_deleted", "deleted tenant cannot be modified")
	}
	return nil
}

func validateQuota(quota int) error {
	if quota < 0 || common.ValidateWalletQuota(quota) != nil {
		return newAPIError(http.StatusBadRequest, "invalid_quota", "quota is outside the allowed range")
	}
	return nil
}

func validateQuotaDelta(delta int) error {
	if delta == math.MinInt || delta > common.MaxWalletQuota || delta < -common.MaxWalletQuota {
		return newAPIError(http.StatusBadRequest, "invalid_quota", "quota delta is outside the allowed range")
	}
	return nil
}

func loadQuotaOperation(tx *gorm.DB, tenantKey, targetType, targetRef string, request quotaAdjustmentRequest) (*QuotaOperation, bool, error) {
	var existing QuotaOperation
	err := tx.Where(
		"tenant_key = ? AND target_type = ? AND target_ref = ? AND operation_id = ?",
		tenantKey, targetType, targetRef, request.OperationID,
	).First(&existing).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	if existing.Delta != request.Delta || existing.Reason != strings.TrimSpace(request.Reason) {
		return nil, false, newAPIError(http.StatusConflict, "operation_conflict", "operation_id was already used with a different adjustment")
	}
	return &existing, true, nil
}

func createQuotaOperation(tx *gorm.DB, tenantKey, targetType, targetRef string, request quotaAdjustmentRequest, resultQuota int) error {
	op := QuotaOperation{
		TenantKey:   tenantKey,
		TargetType:  targetType,
		TargetRef:   targetRef,
		OperationID: request.OperationID,
		Delta:       request.Delta,
		Reason:      strings.TrimSpace(request.Reason),
		ResultQuota: resultQuota,
		CreatedAt:   time.Now().Unix(),
	}
	if err := tx.Create(&op).Error; err != nil {
		return newAPIError(http.StatusConflict, "operation_conflict", "operation_id was already used")
	}
	return nil
}

func validOperationID(value string) bool {
	if len(value) == 0 || len(value) > 128 {
		return false
	}
	for _, char := range value {
		if char >= 'a' && char <= 'z' || char >= 'A' && char <= 'Z' || char >= '0' && char <= '9' || char == '.' || char == '_' || char == ':' || char == '-' {
			continue
		}
		return false
	}
	return true
}

func pagination(c *gin.Context) (int, int, error) {
	page, err := strconv.Atoi(c.DefaultQuery("page", "1"))
	if err != nil || page < 1 {
		return 0, 0, newAPIError(http.StatusBadRequest, "invalid_pagination", "invalid pagination")
	}
	pageSize, err := strconv.Atoi(c.DefaultQuery("page_size", "20"))
	if err != nil || pageSize < 1 || pageSize > 100 {
		return 0, 0, newAPIError(http.StatusBadRequest, "invalid_pagination", "invalid pagination")
	}
	return page, pageSize, nil
}

func toTenantResponse(tenant Tenant, quota int) tenantResponse {
	return tenantResponse{TenantKey: tenant.TenantKey, DisplayName: tenant.DisplayName, Status: tenant.Status, UserID: tenant.UserID, Quota: quota, CreatedAt: tenant.CreatedAt, UpdatedAt: tenant.UpdatedAt}
}

func toTokenResponse(token model.Token, secret string, visible bool) tokenResponse {
	return tokenResponse{Name: token.Name, Token: secret, MaskedToken: model.MaskTokenKey(token.Key), Status: token.Status, RemainQuota: token.RemainQuota, UnlimitedQuota: token.UnlimitedQuota, CreatedAt: token.CreatedTime, SecretVisible: visible}
}

func truncateString(value string, maximum int) string {
	runes := []rune(value)
	if len(runes) <= maximum {
		return value
	}
	return string(runes[:maximum])
}

func writeIdempotentResponse(c *gin.Context, response idempotentResponse, err error) {
	if err != nil {
		writeAPIError(c, err)
		return
	}
	c.Header("Content-Type", "application/json; charset=utf-8")
	c.Header("Cache-Control", "no-store")
	if response.Replayed {
		c.Header("Idempotency-Replayed", "true")
	}
	c.Data(response.Status, "application/json; charset=utf-8", response.Body)
}

func writeAPIError(c *gin.Context, err error) {
	var failure *apiError
	if errors.As(err, &failure) {
		c.JSON(failure.status, failureBody(c, failure.code, failure.message))
		return
	}
	if errors.Is(err, errIdempotencyConflict) {
		c.JSON(http.StatusConflict, failureBody(c, "idempotency_conflict", errIdempotencyConflict.Error()))
		return
	}
	if errors.Is(err, errIdempotencyRunning) {
		c.JSON(http.StatusConflict, failureBody(c, "operation_in_progress", errIdempotencyRunning.Error()))
		return
	}
	c.JSON(http.StatusInternalServerError, failureBody(c, "internal_error", "internal server error"))
}

func failureBody(c *gin.Context, code, message string) gin.H {
	body := gin.H{"success": false, "code": code, "message": message}
	if requestID := c.GetString(common.RequestIdKey); requestID != "" {
		body["request_id"] = requestID
	}
	return body
}
