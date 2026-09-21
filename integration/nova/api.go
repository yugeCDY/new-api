package nova

import (
	"crypto/sha256"
	"errors"
	"fmt"
	"math"
	"net/http"
	"regexp"
	"slices"
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
	// Align with users.username max length (20).
	tenantKeyPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{2,19}$`)
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
	TenantKey    string `json:"tenant_key"`
	TenantName   string `json:"tenant_name"`
	InitialQuota int    `json:"initial_quota"`
	RequestID    string `json:"request_id"`
}

type updateTenantRequest struct {
	RequestID   string `json:"request_id"`
	DisplayName string `json:"display_name"`
}

type quotaAdjustmentRequest struct {
	RequestID     string `json:"request_id"`
	OrderNo       string `json:"order_no"`
	DeltaQuota    *int   `json:"delta_quota"`
	AbsoluteQuota *int   `json:"absolute_quota"`
	Reason        string `json:"reason"`
}

type tokenQuotaSetRequest struct {
	RequestID      string `json:"request_id"`
	RemainQuota    *int   `json:"remain_quota"`
	UnlimitedQuota *bool  `json:"unlimited_quota"`
	Reason         string `json:"reason"`
}

type rotateTokenRequest struct {
	RequestID string `json:"request_id"`
	Reason    string `json:"reason"`
}

type requestIDBody struct {
	RequestID string `json:"request_id"`
}

type tenantResponse struct {
	UserID       int    `json:"user_id"`
	Username     string `json:"username"`
	DisplayName  string `json:"display_name"`
	Status       string `json:"status"`
	Quota        int    `json:"quota"`
	UsedQuota    int    `json:"used_quota"`
	TokenName    string `json:"token_name"`
	TokenKey     string `json:"token_key"`
	CreatedAt    string `json:"created_at"`
	LastActiveAt string `json:"last_active_at"`
}

type tenantListItem struct {
	Username     string `json:"username"`
	UserID       int    `json:"user_id"`
	DisplayName  string `json:"display_name"`
	Status       string `json:"status"`
	Quota        int    `json:"quota"`
	UsedQuota    int    `json:"used_quota"`
	CreatedAt    int64  `json:"created_at"`
	LastActiveAt int64  `json:"last_active_at"`
}

type tenantKeyItem struct {
	TokenID        int    `json:"token_id"`
	TokenName      string `json:"token_name"`
	Key            string `json:"key"`
	Status         string `json:"status"`
	ExpiredTime    int64  `json:"expired_time"`
	RemainQuota    int    `json:"remain_quota"`
	UnlimitedQuota bool   `json:"unlimited_quota"`
	UsedQuota      int    `json:"used_quota"`
	CreatedTime    int64  `json:"created_time"`
}

func createTenant(c *gin.Context) {
	var request createTenantRequest
	if err := c.ShouldBindJSON(&request); err != nil {
		writeAPIError(c, newAPIError(http.StatusBadRequest, "invalid_request", "invalid request body"))
		return
	}
	request.TenantKey = strings.TrimSpace(request.TenantKey)
	request.TenantName = strings.TrimSpace(request.TenantName)
	request.RequestID = strings.TrimSpace(request.RequestID)
	if !tenantKeyPattern.MatchString(request.TenantKey) || request.TenantName == "" || len(request.TenantName) > 20 {
		writeAPIError(c, newAPIError(http.StatusBadRequest, "invalid_request", "tenant fields are invalid"))
		return
	}
	if !requestIDPattern.MatchString(request.RequestID) {
		writeAPIError(c, newAPIError(http.StatusBadRequest, "invalid_request", "request_id is invalid"))
		return
	}
	if err := validateQuota(request.InitialQuota); err != nil {
		writeAPIError(c, err)
		return
	}

	tokenName := primaryTokenName(request.TenantKey)
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

	response, err := executeIdempotent(c, "create_tenant", request.RequestID, func(tx *gorm.DB) (mutationResult, error) {
		now := time.Now().Unix()
		digest := sha256.Sum256([]byte(request.TenantKey))
		user := model.User{
			Username:    request.TenantKey,
			Password:    passwordHash,
			DisplayName: request.TenantName,
			Role:        common.RoleCommonUser,
			Status:      common.UserStatusEnabled,
			Quota:       request.InitialQuota,
			Group:       "default",
			AffCode:     "nv" + fmt.Sprintf("%x", digest[:15]),
			CreatedAt:   now,
			AuthVersion: 1,
		}
		if err := tx.Create(&user).Error; err != nil {
			return mutationResult{}, newAPIError(http.StatusConflict, "tenant_exists", "tenant already exists")
		}
		tenant := Tenant{
			UserID:    user.Id,
			CreatedAt: now,
			UpdatedAt: now,
		}
		if err := tx.Create(&tenant).Error; err != nil {
			return mutationResult{}, newAPIError(http.StatusConflict, "tenant_exists", "tenant already exists")
		}
		token := model.Token{
			UserId:         user.Id,
			Key:            tokenKey,
			Status:         common.TokenStatusEnabled,
			Name:           tokenName,
			CreatedTime:    now,
			AccessedTime:   now,
			ExpiredTime:    -1,
			RemainQuota:    request.InitialQuota,
			UnlimitedQuota: false,
			Group:          "default",
		}
		if err := tx.Create(&token).Error; err != nil {
			return mutationResult{}, err
		}

		body := successBody(gin.H{
			"user_id":    user.Id,
			"username":   user.Username,
			"token_name": token.Name,
			"token_key":  "sk-" + tokenKey,
			"quota":      user.Quota,
		})
		return mutationResult{FreshResponse: body, StoredResponse: body, ResourceRef: request.TenantKey}, nil
	})
	writeIdempotentResponse(c, response, err)
}

func getTenant(c *gin.Context) {
	tenant, user, err := loadTenant(c.Param("tenant_key"))
	if err != nil {
		writeAPIError(c, err)
		return
	}
	detail, err := buildTenantDetail(*tenant, *user)
	if err != nil {
		writeAPIError(c, err)
		return
	}
	c.JSON(http.StatusOK, successBody(detail))
}

func listTenants(c *gin.Context) {
	page, pageSize, err := pagination(c)
	if err != nil {
		writeAPIError(c, err)
		return
	}
	status := strings.TrimSpace(c.Query("status"))
	if status != "" && status != tenantStatusEnabled && status != tenantStatusDisabled && status != tenantStatusDeleted {
		writeAPIError(c, newAPIError(http.StatusBadRequest, "invalid_request", "invalid status"))
		return
	}
	keyword := strings.ToLower(strings.TrimSpace(c.Query("keyword")))
	_, db := currentState()
	filtered := db.Table("nova_tenants AS t").Joins("INNER JOIN users AS u ON u.id = t.user_id")
	switch status {
	case tenantStatusDeleted:
		filtered = filtered.Where("u.deleted_at IS NOT NULL")
	case tenantStatusEnabled:
		filtered = filtered.Where("u.deleted_at IS NULL AND u.status = ?", common.UserStatusEnabled)
	case tenantStatusDisabled:
		filtered = filtered.Where("u.deleted_at IS NULL AND u.status = ?", common.UserStatusDisabled)
	}
	if keyword != "" {
		pattern := "%" + keyword + "%"
		filtered = filtered.Where("LOWER(u.username) LIKE ? OR LOWER(u.display_name) LIKE ?", pattern, pattern)
	}

	var total int64
	if err := filtered.Count(&total).Error; err != nil {
		writeAPIError(c, err)
		return
	}
	var rows []struct {
		TenantID    int64
		UserID      int
		Username    string
		DisplayName string
		UserStatus  int
		DeletedAt   gorm.DeletedAt
		Quota       int
		UsedQuota   int
		LastLoginAt int64
		CreatedAt   int64
	}
	if err := filtered.Select("t.id AS tenant_id, t.user_id, u.username, u.display_name, u.status AS user_status, u.deleted_at, u.quota, u.used_quota, u.last_login_at, t.created_at").
		Order("t.id DESC").Limit(pageSize).Offset((page - 1) * pageSize).Scan(&rows).Error; err != nil {
		writeAPIError(c, err)
		return
	}

	tenantIDs := make([]int64, 0, len(rows))
	for _, row := range rows {
		tenantIDs = append(tenantIDs, row.TenantID)
	}
	lastUsage := map[int64]int64{}
	if len(tenantIDs) > 0 {
		var usageRows []struct {
			TenantID int64 `gorm:"column:tenant_id"`
			LastAt   int64 `gorm:"column:last_at"`
		}
		if err := db.Model(&UsageEvent{}).Select("tenant_id, MAX(occurred_at) AS last_at").Where("tenant_id IN ?", tenantIDs).Group("tenant_id").Scan(&usageRows).Error; err != nil {
			writeAPIError(c, err)
			return
		}
		for _, row := range usageRows {
			lastUsage[row.TenantID] = row.LastAt
		}
	}

	items := make([]tenantListItem, 0, len(rows))
	for _, row := range rows {
		user := model.User{Status: row.UserStatus, DeletedAt: row.DeletedAt, LastLoginAt: row.LastLoginAt}
		lastActive := row.LastLoginAt
		if lastUsage[row.TenantID] > lastActive {
			lastActive = lastUsage[row.TenantID]
		}
		items = append(items, tenantListItem{
			Username:     row.Username,
			UserID:       row.UserID,
			DisplayName:  row.DisplayName,
			Status:       tenantStatusFromUser(user),
			Quota:        row.Quota,
			UsedQuota:    row.UsedQuota,
			CreatedAt:    row.CreatedAt,
			LastActiveAt: lastActive,
		})
	}
	c.JSON(http.StatusOK, successBody(gin.H{
		"total":     total,
		"page":      page,
		"page_size": pageSize,
		"items":     items,
	}))
}

func updateTenant(c *gin.Context) {
	var request updateTenantRequest
	if err := c.ShouldBindJSON(&request); err != nil {
		writeAPIError(c, newAPIError(http.StatusBadRequest, "invalid_request", "invalid request body"))
		return
	}
	displayName := strings.TrimSpace(request.DisplayName)
	if displayName == "" || len(displayName) > 20 {
		writeAPIError(c, newAPIError(http.StatusBadRequest, "invalid_request", "display_name is invalid"))
		return
	}
	if !requestIDPattern.MatchString(strings.TrimSpace(request.RequestID)) {
		writeAPIError(c, newAPIError(http.StatusBadRequest, "invalid_request", "request_id is invalid"))
		return
	}
	tenantKey := c.Param("tenant_key")
	response, err := executeIdempotent(c, "update_tenant:"+tenantKey, strings.TrimSpace(request.RequestID), func(tx *gorm.DB) (mutationResult, error) {
		tenant, user, err := lockTenant(tx, tenantKey)
		if err != nil {
			return mutationResult{}, err
		}
		if err := ensureTenantMutable(user); err != nil {
			return mutationResult{}, err
		}
		now := time.Now().Unix()
		if err := tx.Model(&model.User{}).Where("id = ?", tenant.UserID).Updates(map[string]any{
			"display_name": displayName,
		}).Error; err != nil {
			return mutationResult{}, err
		}
		if err := tx.Model(&tenant).Update("updated_at", now).Error; err != nil {
			return mutationResult{}, err
		}
		tenant.UpdatedAt = now
		if err := tx.Unscoped().Select("id", "username", "display_name", "status", "quota", "used_quota", "last_login_at", "deleted_at").First(&user, tenant.UserID).Error; err != nil {
			return mutationResult{}, err
		}
		detail, err := buildTenantDetail(tenant, user)
		if err != nil {
			return mutationResult{}, err
		}
		body := successBody(detail)
		return mutationResult{
			FreshResponse:  body,
			StoredResponse: body,
			ResourceRef:    tenantKey,
			AfterCommit:    func() { _ = model.InvalidateUserCache(tenant.UserID) },
		}, nil
	})
	writeIdempotentResponse(c, response, err)
}

func setTenantStatus(c *gin.Context, status string) {
	var request requestIDBody
	if err := c.ShouldBindJSON(&request); err != nil {
		writeAPIError(c, newAPIError(http.StatusBadRequest, "invalid_request", "request_id is required"))
		return
	}
	requestID := strings.TrimSpace(request.RequestID)
	if !requestIDPattern.MatchString(requestID) {
		writeAPIError(c, newAPIError(http.StatusBadRequest, "invalid_request", "request_id is invalid"))
		return
	}
	tenantKey := c.Param("tenant_key")
	response, err := executeIdempotent(c, "tenant_status:"+status+":"+tenantKey, requestID, func(tx *gorm.DB) (mutationResult, error) {
		tenant, user, err := lockTenant(tx, tenantKey)
		if err != nil {
			return mutationResult{}, err
		}
		if user.DeletedAt.Valid && status != tenantStatusDeleted {
			return mutationResult{}, newAPIError(http.StatusConflict, "tenant_deleted", "deleted tenant cannot be re-enabled or modified")
		}
		if _, err := model.IncrementUserAuthVersionWithTx(tx, tenant.UserID); err != nil {
			return mutationResult{}, err
		}
		now := time.Now().Unix()
		switch status {
		case tenantStatusEnabled:
			if err := tx.Unscoped().Model(&model.User{}).Where("id = ?", tenant.UserID).Updates(map[string]any{
				"status":     common.UserStatusEnabled,
				"deleted_at": nil,
			}).Error; err != nil {
				return mutationResult{}, err
			}
		case tenantStatusDisabled:
			if err := tx.Model(&model.User{}).Where("id = ?", tenant.UserID).Update("status", common.UserStatusDisabled).Error; err != nil {
				return mutationResult{}, err
			}
			if err := tx.Model(&model.Token{}).Where("user_id = ?", tenant.UserID).Update("status", common.TokenStatusDisabled).Error; err != nil {
				return mutationResult{}, err
			}
		case tenantStatusDeleted:
			if err := tx.Model(&model.User{}).Where("id = ?", tenant.UserID).Update("status", common.UserStatusDisabled).Error; err != nil {
				return mutationResult{}, err
			}
			if err := tx.Model(&model.Token{}).Where("user_id = ?", tenant.UserID).Update("status", common.TokenStatusDisabled).Error; err != nil {
				return mutationResult{}, err
			}
			if err := tx.Delete(&model.User{}, tenant.UserID).Error; err != nil {
				return mutationResult{}, err
			}
		default:
			return mutationResult{}, newAPIError(http.StatusBadRequest, "invalid_request", "invalid status")
		}
		if err := tx.Model(&tenant).Update("updated_at", now).Error; err != nil {
			return mutationResult{}, err
		}
		body := successBody(gin.H{"username": tenantKey, "status": status})
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
	if err := c.ShouldBindJSON(&request); err != nil {
		writeAPIError(c, newAPIError(http.StatusBadRequest, "invalid_request", "invalid quota adjustment"))
		return
	}
	request.OrderNo = strings.TrimSpace(request.OrderNo)
	request.Reason = strings.TrimSpace(request.Reason)
	request.RequestID = strings.TrimSpace(request.RequestID)
	if !validOrderNo(request.OrderNo) || request.Reason == "" || len(request.Reason) > 255 {
		writeAPIError(c, newAPIError(http.StatusBadRequest, "invalid_request", "invalid quota adjustment"))
		return
	}
	if !requestIDPattern.MatchString(request.RequestID) {
		writeAPIError(c, newAPIError(http.StatusBadRequest, "invalid_request", "request_id is invalid"))
		return
	}
	if err := validateQuotaAdjustmentMode(request); err != nil {
		writeAPIError(c, err)
		return
	}
	tenantKey := c.Param("tenant_key")
	response, err := executeIdempotent(c, "tenant_quota:"+tenantKey, request.RequestID, func(tx *gorm.DB) (mutationResult, error) {
		tenant, user, err := lockTenant(tx, tenantKey)
		if err != nil {
			return mutationResult{}, err
		}
		if err := ensureTenantMutable(user); err != nil {
			return mutationResult{}, err
		}
		if existing, replay, err := loadQuotaOperation(tx, tenantKey, quotaTargetTenant, "", request); err != nil {
			return mutationResult{}, err
		} else if replay {
			body := successBody(quotaAdjustmentResponse(tenantKey, existing.ResultQuota-existing.Delta, existing.ResultQuota, existing.Delta, request.OrderNo, request.Reason, true))
			return mutationResult{FreshResponse: body, StoredResponse: body, ResourceRef: tenantKey}, nil
		}

		quotaBefore := user.Quota
		var delta int
		if request.AbsoluteQuota != nil {
			delta = *request.AbsoluteQuota - quotaBefore
		} else {
			delta = *request.DeltaQuota
		}
		if err := validateQuotaDelta(delta); err != nil {
			return mutationResult{}, err
		}
		if delta != 0 {
			query := tx.Model(&model.User{}).Where("id = ?", tenant.UserID)
			if delta > 0 {
				query = query.Where("quota <= ?", common.MaxWalletQuota-delta)
			} else {
				query = query.Where("quota >= ?", -delta)
			}
			result := query.Update("quota", gorm.Expr("quota + ?", delta))
			if result.Error != nil {
				return mutationResult{}, result.Error
			}
			if result.RowsAffected != 1 {
				return mutationResult{}, newAPIError(http.StatusConflict, "quota_out_of_range", "quota adjustment would exceed allowed bounds")
			}
		}
		if err := tx.Select("id", "quota").First(&user, tenant.UserID).Error; err != nil {
			return mutationResult{}, err
		}
		if err := createQuotaOperation(tx, tenantKey, quotaTargetTenant, "", request, delta, user.Quota); err != nil {
			return mutationResult{}, err
		}
		body := successBody(quotaAdjustmentResponse(tenantKey, quotaBefore, user.Quota, delta, request.OrderNo, request.Reason, false))
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
	items := make([]tenantKeyItem, 0, len(tokens))
	for _, token := range tokens {
		items = append(items, toTenantKeyItem(token))
	}
	c.JSON(http.StatusOK, successBody(gin.H{"items": items}))
}

func rotateToken(c *gin.Context) {
	var request rotateTokenRequest
	if err := c.ShouldBindJSON(&request); err != nil {
		writeAPIError(c, newAPIError(http.StatusBadRequest, "invalid_request", "invalid token rotation request"))
		return
	}
	request.RequestID = strings.TrimSpace(request.RequestID)
	request.Reason = strings.TrimSpace(request.Reason)
	if !requestIDPattern.MatchString(request.RequestID) {
		writeAPIError(c, newAPIError(http.StatusBadRequest, "invalid_request", "request_id is invalid"))
		return
	}
	if request.Reason == "" || len(request.Reason) > 255 {
		writeAPIError(c, newAPIError(http.StatusBadRequest, "invalid_request", "reason is required"))
		return
	}
	newKey, err := common.GenerateKey()
	if err != nil {
		writeAPIError(c, err)
		return
	}
	tenantKey := c.Param("tenant_key")
	response, err := executeIdempotent(c, "rotate_token:"+tenantKey, request.RequestID, func(tx *gorm.DB) (mutationResult, error) {
		tenant, user, err := lockTenant(tx, tenantKey)
		if err != nil {
			return mutationResult{}, err
		}
		if err := ensureTenantMutable(user); err != nil {
			return mutationResult{}, err
		}
		token, err := loadPrimaryToken(tx, tenant.UserID, tenantKey)
		if err != nil {
			return mutationResult{}, err
		}
		oldName := token.Name
		now := time.Now().Unix()
		if err := tx.Model(&token).Updates(map[string]any{
			"key":           newKey,
			"status":        common.TokenStatusEnabled,
			"accessed_time": now,
		}).Error; err != nil {
			return mutationResult{}, err
		}
		body := successBody(gin.H{
			"old_token_name": oldName,
			"token_name":     oldName,
			"token_key":      "sk-" + newKey,
		})
		return mutationResult{FreshResponse: body, StoredResponse: body, ResourceRef: oldName, AfterCommit: func() { _ = model.InvalidateUserTokensCache(tenant.UserID) }}, nil
	})
	writeIdempotentResponse(c, response, err)
}

func adjustTokenQuota(c *gin.Context) {
	var request tokenQuotaSetRequest
	if err := c.ShouldBindJSON(&request); err != nil || !tokenNamePattern.MatchString(c.Param("token_name")) {
		writeAPIError(c, newAPIError(http.StatusBadRequest, "invalid_request", "invalid token quota request"))
		return
	}
	request.RequestID = strings.TrimSpace(request.RequestID)
	request.Reason = strings.TrimSpace(request.Reason)
	if !requestIDPattern.MatchString(request.RequestID) {
		writeAPIError(c, newAPIError(http.StatusBadRequest, "invalid_request", "request_id is invalid"))
		return
	}
	if request.RemainQuota == nil || request.UnlimitedQuota == nil || request.Reason == "" || len(request.Reason) > 255 {
		writeAPIError(c, newAPIError(http.StatusBadRequest, "invalid_request", "remain_quota, unlimited_quota and reason are required"))
		return
	}
	if err := validateQuota(*request.RemainQuota); err != nil {
		writeAPIError(c, err)
		return
	}
	tenantKey := c.Param("tenant_key")
	tokenName := c.Param("token_name")
	response, err := executeIdempotent(c, "token_quota:"+tenantKey+":"+tokenName, request.RequestID, func(tx *gorm.DB) (mutationResult, error) {
		tenant, user, err := lockTenant(tx, tenantKey)
		if err != nil {
			return mutationResult{}, err
		}
		if err := ensureTenantMutable(user); err != nil {
			return mutationResult{}, err
		}
		var token model.Token
		if err := tx.Where("user_id = ? AND name = ?", tenant.UserID, tokenName).First(&token).Error; err != nil {
			return mutationResult{}, newAPIError(http.StatusNotFound, "token_not_found", "token not found")
		}
		updates := map[string]any{
			"remain_quota":    *request.RemainQuota,
			"unlimited_quota": *request.UnlimitedQuota,
		}
		if *request.UnlimitedQuota || *request.RemainQuota > 0 {
			if token.Status == common.TokenStatusExhausted {
				updates["status"] = common.TokenStatusEnabled
			}
		}
		if err := tx.Model(&token).Updates(updates).Error; err != nil {
			return mutationResult{}, err
		}
		body := successBody(gin.H{
			"token_name":      tokenName,
			"remain_quota":    *request.RemainQuota,
			"unlimited_quota": *request.UnlimitedQuota,
		})
		return mutationResult{FreshResponse: body, StoredResponse: body, ResourceRef: tokenName, AfterCommit: func() { _ = model.InvalidateUserTokensCache(tenant.UserID) }}, nil
	})
	writeIdempotentResponse(c, response, err)
}

func deleteToken(c *gin.Context) {
	var request requestIDBody
	if err := c.ShouldBindJSON(&request); err != nil {
		writeAPIError(c, newAPIError(http.StatusBadRequest, "invalid_request", "request_id is required"))
		return
	}
	requestID := strings.TrimSpace(request.RequestID)
	if !requestIDPattern.MatchString(requestID) {
		writeAPIError(c, newAPIError(http.StatusBadRequest, "invalid_request", "request_id is invalid"))
		return
	}
	tenantKey := c.Param("tenant_key")
	tokenName := c.Param("token_name")
	response, err := executeIdempotent(c, "delete_token:"+tenantKey+":"+tokenName, requestID, func(tx *gorm.DB) (mutationResult, error) {
		tenant, user, err := lockTenant(tx, tenantKey)
		if err != nil {
			return mutationResult{}, err
		}
		if err := ensureTenantMutable(user); err != nil {
			return mutationResult{}, err
		}
		result := tx.Model(&model.Token{}).Where("user_id = ? AND name = ?", tenant.UserID, tokenName).Update("status", common.TokenStatusDisabled)
		if result.Error != nil {
			return mutationResult{}, result.Error
		}
		if result.RowsAffected != 1 {
			return mutationResult{}, newAPIError(http.StatusNotFound, "token_not_found", "token not found")
		}
		body := successBody(gin.H{"username": tenantKey, "token_name": tokenName, "status": common.TokenStatusDisabled})
		return mutationResult{FreshResponse: body, StoredResponse: body, ResourceRef: tokenName, AfterCommit: func() { _ = model.InvalidateUserTokensCache(tenant.UserID) }}, nil
	})
	writeIdempotentResponse(c, response, err)
}

type modelCatalogItem struct {
	ModelName              string   `json:"model_name"`
	ChannelCount           int      `json:"channel_count"`
	Enabled                bool     `json:"enabled"`
	Description            string   `json:"description,omitempty"`
	Tags                   string   `json:"tags,omitempty"`
	VendorName             string   `json:"vendor_name,omitempty"`
	QuotaType              int      `json:"quota_type"`
	ModelRatio             *float64 `json:"model_ratio,omitempty"`
	ModelPrice             *float64 `json:"model_price,omitempty"`
	CompletionRatio        *float64 `json:"completion_ratio,omitempty"`
	CacheRatio             *float64 `json:"cache_ratio,omitempty"`
	CreateCacheRatio       *float64 `json:"create_cache_ratio,omitempty"`
	ImageRatio             *float64 `json:"image_ratio,omitempty"`
	AudioRatio             *float64 `json:"audio_ratio,omitempty"`
	AudioCompletionRatio   *float64 `json:"audio_completion_ratio,omitempty"`
	EnableGroups           []string `json:"enable_groups"`
	SupportedEndpointTypes []string `json:"supported_endpoint_types"`
}

func listModels(c *gin.Context) {
	items, err := modelCatalog()
	if err != nil {
		writeAPIError(c, err)
		return
	}
	c.JSON(http.StatusOK, successBody(gin.H{"items": items}))
}

func modelCatalog() ([]modelCatalogItem, error) {
	if model.DB == nil {
		return []modelCatalogItem{}, nil
	}
	counts, err := enabledChannelCounts(model.DB)
	if err != nil {
		return nil, err
	}
	vendors := map[int]string{}
	for _, vendor := range model.GetVendors() {
		vendors[vendor.ID] = vendor.Name
	}
	pricing := model.GetPricing()
	items := make([]modelCatalogItem, 0, len(pricing))
	for _, item := range pricing {
		endpoints := make([]string, 0, len(item.SupportedEndpointTypes))
		for _, endpoint := range item.SupportedEndpointTypes {
			endpoints = append(endpoints, string(endpoint))
		}
		groups := item.EnableGroup
		if groups == nil {
			groups = []string{}
		}
		entry := modelCatalogItem{
			ModelName:              item.ModelName,
			ChannelCount:           counts[item.ModelName],
			Enabled:                counts[item.ModelName] > 0,
			Description:            item.Description,
			Tags:                   item.Tags,
			VendorName:             vendors[item.VendorID],
			QuotaType:              item.QuotaType,
			CacheRatio:             item.CacheRatio,
			CreateCacheRatio:       item.CreateCacheRatio,
			ImageRatio:             item.ImageRatio,
			AudioRatio:             item.AudioRatio,
			AudioCompletionRatio:   item.AudioCompletionRatio,
			EnableGroups:           groups,
			SupportedEndpointTypes: endpoints,
		}
		if item.QuotaType == 1 {
			price := item.ModelPrice
			entry.ModelPrice = &price
		} else {
			ratio := item.ModelRatio
			completion := item.CompletionRatio
			entry.ModelRatio = &ratio
			entry.CompletionRatio = &completion
		}
		items = append(items, entry)
	}
	slices.SortFunc(items, func(a, b modelCatalogItem) int {
		return strings.Compare(a.ModelName, b.ModelName)
	})
	return items, nil
}

func enabledChannelCounts(db *gorm.DB) (map[string]int, error) {
	var rows []struct {
		Model     string `gorm:"column:model"`
		ChannelId int    `gorm:"column:channel_id"`
	}
	if err := db.Model(&model.Ability{}).Select("model", "channel_id").Where("enabled = ?", true).Find(&rows).Error; err != nil {
		return nil, err
	}
	channels := map[string]map[int]struct{}{}
	for _, row := range rows {
		if channels[row.Model] == nil {
			channels[row.Model] = map[int]struct{}{}
		}
		channels[row.Model][row.ChannelId] = struct{}{}
	}
	counts := make(map[string]int, len(channels))
	for name, ids := range channels {
		counts[name] = len(ids)
	}
	return counts, nil
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
	c.JSON(http.StatusOK, successListBody(events, page, pageSize, total))
}

func loadTenant(tenantKey string) (*Tenant, *model.User, error) {
	if !tenantKeyPattern.MatchString(tenantKey) {
		return nil, nil, newAPIError(http.StatusNotFound, "tenant_not_found", "tenant not found")
	}
	_, db := currentState()
	var user model.User
	if err := db.Unscoped().Select("id", "username", "display_name", "status", "quota", "used_quota", "last_login_at", "deleted_at").
		Where("username = ?", tenantKey).First(&user).Error; err != nil {
		return nil, nil, tenantLookupError(err)
	}
	var tenant Tenant
	if err := db.Where("user_id = ?", user.Id).First(&tenant).Error; err != nil {
		return nil, nil, tenantLookupError(err)
	}
	return &tenant, &user, nil
}

func tenantLookupError(err error) error {
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return newAPIError(http.StatusNotFound, "tenant_not_found", "tenant not found")
	}
	return err
}

func lockTenant(tx *gorm.DB, tenantKey string) (Tenant, model.User, error) {
	if !tenantKeyPattern.MatchString(tenantKey) {
		return Tenant{}, model.User{}, newAPIError(http.StatusNotFound, "tenant_not_found", "tenant not found")
	}
	var user model.User
	if err := tx.Unscoped().Where("username = ?", tenantKey).First(&user).Error; err != nil {
		return Tenant{}, model.User{}, tenantLookupError(err)
	}
	query := tx
	if tx.Dialector.Name() != "sqlite" {
		query = query.Clauses(clause.Locking{Strength: "UPDATE"})
	}
	var tenant Tenant
	if err := query.Where("user_id = ?", user.Id).First(&tenant).Error; err != nil {
		return Tenant{}, model.User{}, tenantLookupError(err)
	}
	return tenant, user, nil
}

func ensureTenantMutable(user model.User) error {
	if user.DeletedAt.Valid {
		return newAPIError(http.StatusConflict, "tenant_deleted", "deleted tenant cannot be modified")
	}
	return nil
}

func tenantStatusFromUser(user model.User) string {
	if user.DeletedAt.Valid {
		return tenantStatusDeleted
	}
	if user.Status == common.UserStatusDisabled {
		return tenantStatusDisabled
	}
	return tenantStatusEnabled
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

func validateQuotaAdjustmentMode(request quotaAdjustmentRequest) error {
	hasDelta := request.DeltaQuota != nil
	hasAbsolute := request.AbsoluteQuota != nil
	if hasDelta == hasAbsolute {
		return newAPIError(http.StatusBadRequest, "invalid_request", "exactly one of delta_quota or absolute_quota is required")
	}
	if hasAbsolute {
		return validateQuota(*request.AbsoluteQuota)
	}
	return validateQuotaDelta(*request.DeltaQuota)
}

func quotaAdjustmentResponse(username string, quotaBefore, quotaAfter, delta int, orderNo, reason string, replayed bool) gin.H {
	return gin.H{
		"username":     username,
		"quota_before": quotaBefore,
		"quota_after":  quotaAfter,
		"delta_quota":  delta,
		"order_no":     orderNo,
		"reason":       reason,
		"replayed":     replayed,
	}
}

func loadQuotaOperation(tx *gorm.DB, tenantKey, targetType, targetRef string, request quotaAdjustmentRequest) (*QuotaOperation, bool, error) {
	var existing QuotaOperation
	err := tx.Where(
		"tenant_key = ? AND target_type = ? AND target_ref = ? AND operation_id = ?",
		tenantKey, targetType, targetRef, request.OrderNo,
	).First(&existing).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	if existing.Reason != strings.TrimSpace(request.Reason) {
		return nil, false, newAPIError(http.StatusConflict, "operation_conflict", "order_no was already used with a different adjustment")
	}
	if request.AbsoluteQuota != nil {
		if existing.AbsoluteQuota == nil || *existing.AbsoluteQuota != *request.AbsoluteQuota {
			return nil, false, newAPIError(http.StatusConflict, "operation_conflict", "order_no was already used with a different adjustment")
		}
	} else if existing.AbsoluteQuota != nil || existing.Delta != *request.DeltaQuota {
		return nil, false, newAPIError(http.StatusConflict, "operation_conflict", "order_no was already used with a different adjustment")
	}
	return &existing, true, nil
}

func createQuotaOperation(tx *gorm.DB, tenantKey, targetType, targetRef string, request quotaAdjustmentRequest, delta, resultQuota int) error {
	op := QuotaOperation{
		TenantKey:     tenantKey,
		TargetType:    targetType,
		TargetRef:     targetRef,
		OrderNo:       request.OrderNo,
		Delta:         delta,
		AbsoluteQuota: request.AbsoluteQuota,
		Reason:        strings.TrimSpace(request.Reason),
		ResultQuota:   resultQuota,
		CreatedAt:     time.Now().Unix(),
	}
	if err := tx.Create(&op).Error; err != nil {
		return newAPIError(http.StatusConflict, "operation_conflict", "order_no was already used")
	}
	return nil
}

func validOrderNo(value string) bool {
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

func primaryTokenName(tenantKey string) string {
	name := "nova-" + tenantKey
	if len(name) > 50 {
		name = name[:50]
	}
	return name
}

func loadPrimaryToken(tx *gorm.DB, userID int, tenantKey string) (model.Token, error) {
	var token model.Token
	name := primaryTokenName(tenantKey)
	err := tx.Where("user_id = ? AND name = ? AND status = ?", userID, name, common.TokenStatusEnabled).First(&token).Error
	if err == nil {
		return token, nil
	}
	if !errors.Is(err, gorm.ErrRecordNotFound) {
		return model.Token{}, err
	}
	err = tx.Where("user_id = ? AND status = ?", userID, common.TokenStatusEnabled).Order("id DESC").First(&token).Error
	if err == nil {
		return token, nil
	}
	if !errors.Is(err, gorm.ErrRecordNotFound) {
		return model.Token{}, err
	}
	err = tx.Where("user_id = ? AND name = ?", userID, name).First(&token).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return model.Token{}, newAPIError(http.StatusNotFound, "token_not_found", "token not found")
	}
	if err != nil {
		return model.Token{}, err
	}
	return token, nil
}

func buildTenantDetail(tenant Tenant, user model.User) (tenantResponse, error) {
	_, db := currentState()
	lastActive := user.LastLoginAt
	var lastUsage int64
	if err := db.Model(&UsageEvent{}).Select("COALESCE(MAX(occurred_at), 0)").Where("tenant_id = ?", tenant.ID).Scan(&lastUsage).Error; err != nil {
		return tenantResponse{}, err
	}
	if lastUsage > lastActive {
		lastActive = lastUsage
	}
	if lastActive <= 0 {
		lastActive = tenant.CreatedAt
	}

	tokenName := ""
	tokenKey := ""
	var token model.Token
	err := db.Where("user_id = ? AND status = ?", tenant.UserID, common.TokenStatusEnabled).Order("id DESC").First(&token).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		err = db.Where("user_id = ?", tenant.UserID).Order("id DESC").First(&token).Error
	}
	if err == nil {
		tokenName = token.Name
		if token.Key != "" {
			tokenKey = "sk-" + model.MaskTokenKey(token.Key)
		}
	} else if !errors.Is(err, gorm.ErrRecordNotFound) {
		return tenantResponse{}, err
	}

	return tenantResponse{
		UserID:       tenant.UserID,
		Username:     user.Username,
		DisplayName:  user.DisplayName,
		Status:       tenantStatusFromUser(user),
		Quota:        user.Quota,
		UsedQuota:    user.UsedQuota,
		TokenName:    tokenName,
		TokenKey:     tokenKey,
		CreatedAt:    formatUnixUTC(tenant.CreatedAt),
		LastActiveAt: formatUnixUTC(lastActive),
	}, nil
}

func formatUnixUTC(seconds int64) string {
	if seconds <= 0 {
		return ""
	}
	return time.Unix(seconds, 0).UTC().Format(time.RFC3339)
}

func toTenantKeyItem(token model.Token) tenantKeyItem {
	return tenantKeyItem{
		TokenID:        token.Id,
		TokenName:      token.Name,
		Key:            "sk-" + token.Key,
		Status:         tokenStatusString(token.Status),
		ExpiredTime:    token.ExpiredTime,
		RemainQuota:    token.RemainQuota,
		UnlimitedQuota: token.UnlimitedQuota,
		UsedQuota:      token.UsedQuota,
		CreatedTime:    token.CreatedTime,
	}
}

func tokenStatusString(status int) string {
	switch status {
	case common.TokenStatusEnabled:
		return "enabled"
	case common.TokenStatusDisabled:
		return "disabled"
	case common.TokenStatusExpired:
		return "expired"
	case common.TokenStatusExhausted:
		return "exhausted"
	default:
		return "disabled"
	}
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

func successBody(data any) gin.H {
	return gin.H{"success": true, "message": "ok", "data": data}
}

func successListBody(data any, page, pageSize int, total int64) gin.H {
	return gin.H{"success": true, "message": "ok", "data": data, "page": page, "page_size": pageSize, "total": total}
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
