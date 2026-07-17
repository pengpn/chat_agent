package middleware

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"strings"
	"time"

	"github.com/example/agent-platform/backend/internal/logger"
	"github.com/gin-gonic/gin"
)

const RequestIDHeader = "X-Request-ID"

// RequestID accepts a safe caller-provided identifier or creates a new one.
// The id is returned to the caller and placed on the request context so all
// logs emitted while handling the request can be correlated.
func RequestID() gin.HandlerFunc {
	return func(c *gin.Context) {
		requestID := strings.TrimSpace(c.GetHeader(RequestIDHeader))
		if !validRequestID(requestID) {
			requestID = newRequestID()
		}
		c.Set(RequestIDHeader, requestID)
		c.Header(RequestIDHeader, requestID)
		c.Request = c.Request.WithContext(logger.WithRequestID(c.Request.Context(), requestID))
		c.Next()
	}
}

func newRequestID() string {
	var bytes [16]byte
	if _, err := rand.Read(bytes[:]); err == nil {
		return hex.EncodeToString(bytes[:])
	}
	// crypto/rand failures are exceptionally rare. Keep requests traceable if
	// one does occur instead of failing an otherwise valid HTTP request.
	return fmt.Sprintf("fallback-%x", time.Now().UnixNano())
}

func validRequestID(value string) bool {
	if len(value) == 0 || len(value) > 128 {
		return false
	}
	for _, char := range value {
		if (char >= 'a' && char <= 'z') || (char >= 'A' && char <= 'Z') || (char >= '0' && char <= '9') || char == '-' || char == '_' || char == '.' {
			continue
		}
		return false
	}
	return true
}
