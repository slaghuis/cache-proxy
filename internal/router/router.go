package router

import (
	"net/http"

	"github.com/slaghuis/cache-proxy/internal/api"
	"github.com/slaghuis/cache-proxy/internal/config"
)

func Route(cfg *config.Config, req *api.ChatRequest, headers http.Header) string {
	promptLen := 0
	for _, m := range req.Messages {
		promptLen += len(m.Content)
	}
	tag := headers.Get("x-task-tag")
	modelHint := req.Model

	for _, rule := range cfg.Routing.Rules {
		if rule.When.ModelHintEquals != "" && rule.When.ModelHintEquals == modelHint {
			return rule.Model
		}
		if rule.When.HasTag != "" && rule.When.HasTag == tag {
			return rule.Model
		}
		if rule.When.MaxPromptChars > 0 && promptLen <= rule.When.MaxPromptChars {
			return rule.Model
		}
	}
	return cfg.Routing.DefaultModel
}