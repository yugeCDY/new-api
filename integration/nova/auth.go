package nova

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/logger"
	"github.com/QuantumNous/new-api/service"
	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

const (
	headerKeyID     = "X-Nova-Key-Id"
	headerTimestamp = "X-Nova-Timestamp"
	headerNonce     = "X-Nova-Nonce"
	headerSignature = "X-Nova-Signature"
)

func HMACAuth() gin.HandlerFunc {
	return func(c *gin.Context) {
		config, db := currentState()
		if !config.Enabled || db == nil {
			c.AbortWithStatusJSON(http.StatusNotFound, gin.H{"success": false, "message": "not found"})
			return
		}

		body, bodyErr := io.ReadAll(c.Request.Body)
		if bodyErr != nil {
			rejectAuthentication(c, "body")
			return
		}
		c.Request.Body = io.NopCloser(bytes.NewReader(body))
		bodyDigest := sha256.Sum256(body)
		c.Set("nova_body_hash", hex.EncodeToString(bodyDigest[:]))
		if !verifyAndReserve(c, config, db, hex.EncodeToString(bodyDigest[:])) {
			return
		}

		c.Set("nova_authenticated", true)
		c.Set("nova_key_id", c.GetHeader(headerKeyID))
		c.Next()
	}
}

// RelayAttribution requires a valid Nova signature only for tokens owned by a
// Nova tenant. Existing non-Nova tokens retain their current behavior.
func RelayAttribution() gin.HandlerFunc {
	return func(c *gin.Context) {
		config, db := currentState()
		if !config.Enabled || db == nil {
			c.Next()
			return
		}
		userID := c.GetInt("id")
		tokenID := c.GetInt("token_id")
		var tenant Tenant
		err := db.Where("user_id = ?", userID).First(&tenant).Error
		if errors.Is(err, gorm.ErrRecordNotFound) {
			c.Next()
			return
		}
		if err != nil || tenant.Status != tenantStatusEnabled {
			c.AbortWithStatusJSON(http.StatusForbidden, gin.H{"success": false, "code": "nova_tenant_unavailable", "message": "tenant is unavailable"})
			return
		}

		storage, err := common.GetBodyStorage(c)
		if err != nil {
			rejectAuthentication(c, "body")
			return
		}
		digest := sha256.New()
		if _, err := storage.Seek(0, io.SeekStart); err != nil {
			rejectAuthentication(c, "body")
			return
		}
		if _, err := io.Copy(digest, storage); err != nil {
			rejectAuthentication(c, "body")
			return
		}
		if _, err := storage.Seek(0, io.SeekStart); err != nil {
			rejectAuthentication(c, "body")
			return
		}
		reader, err := storage.NewReader()
		if err != nil {
			rejectAuthentication(c, "body")
			return
		}
		c.Request.Body = reader
		bodyHash := hex.EncodeToString(digest.Sum(nil))
		if c.GetHeader("X-Nova-Tenant-Key") != tenant.TenantKey || !verifyAndReserve(c, config, db, bodyHash) {
			if !c.IsAborted() {
				rejectAuthentication(c, "tenant")
			}
			return
		}
		service.SetUsageAttribution(c, service.UsageAttribution{Provider: "nova", Subject: tenant.TenantKey, RequestID: c.GetHeader("X-Nova-Request-Id"), Verified: true})
		c.Set("nova_token_id", tokenID)
		requestID := c.GetString(common.RequestIdKey)
		if requestID != "" {
			service.RecordUsageAttribution(c, "relay", requestID, userID, tokenID)
		}
		c.Next()
		if requestID != "" && c.Writer.Status() >= http.StatusBadRequest {
			_ = db.Where("source_type = ? AND source_key = ?", "relay", requestID).Delete(&Attribution{}).Error
		}
	}
}

func verifyAndReserve(c *gin.Context, config Config, db *gorm.DB, bodyHash string) bool {
	keyID := c.GetHeader(headerKeyID)
	timestampRaw := c.GetHeader(headerTimestamp)
	nonce := c.GetHeader(headerNonce)
	signatureRaw := c.GetHeader(headerSignature)
	secret, keyExists := config.Keys[keyID]
	timestamp, timestampErr := strconv.ParseInt(timestampRaw, 10, 64)
	now := time.Now().Unix()
	validTimestamp := timestampErr == nil && timestamp >= now-int64(config.MaxSkew/time.Second) && timestamp <= now+int64(config.MaxSkew/time.Second)
	providedSignature, signatureErr := hex.DecodeString(signatureRaw)
	validSignatureEncoding := signatureErr == nil && len(providedSignature) == sha256.Size
	verificationSecret := secret
	if !keyExists {
		verificationSecret = make([]byte, sha256.Size)
	}
	expected := calculateSignature(verificationSecret, canonicalRequestWithBodyHash(c.Request, timestampRaw, nonce, bodyHash))
	validSignature := validTimestamp && validNonceValue(nonce) && validSignatureEncoding && subtle.ConstantTimeCompare(expected, providedSignature) == 1
	if !validSignature {
		rejectAuthentication(c, "credentials")
		return false
	}

	nonceDigest := sha256.Sum256([]byte(nonce))
	replayNonce := ReplayNonce{KeyID: keyID, NonceHash: hex.EncodeToString(nonceDigest[:]), RequestTimestamp: timestamp, ExpiresAt: now + int64(config.NonceTTL/time.Second), CreatedAt: now}
	if err := db.Create(&replayNonce).Error; err != nil {
		rejectAuthentication(c, "replay")
		return false
	}
	return true
}

func validNonceValue(nonce string) bool {
	if len(nonce) < 22 || len(nonce) > 128 {
		return false
	}
	for _, char := range nonce {
		if char >= 'a' && char <= 'z' || char >= 'A' && char <= 'Z' || char >= '0' && char <= '9' || char == '-' || char == '_' {
			continue
		}
		return false
	}
	return true
}

func canonicalRequest(request *http.Request, timestamp, nonce string, body []byte) string {
	bodyDigest := sha256.Sum256(body)
	return canonicalRequestWithBodyHash(request, timestamp, nonce, hex.EncodeToString(bodyDigest[:]))
}

func canonicalRequestWithBodyHash(request *http.Request, timestamp, nonce, bodyHash string) string {
	path := request.URL.EscapedPath()
	if path == "" {
		path = "/"
	}
	return strings.Join([]string{
		strings.ToUpper(request.Method),
		path,
		canonicalQuery(request.URL.Query()),
		timestamp,
		nonce,
		bodyHash,
	}, "\n")
}

func canonicalQuery(values url.Values) string {
	canonical := make(url.Values, len(values))
	for key, entries := range values {
		copied := append([]string(nil), entries...)
		sort.Strings(copied)
		canonical[key] = copied
	}
	return canonical.Encode()
}

func calculateSignature(secret []byte, canonical string) []byte {
	mac := hmac.New(sha256.New, secret)
	_, _ = mac.Write([]byte(canonical))
	return mac.Sum(nil)
}

func rejectAuthentication(c *gin.Context, reason string) {
	logger.LogWarn(c, fmt.Sprintf("Nova service authentication rejected: %s", reason))
	c.Header("Cache-Control", "no-store")
	c.AbortWithStatusJSON(http.StatusUnauthorized, failureBody(c, "nova_authentication_failed", "service authentication failed"))
}
