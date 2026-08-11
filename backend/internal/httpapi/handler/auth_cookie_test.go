package handler

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
)

func TestRefreshCookieUsesHttpOnlySameSiteAndConfiguredSecureFlag(t *testing.T) {
	gin.SetMode(gin.TestMode)
	response := httptest.NewRecorder()
	context, _ := gin.CreateTestContext(response)
	handler := &Handler{}
	handler.ConfigureAuthCookies(AuthCookieConfig{Name: "refresh_test", Secure: true})
	handler.setRefreshCookie(context, "secret", time.Now().Add(time.Hour))

	result := response.Result()
	cookies := result.Cookies()
	if len(cookies) != 1 {
		t.Fatalf("cookies = %d, want 1", len(cookies))
	}
	cookie := cookies[0]
	if !cookie.HttpOnly || !cookie.Secure || cookie.SameSite != http.SameSiteLaxMode {
		t.Fatalf("cookie security flags = %+v", cookie)
	}
	if cookie.Path != "/api/v1/auth" || cookie.MaxAge <= 0 {
		t.Fatalf("cookie path/max-age = %q/%d", cookie.Path, cookie.MaxAge)
	}
}
