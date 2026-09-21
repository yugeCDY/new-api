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
		SourceType: event.SourceType, SourceKey: event.SourceKey, TenantID: tenant.ID,
		TenantKey: username, UserID: event.UserID, TokenID: event.TokenID,
		NovaRequestID: event.Attribution.RequestID, CreatedAt: time.Now().Unix(),
	}).Error
}

func recordFinalizedUsage(event service.UsageLifecycleEvent) error {
	if event.Quota < 0 || event.SourceKey == "" {
		return errors.New("invalid finalized usage event")
	}
	if event.LogID <= 0 {
		return nil
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
	if event.Attribution != nil {
		novaRequestID = event.Attribution.RequestID
	}
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
	} else if event.Attribution == nil || !event.Attribution.Verified || event.Attribution.Provider != "nova" || event.Attribution.Subject != username {
		return nil
	}
	log, err := consumeLogByID(event.LogID)
	if err != nil {
		return err
	}
	if log.UserId != event.UserID || log.TokenId != event.TokenID {
		return errors.New("consume log does not match usage ownership")
	}
	now := time.Now().Unix()
	if event.OccurredAt == 0 {
		event.OccurredAt = now
	}
	requestID := event.RequestID
	if requestID == "" {
		requestID = log.RequestId
	}
	ref := LogRef{EventID: uuid.NewString(), SourceType: event.SourceType, SourceKey: event.SourceKey,
		TenantKey: username, UserID: event.UserID, TokenID: event.TokenID, RequestID: requestID,
		NovaRequestID: novaRequestID, LogID: event.LogID, OccurredAt: event.OccurredAt, CreatedAt: now}
	payload, err := marshalUsageMessage(ref)
	if err != nil {
		return err
	}
	outbox := Outbox{EventID: ref.EventID, ExchangeName: config.Exchange, RoutingKey: config.RoutingKey,
		Payload: string(payload), Status: "pending", NextAttemptAt: now, CreatedAt: now, UpdatedAt: now}
	return db.Transaction(func(tx *gorm.DB) error {
		created := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&ref)
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
}

func consumeLogByID(logID int) (*model.Log, error) {
	if model.LOG_DB == nil {
		return nil, errors.New("log database is not configured")
	}
	var log model.Log
	if err := model.LOG_DB.Where("id = ? AND type = ?", logID, model.LogTypeConsume).First(&log).Error; err != nil {
		return nil, err
	}
	return &log, nil
}

func marshalUsageMessage(ref LogRef) ([]byte, error) {
	item, err := usageLogRefResponse(ref)
	if err != nil {
		return nil, err
	}
	return common.Marshal(item)
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
