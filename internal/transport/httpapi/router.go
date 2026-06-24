package httpapi

import (
	"log/slog"

	"github.com/gin-gonic/gin"
)

// NewRouter builds the Gin engine with the middleware chain and routes. The
// health endpoint is unauthenticated; the API routes require the bearer token
// when one is configured (empty token disables auth).
func NewRouter(h *Handlers, logger *slog.Logger, authToken string) *gin.Engine {
	gin.SetMode(gin.ReleaseMode)

	engine := gin.New()
	engine.MaxMultipartMemory = maxMultipartMemory
	engine.Use(requestID(), recovery(logger), accessLog(logger))

	engine.GET(RouteHealth, h.Health)

	api := engine.Group("", auth(authToken))
	api.POST(RouteSubmit, h.Submit)
	api.GET(RouteStatus, h.Status)

	return engine
}
