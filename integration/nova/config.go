package nova

import (
	"encoding/base64"
	"errors"
	"fmt"
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"
)

const (
	defaultMaxSkew  = 5 * time.Minute
	defaultNonceTTL = 10 * time.Minute
	maxHMACKeys     = 2
)

var keyIDPattern = regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`)

type Config struct {
	Enabled         bool
	Keys            map[string][]byte
	CurrentKeyID    string
	MaxSkew         time.Duration
	NonceTTL        time.Duration
	RabbitMQURL     string
	Exchange        string
	RoutingKey      string
	PublishTimeout  time.Duration
	PollInterval    time.Duration
	BatchSize       int
	MaxAttempts     int
	OutboxRetention time.Duration
	DeadRetention   time.Duration
	EventRetention  time.Duration
}

func loadConfig() (Config, error) {
	enabled, err := parseBoolEnv("NOVA_INTEGRATION_ENABLED", false)
	if err != nil {
		return Config{}, err
	}
	config := Config{
		Enabled:         enabled,
		MaxSkew:         defaultMaxSkew,
		NonceTTL:        defaultNonceTTL,
		RabbitMQURL:     strings.TrimSpace(os.Getenv("NOVA_RABBITMQ_URL")),
		Exchange:        envOrDefault("NOVA_MQ_EXCHANGE", "nova.events"),
		RoutingKey:      envOrDefault("NOVA_MQ_ROUTING_KEY", "nova.usage.reported"),
		PublishTimeout:  5 * time.Second,
		PollInterval:    time.Second,
		BatchSize:       100,
		MaxAttempts:     20,
		OutboxRetention: 30 * 24 * time.Hour,
		DeadRetention:   90 * 24 * time.Hour,
		EventRetention:  180 * 24 * time.Hour,
	}
	if !enabled {
		return config, nil
	}

	maxSkewSeconds, err := parsePositiveIntEnv("NOVA_HMAC_MAX_SKEW_SECONDS", int(defaultMaxSkew/time.Second))
	if err != nil || maxSkewSeconds > 3600 {
		return Config{}, errors.New("NOVA_HMAC_MAX_SKEW_SECONDS must be between 1 and 3600")
	}
	nonceTTLSeconds, err := parsePositiveIntEnv("NOVA_NONCE_TTL_SECONDS", int(defaultNonceTTL/time.Second))
	if err != nil || nonceTTLSeconds > 86400 {
		return Config{}, errors.New("NOVA_NONCE_TTL_SECONDS must be between 1 and 86400")
	}
	if nonceTTLSeconds < maxSkewSeconds {
		return Config{}, errors.New("NOVA_NONCE_TTL_SECONDS must be greater than or equal to NOVA_HMAC_MAX_SKEW_SECONDS")
	}
	config.MaxSkew = time.Duration(maxSkewSeconds) * time.Second
	config.NonceTTL = time.Duration(nonceTTLSeconds) * time.Second
	pollMilliseconds, err := parsePositiveIntEnv("NOVA_OUTBOX_POLL_INTERVAL_MS", 1000)
	if err != nil || pollMilliseconds > 60000 {
		return Config{}, errors.New("NOVA_OUTBOX_POLL_INTERVAL_MS must be between 1 and 60000")
	}
	batchSize, err := parsePositiveIntEnv("NOVA_OUTBOX_BATCH_SIZE", 100)
	if err != nil || batchSize > 1000 {
		return Config{}, errors.New("NOVA_OUTBOX_BATCH_SIZE must be between 1 and 1000")
	}
	maxAttempts, err := parsePositiveIntEnv("NOVA_OUTBOX_MAX_ATTEMPTS", 20)
	if err != nil || maxAttempts > 1000 {
		return Config{}, errors.New("NOVA_OUTBOX_MAX_ATTEMPTS must be between 1 and 1000")
	}
	config.PollInterval = time.Duration(pollMilliseconds) * time.Millisecond
	publishTimeoutSeconds, err := parsePositiveIntEnv("NOVA_MQ_PUBLISH_TIMEOUT_SECONDS", 5)
	if err != nil || publishTimeoutSeconds > 300 {
		return Config{}, errors.New("NOVA_MQ_PUBLISH_TIMEOUT_SECONDS must be between 1 and 300")
	}
	config.PublishTimeout = time.Duration(publishTimeoutSeconds) * time.Second
	config.BatchSize = batchSize
	config.MaxAttempts = maxAttempts
	outboxRetentionDays, err := parsePositiveIntEnv("NOVA_OUTBOX_RETENTION_DAYS", 30)
	if err != nil || outboxRetentionDays > 3650 {
		return Config{}, errors.New("NOVA_OUTBOX_RETENTION_DAYS must be between 1 and 3650")
	}
	deadRetentionDays, err := parsePositiveIntEnv("NOVA_OUTBOX_DEAD_RETENTION_DAYS", 90)
	if err != nil || deadRetentionDays > 3650 {
		return Config{}, errors.New("NOVA_OUTBOX_DEAD_RETENTION_DAYS must be between 1 and 3650")
	}
	eventRetentionDays, err := parsePositiveIntEnv("NOVA_EVENT_RETENTION_DAYS", 180)
	if err != nil || eventRetentionDays > 3650 {
		return Config{}, errors.New("NOVA_EVENT_RETENTION_DAYS must be between 1 and 3650")
	}
	config.OutboxRetention = time.Duration(outboxRetentionDays) * 24 * time.Hour
	config.DeadRetention = time.Duration(deadRetentionDays) * 24 * time.Hour
	config.EventRetention = time.Duration(eventRetentionDays) * 24 * time.Hour

	keys, currentKeyID, err := parseHMACKeys(os.Getenv("NOVA_HMAC_KEYS"))
	if err != nil {
		return Config{}, err
	}
	config.Keys = keys
	config.CurrentKeyID = currentKeyID
	return config, nil
}

func envOrDefault(name, defaultValue string) string {
	value := strings.TrimSpace(os.Getenv(name))
	if value == "" {
		return defaultValue
	}
	return value
}

func parseBoolEnv(name string, defaultValue bool) (bool, error) {
	raw := strings.TrimSpace(os.Getenv(name))
	if raw == "" {
		return defaultValue, nil
	}
	value, err := strconv.ParseBool(raw)
	if err != nil {
		return false, fmt.Errorf("%s must be a boolean", name)
	}
	return value, nil
}

func parsePositiveIntEnv(name string, defaultValue int) (int, error) {
	raw := strings.TrimSpace(os.Getenv(name))
	if raw == "" {
		return defaultValue, nil
	}
	value, err := strconv.Atoi(raw)
	if err != nil || value <= 0 {
		return 0, fmt.Errorf("%s must be a positive integer", name)
	}
	return value, nil
}

// NOVA_HMAC_KEYS is an ordered comma-separated list of key-id:base64url-secret
// entries. The first entry signs new requests; at most one previous key is
// accepted during rotation. Each decoded key must contain at least 256 bits.
func parseHMACKeys(raw string) (map[string][]byte, string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, "", errors.New("NOVA_HMAC_KEYS is required when Nova integration is enabled")
	}
	entries := strings.Split(raw, ",")
	if len(entries) > maxHMACKeys {
		return nil, "", fmt.Errorf("NOVA_HMAC_KEYS supports at most %d keys", maxHMACKeys)
	}

	keys := make(map[string][]byte, len(entries))
	currentKeyID := ""
	for i, entry := range entries {
		keyID, encodedSecret, ok := strings.Cut(strings.TrimSpace(entry), ":")
		if !ok || !keyIDPattern.MatchString(keyID) || encodedSecret == "" {
			return nil, "", errors.New("NOVA_HMAC_KEYS contains an invalid entry")
		}
		if _, exists := keys[keyID]; exists {
			return nil, "", errors.New("NOVA_HMAC_KEYS contains a duplicate key id")
		}
		secret, err := base64.RawURLEncoding.DecodeString(encodedSecret)
		if err != nil {
			secret, err = base64.URLEncoding.DecodeString(encodedSecret)
		}
		if err != nil || len(secret) < 32 {
			return nil, "", errors.New("NOVA_HMAC_KEYS secrets must be base64url encoded and at least 32 bytes")
		}
		keys[keyID] = secret
		if i == 0 {
			currentKeyID = keyID
		}
	}
	return keys, currentKeyID, nil
}
