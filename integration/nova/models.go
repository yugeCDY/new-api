package nova

import "gorm.io/gorm"

type Tenant struct {
	ID        int64 `json:"id"`
	UserID    int   `json:"user_id" gorm:"uniqueIndex"`
	CreatedAt int64 `json:"created_at" gorm:"bigint"`
	UpdatedAt int64 `json:"updated_at" gorm:"bigint"`
}

func (Tenant) TableName() string { return "nova_tenants" }

// LogRef associates a Nova request with its authoritative New API consume log.
// The log database remains the source for usage and billing fields; this table
// only provides tenant-scoped correlation, idempotency, and outbox recovery.
type LogRef struct {
	ID            int64  `json:"id" gorm:"index:idx_nova_log_ref_tenant_id,priority:2"`
	EventID       string `json:"event_id" gorm:"type:varchar(64);uniqueIndex"`
	SourceType    string `json:"source_type" gorm:"type:varchar(16);uniqueIndex:idx_nova_log_ref_source,priority:1"`
	SourceKey     string `json:"source_key" gorm:"type:varchar(128);uniqueIndex:idx_nova_log_ref_source,priority:2"`
	TenantKey     string `json:"tenant_key" gorm:"type:varchar(64);uniqueIndex:idx_nova_log_ref_source,priority:3;index:idx_nova_log_ref_timeline,priority:1;index:idx_nova_log_ref_request,priority:1;index:idx_nova_log_ref_tenant_id,priority:1"`
	UserID        int    `json:"user_id" gorm:"index"`
	RequestID     string `json:"request_id" gorm:"type:varchar(128);index:idx_nova_log_ref_request,priority:2"`
	NovaRequestID string `json:"nova_request_id" gorm:"type:varchar(128)"`
	TokenID       int    `json:"token_id" gorm:"index"`
	LogID         int    `json:"log_id" gorm:"index"`
	OccurredAt    int64  `json:"occurred_at" gorm:"bigint;index:idx_nova_log_ref_timeline,priority:2"`
	CreatedAt     int64  `json:"created_at" gorm:"bigint"`
}

func (LogRef) TableName() string { return "nova_log_ref" }

type Outbox struct {
	ID            int64  `json:"id"`
	EventID       string `json:"event_id" gorm:"type:varchar(64);uniqueIndex"`
	ExchangeName  string `json:"exchange_name" gorm:"type:varchar(128)"`
	RoutingKey    string `json:"routing_key" gorm:"type:varchar(128)"`
	Payload       string `json:"payload" gorm:"type:text"`
	Status        string `json:"status" gorm:"type:varchar(16);index:idx_nova_outbox_ready,priority:1"`
	Attempts      int    `json:"attempts"`
	NextAttemptAt int64  `json:"next_attempt_at" gorm:"bigint;index:idx_nova_outbox_ready,priority:2"`
	LockedBy      string `json:"locked_by" gorm:"type:varchar(128)"`
	LockedUntil   int64  `json:"locked_until" gorm:"bigint;index"`
	LastError     string `json:"last_error" gorm:"type:varchar(512)"`
	PublishedAt   int64  `json:"published_at" gorm:"bigint"`
	CreatedAt     int64  `json:"created_at" gorm:"bigint"`
	UpdatedAt     int64  `json:"updated_at" gorm:"bigint"`
}

func (Outbox) TableName() string { return "nova_outbox" }

type IdempotencyRecord struct {
	ID             int64  `json:"id"`
	Scope          string `json:"scope" gorm:"type:varchar(128);uniqueIndex:idx_nova_idempotency,priority:1"`
	IdempotencyKey string `json:"idempotency_key" gorm:"type:varchar(128);uniqueIndex:idx_nova_idempotency,priority:2"`
	RequestHash    string `json:"request_hash" gorm:"type:char(64)"`
	Status         string `json:"status" gorm:"type:varchar(16);index"`
	HTTPStatus     int    `json:"http_status"`
	ResponseBody   string `json:"response_body" gorm:"type:text"`
	ResourceRef    string `json:"resource_ref" gorm:"type:varchar(191)"`
	ExpiresAt      int64  `json:"expires_at" gorm:"bigint;index"`
	CreatedAt      int64  `json:"created_at" gorm:"bigint"`
	UpdatedAt      int64  `json:"updated_at" gorm:"bigint"`
}

