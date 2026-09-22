package nova

import (
	"fmt"
	"net/http"

	"github.com/QuantumNous/new-api/model"
	"github.com/gin-gonic/gin"
)

const novaAuditServiceIdentity = "nova-service"

func recordManagementAudit(c *gin.Context) {
	action := managementAuditAction(c.Request.Method, c.FullPath())
	if action == "" {
		return
	}

	params := model.AuditFields{}
	tenantKey := c.Param("tenant_key")
	if tenantKey == "" {
		tenantKey = c.GetString("nova_audit_tenant_key")
	}
	if tenantKey != "" {
		params["tenant_key"] = tenantKey
	}
	if extra, ok := c.Get("nova_audit_params"); ok {
		if extraParams, ok := extra.(model.AuditFields); ok {
			for key, value := range extraParams {
				params[key] = value
			}
		}
	}
	if c.Writer.Header().Get("Idempotency-Replayed") == "true" {
		params["idempotency_replayed"] = true
	}

	status := c.Writer.Status()
	userID, username := managementAuditIdentity(tenantKey)
	model.RecordAuditLog(c, model.AuditLog{
		UserId:     userID,
		Username:   username,
		ActorRole:  model.AuditActorRoleService,
		Category:   model.AuditCategoryOperation,
		Action:     action,
		AuthMethod: "nova_hmac",
		Status:     status,
		Success:    status >= http.StatusOK && status < http.StatusMultipleChoices,
		Content:    managementAuditContent(action, tenantKey, params, status),
		Other: model.AuditOther{
			Op: &model.AuditOperation{Action: action, Params: params},
			AdminInfo: &model.AuditAdminInfo{
				AdminUsername: novaAuditServiceIdentity,
				AdminRole:     model.AuditActorRoleService,
				AuthMethod:    "nova_hmac",
			},
			RootInfo: model.AuditFields{
				"nova_key_id": c.GetString("nova_key_id"),
			},
		},
	})
}

func recordAuthenticationFailure(c *gin.Context) {
	model.RecordAuditLog(c, model.AuditLog{
		Username:   novaAuditServiceIdentity,
		ActorRole:  model.AuditActorRoleService,
		Category:   model.AuditCategorySecurity,
		Action:     "nova.authentication.failed",
		AuthMethod: "nova_hmac",
		Status:     http.StatusUnauthorized,
		Success:    false,
		Content:    "通过 Nova 接口的 HMAC 鉴权失败，未能关联租户用户",
		Other: model.AuditOther{
			Op: &model.AuditOperation{Action: "nova.authentication.failed"},
			AdminInfo: &model.AuditAdminInfo{
				AdminUsername: novaAuditServiceIdentity,
				AdminRole:     model.AuditActorRoleService,
				AuthMethod:    "nova_hmac",
			},
		},
	})
}

// managementAuditIdentity makes tenant-scoped management events belong to the
// tenant user, while AdminInfo retains the actual Nova service caller.
func managementAuditIdentity(tenantKey string) (int, string) {
	if tenantKey == "" {
		return 0, novaAuditServiceIdentity
	}
	_, db := currentState()
	if db == nil {
		return 0, novaAuditServiceIdentity
	}
	var user model.User
	if err := db.Unscoped().Select("id", "username").Where("username = ?", tenantKey).First(&user).Error; err != nil {
		return 0, novaAuditServiceIdentity
	}
	return user.Id, user.Username
}

func managementAuditContent(action, tenantKey string, params model.AuditFields, status int) string {
	content := "通过 Nova 接口"
	switch action {
	case "nova.tenant.create":
		content += "创建租户"
	case "nova.tenant.update":
		content += "更新租户信息"
	case "nova.tenant.delete":
		content += "删除租户"
	case "nova.tenant.quota.adjust":
		content += "调整租户额度"
	case "nova.tenant.disable":
		content += "禁用租户"
	case "nova.tenant.enable":
		content += "启用租户"
	case "nova.token.rotate":
		content += "轮换令牌密钥"
	case "nova.token.list":
		content += "读取令牌列表（含明文密钥）"
	case "nova.token.quota.set":
		content += "设置令牌额度"
	case "nova.token.revoke":
		content += "吊销令牌"
	default:
		content += "执行管理操作"
	}
	if tenantKey != "" {
		content += "，租户：" + tenantKey
	}
	if orderNo, ok := params["order_no"]; ok {
		content += "，调额单号：" + fmt.Sprint(orderNo)
	}
	if quota, ok := params["delta_quota"]; ok {
		content += "，增量额度：" + fmt.Sprint(quota)
	}
	if quota, ok := params["absolute_quota"]; ok {
		content += "，目标额度：" + fmt.Sprint(quota)
	}
	if oldName, ok := params["old_token_name"]; ok {
		content += "，原令牌：" + fmt.Sprint(oldName)
	}
	if tokenName, ok := params["token_name"]; ok {
		content += "，令牌：" + fmt.Sprint(tokenName)
	}
	if quota, ok := params["remain_quota"]; ok {
		content += "，令牌额度：" + fmt.Sprint(quota)
	}
	if unlimited, ok := params["unlimited_quota"]; ok {
		content += "，不限额：" + fmt.Sprint(unlimited)
	}
	if tenantStatus, ok := params["status"]; ok {
		content += "，目标状态：" + fmt.Sprint(tenantStatus)
	}
	if params["idempotency_replayed"] == true {
		content += "，幂等重放"
	}
	if status >= http.StatusOK && status < http.StatusMultipleChoices {
		return content + fmt.Sprintf("，结果：成功（HTTP %d）", status)
	}
	return content + fmt.Sprintf("，结果：失败（HTTP %d）", status)
}

func managementAuditAction(method, route string) string {
	switch method + " " + route {
	case "POST /api/novapay/tenant":
		return "nova.tenant.create"
	case "PUT /api/novapay/tenant/:tenant_key":
		return "nova.tenant.update"
	case "DELETE /api/novapay/tenant/:tenant_key":
		return "nova.tenant.delete"
	case "POST /api/novapay/tenant/:tenant_key/quota":
		return "nova.tenant.quota.adjust"
	case "POST /api/novapay/tenant/:tenant_key/disable":
		return "nova.tenant.disable"
	case "POST /api/novapay/tenant/:tenant_key/enable":
		return "nova.tenant.enable"
	case "POST /api/novapay/tenant/:tenant_key/token/rotate":
		return "nova.token.rotate"
	case "GET /api/novapay/tenant/:tenant_key/tokens":
		return "nova.token.list"
	case "POST /api/novapay/tenant/:tenant_key/tokens/:token_name/quota":
		return "nova.token.quota.set"
	case "DELETE /api/novapay/tenant/:tenant_key/tokens/:token_name":
		return "nova.token.revoke"
	default:
		return ""
	}
}
