package api

import "github.com/slaghuis/cache-proxy/internal/types"

// Wire types live in internal/types so cache and router can use them
// without importing api (which imports both).
type (
	ChatRequest     = types.ChatRequest
	Message         = types.Message
	ChatResponse    = types.ChatResponse
	Choice          = types.Choice
	ResponseMessage = types.ResponseMessage
	Usage           = types.Usage
)