func (IdempotencyRecord) TableName() string { return "nova_idempotency" }

type ReplayNonce struct {
	ID               int64  `json:"id"`
	KeyID            string `json:"key_id" gorm:"type:varchar(64);uniqueIndex:idx_nova_replay_nonce,priority:1"`
	NonceHash        string `json:"nonce_hash" gorm:"type:char(64);uniqueIndex:idx_nova_replay_nonce,priority:2"`
	RequestTimestamp int64  `json:"request_timestamp" gorm:"bigint"`
	ExpiresAt        int64  `json:"expires_at" gorm:"bigint;index"`
	CreatedAt        int64  `json:"created_at" gorm:"bigint"`
}

func (ReplayNonce) TableName() string { return "nova_replay_nonces" }

type Attribution struct {
	ID            int64  `json:"id"`
	SourceType    string `json:"source_type" gorm:"type:varchar(16);uniqueIndex:idx_nova_attribution_source,priority:1"`
	SourceKey     string `json:"source_key" gorm:"type:varchar(128);uniqueIndex:idx_nova_attribution_source,priority:2"`
	TenantID      int64  `json:"tenant_id" gorm:"index"`
	TenantKey     string `json:"tenant_key" gorm:"type:varchar(64);index"` // denormalized users.username
	UserID        int    `json:"user_id" gorm:"index"`
	TokenID       int    `json:"token_id" gorm:"index"`
	NovaRequestID string `json:"nova_request_id" gorm:"type:varchar(128);index"`
	CreatedAt     int64  `json:"created_at" gorm:"bigint"`
}

func (Attribution) TableName() string { return "nova_attributions" }

const (
	quotaTargetTenant = "tenant"
	quotaTargetToken  = "token"
)

// QuotaOperation records a durable business-level quota adjustment identity.
// Unique on (tenant_key, target_type, target_ref, operation_id/order_no).
// tenant_key stores users.username. absolute_quota is set for absolute mode.
type QuotaOperation struct {
	ID            int64  `json:"id"`
	TenantKey     string `json:"tenant_key" gorm:"type:varchar(64);uniqueIndex:idx_nova_quota_operation,priority:1"`
	TargetType    string `json:"target_type" gorm:"type:varchar(16);uniqueIndex:idx_nova_quota_operation,priority:2"`
	TargetRef     string `json:"target_ref" gorm:"type:varchar(64);uniqueIndex:idx_nova_quota_operation,priority:3"`
	OrderNo       string `json:"order_no" gorm:"column:operation_id;type:varchar(128);uniqueIndex:idx_nova_quota_operation,priority:4"`
	Delta         int    `json:"delta"`
	AbsoluteQuota *int   `json:"absolute_quota,omitempty" gorm:"column:absolute_quota"`
	Reason        string `json:"reason" gorm:"type:varchar(255)"`
	ResultQuota   int    `json:"result_quota"`
	CreatedAt     int64  `json:"created_at" gorm:"bigint"`
}

func (QuotaOperation) TableName() string { return "nova_quota_operations" }

func migrate(db *gorm.DB) error {
	// Usage events were historical snapshots. They are intentionally discarded
	// when moving to log references, together with their dependent outbox rows.
	if db.Migrator().HasTable("nova_usage_events") {
		if db.Migrator().HasTable(&Outbox{}) {
			if err := db.Where("event_id IN (SELECT event_id FROM nova_usage_events)").Delete(&Outbox{}).Error; err != nil {
				return err
			}
		}
		if err := db.Migrator().DropTable("nova_usage_events"); err != nil {
			return err
		}
	}
	migrationDB := db
	if db.Dialector.Name() == "mysql" {
		migrationDB = db.Set("gorm:table_options", "CHARSET=utf8mb4")
	}
	return migrationDB.AutoMigrate(
		&Tenant{},
		&LogRef{},
		&Outbox{},
		&IdempotencyRecord{},
		&ReplayNonce{},
		&Attribution{},
		&QuotaOperation{},
	)
}
