package nova

import (
	"errors"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/service"
	"github.com/google/uuid"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

func recordAttribution(event service.UsageLifecycleEvent) error {
	if event.Attribution == nil || !event.Attribution.Verified || event.Attribution.Provider != "nova" {
		return nil
	}
	if event.SourceKey == "" {
		return errors.New("usage attribution source key is required")
	}
	config, db := currentState()
	if !config.Enabled || db == nil {
		return nil
	}
	tenant, username, err := resolveTenantOwnership(db, event.UserID, event.TokenID)
	if err != nil {
		return err
	}
	if username != event.Attribution.Subject {
		return errors.New("verified usage attribution does not match token ownership")
	}
	return db.Clauses(clause.OnConflict{DoNothing: true}).Create(&Attribution{
		SourceType:    event.SourceType,
		SourceKey:     event.SourceKey,
		TenantID:      tenant.ID,
		TenantKey:     username,
		UserID:        event.UserID,
		TokenID:       event.TokenID,
		NovaRequestID: event.Attribution.RequestID,
		CreatedAt:     time.Now().Unix(),
	}).Error
}

func recordFinalizedUsage(event service.UsageLifecycleEvent) error {
	if event.Quota < 0 || event.SourceKey == "" {
		return errors.New("invalid finalized usage event")
	}
	config, db := currentState()
	if !config.Enabled || db == nil {
		return nil
	}
	tenant, username, err := resolveTenantOwnership(db, event.UserID, event.TokenID)
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil
	}
	if err != nil {
		return err
	}

	novaRequestID := ""
	if event.SourceType == "task" {
		var attribution Attribution
		if err := db.Where("source_type = ? AND source_key = ?", event.SourceType, event.SourceKey).First(&attribution).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return nil
			}
			return err
		}
		if attribution.TenantID != tenant.ID || attribution.TokenID != event.TokenID {
			return errors.New("task attribution does not match token ownership")
		}
		novaRequestID = attribution.NovaRequestID
	} else {
		if event.Attribution == nil || !event.Attribution.Verified || event.Attribution.Provider != "nova" || event.Attribution.Subject != username {
			return nil
		}
		novaRequestID = event.Attribution.RequestID
	}

	now := time.Now().Unix()
	if event.OccurredAt == 0 {
		event.OccurredAt = now
	}
	eventID := uuid.NewString()
	usage := UsageEvent{
		EventID:          eventID,
		SourceType:       event.SourceType,
		SourceKey:        event.SourceKey,
		TenantID:         tenant.ID,
		TenantKey:        username,
		UserID:           event.UserID,
		TokenID:          event.TokenID,
		TokenName:        event.TokenName,
		RequestID:        event.RequestID,
		NovaRequestID:    novaRequestID,
		ModelName:        event.ModelName,
		UpstreamModel:    event.UpstreamModel,
		ChannelID:        event.ChannelID,
		GroupName:        event.GroupName,
		Quota:            event.Quota,
		PromptTokens:     event.PromptTokens,
		CompletionTokens: event.CompletionTokens,
		TotalTokens:      event.TotalTokens,
		OccurredAt:       event.OccurredAt,
		CreatedAt:        now,
	}
	payload, err := marshalUsageMessage(usage)
	if err != nil {
		return err
	}
	outbox := Outbox{EventID: eventID, ExchangeName: config.Exchange, RoutingKey: config.RoutingKey, Payload: string(payload), Status: "pending", NextAttemptAt: now, CreatedAt: now, UpdatedAt: now}
	err = db.Transaction(func(tx *gorm.DB) error {
		created := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&usage)
		if created.Error != nil {
			return created.Error
		}
		if created.RowsAffected != 0 {
			if err := tx.Create(&outbox).Error; err != nil {
				return err
			}
		}
		return tx.Where("source_type = ? AND source_key = ?", event.SourceType, event.SourceKey).Delete(&Attribution{}).Error
	})
	return err
}

func marshalUsageMessage(event UsageEvent) ([]byte, error) {
	usagePayload := map[string]any{"quota": event.Quota}
	if event.ModelName != "" {
		usagePayload["model"] = event.ModelName
	}
	if event.PromptTokens != 0 {
		usagePayload["prompt_tokens"] = event.PromptTokens
	}
	if event.CompletionTokens != 0 {
		usagePayload["completion_tokens"] = event.CompletionTokens
	}
	if event.TotalTokens != 0 {
		usagePayload["total_tokens"] = event.TotalTokens
	}
	messageContext := map[string]any{"token_id": event.TokenID}
	if event.RequestID != "" {
		messageContext["request_id"] = event.RequestID
	}
	if event.NovaRequestID != "" {
		messageContext["nova_request_id"] = event.NovaRequestID
	}
	if event.TokenName != "" {
		messageContext["token_name"] = event.TokenName
	}
	if event.ChannelID != 0 {
		messageContext["channel_id"] = event.ChannelID
	}
	if event.GroupName != "" {
		messageContext["group"] = event.GroupName
	}
	return common.Marshal(map[string]any{
		"schema_version": 1,
		"event_id":       event.EventID,
		"event_type":     "nova.usage.reported",
		"occurred_at":    time.Unix(event.OccurredAt, 0).UTC().Format(time.RFC3339),
		"producer":       "new-api",
		"tenant_key":     event.TenantKey,
		"source": map[string]any{
			"type": event.SourceType,
			"key":  event.SourceKey,
		},
		"usage":   usagePayload,
		"context": messageContext,
	})
}

func resolveTenantOwnership(db *gorm.DB, userID, tokenID int) (*Tenant, string, error) {
	if userID <= 0 || tokenID <= 0 {
		return nil, "", gorm.ErrRecordNotFound
	}
	var token model.Token
	if err := db.Select("id", "user_id").Where("id = ? AND user_id = ?", tokenID, userID).First(&token).Error; err != nil {
		return nil, "", err
	}
	var user model.User
	if err := db.Select("id", "username", "status", "deleted_at").First(&user, userID).Error; err != nil {
		return nil, "", err
	}
	if user.DeletedAt.Valid || user.Status != common.UserStatusEnabled {
		return nil, "", gorm.ErrRecordNotFound
	}
	var tenant Tenant
	if err := db.Where("user_id = ?", userID).First(&tenant).Error; err != nil {
		return nil, "", err
	}
	return &tenant, user.Username, nil
}
