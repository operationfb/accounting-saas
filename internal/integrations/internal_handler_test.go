package integrations

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
)

// TestRequireWorkflowOIDC_Unconfigured: an empty service account → fail closed
// (503), without touching idtoken/network. It lives in-package so it can call the
// unexported requireWorkflowOIDC middleware directly. (The missing-token 401 and
// the wired routes are covered through the real router in the main package.)
func TestRequireWorkflowOIDC_Unconfigured(t *testing.T) {
	gin.SetMode(gin.TestMode)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request, _ = http.NewRequest(http.MethodGet, "/internal/v1/x", nil)

	requireWorkflowOIDC("")(c)

	if rec.Code != http.StatusServiceUnavailable {
		t.Errorf("unconfigured guard: expected 503, got %d", rec.Code)
	}
}
