package model

import "time"

func NewDeepSeek(cfg OpenAIConfig) *OpenAIProvider {
	if cfg.BaseURL == "" {
		cfg.BaseURL = "https://api.deepseek.com/v1"
	}
	if cfg.ModelID == "" {
		cfg.ModelID = "deepseek-chat"
	}
	if cfg.Timeout == 0 {
		cfg.Timeout = 60 * time.Second
	}
	return NewOpenAI(cfg)
}
