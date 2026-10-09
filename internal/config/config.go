package config

import (
	"os"
	"sync/atomic"

	"gopkg.in/yaml.v3"
)

type EscalationConfig struct {
    Enabled             bool                        `yaml:"enabled"`
    LocalTimeoutSeconds int                         `yaml:"local_timeout_seconds"`
    DefaultProfile      EscalationProfile           `yaml:"default_profile"`
    Profiles            map[string]EscalationProfile `yaml:"profiles"`
}

type EscalationProfile struct {
    LocalModel            string  `yaml:"local_model"`
    CloudModel            string  `yaml:"cloud_model"`
    Threshold             float64 `yaml:"threshold"`
    ExpectedOutputTokens  int     `yaml:"expected_output_tokens"`
    EnableCompileCheck    bool    `yaml:"enable_compile_check"`
    EnableSimilarityCheck bool    `yaml:"enable_similarity_check"`
    SimilarityFloor       float64 `yaml:"similarity_floor"`
}

type Config struct {
	Listen string `yaml:"listen"`

	Upstream struct {
		BaseURL string `yaml:"base_url"`
		APIKey  string `yaml:"api_key"`
	} `yaml:"upstream"`

	Qdrant struct {
		Host       string `yaml:"host"`
		Port       int    `yaml:"port"`
		Collection string `yaml:"collection"`
	} `yaml:"qdrant"`

	Ollama struct {
		BaseURL string `yaml:"base_url"`
		Model   string `yaml:"model"`
	} `yaml:"ollama"`

	Cache struct {
		SQLitePath         string  `yaml:"sqlite_path"`
		SemanticThreshold  float32 `yaml:"semantic_threshold"`
		Enabled            bool    `yaml:"enabled"`
		MaxPromptChars     int     `yaml:"max_prompt_chars"`
		TTLHours           int     `yaml:"ttl_hours"`
	} `yaml:"cache"`

	Routing struct {
		DefaultModel string         `yaml:"default_model"`
		Rules        []RoutingRule  `yaml:"rules"`
	} `yaml:"routing"`

	Pricing map[string]ModelPricing `yaml:"pricing"`

	Escalation EscalationConfig `yaml:"escalation"`
}

type RoutingRule struct {
	When  RuleCondition `yaml:"when"`
	Model string        `yaml:"model"`
}

type RuleCondition struct {
	ModelHintEquals string `yaml:"model_hint_equals"`
	MaxPromptChars  int    `yaml:"max_prompt_chars"`
	HasTag          string `yaml:"has_tag"`
}

type ModelPricing struct {
	InputPer1M  float64 `yaml:"input_per_1m"`
	OutputPer1M float64 `yaml:"output_per_1m"`
}

var current atomic.Pointer[Config]

func Load(path string) (*Config, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var c Config
	if err := yaml.Unmarshal(b, &c); err != nil {
		return nil, err
	}
	applyDefaults(&c)
	current.Store(&c)
	return &c, nil
}

func Current() *Config { return current.Load() }

func applyDefaults(c *Config) {
	if c.Listen == "" {
		c.Listen = ":8080"
	}
	if c.Cache.SemanticThreshold == 0 {
		c.Cache.SemanticThreshold = 0.95
	}
	if c.Cache.MaxPromptChars == 0 {
		c.Cache.MaxPromptChars = 24000
	}
	if c.Cache.TTLHours == 0 {
		c.Cache.TTLHours = 168
	}
    	if c.Escalation.LocalTimeoutSeconds == 0 {
        	c.Escalation.LocalTimeoutSeconds = 30
    	}
    	if c.Escalation.DefaultProfile.Threshold == 0 {
        	c.Escalation.DefaultProfile.Threshold = 0.65
    	}
    	if c.Escalation.DefaultProfile.ExpectedOutputTokens == 0 {
        	c.Escalation.DefaultProfile.ExpectedOutputTokens = 400
    	}
    	if c.Escalation.DefaultProfile.SimilarityFloor == 0 {
        	c.Escalation.DefaultProfile.SimilarityFloor = 0.35
    	}
}

// ProfileFor resolves a profile by task tag, falling back to default.
func (c *Config) ProfileFor(tag string) EscalationProfile {
    if p, ok := c.Escalation.Profiles[tag]; ok {
        // Fill unset fields from default
        if p.LocalModel == "" {
            p.LocalModel = c.Escalation.DefaultProfile.LocalModel
        }
        if p.CloudModel == "" {
            p.CloudModel = c.Escalation.DefaultProfile.CloudModel
        }
        if p.Threshold == 0 {
            p.Threshold = c.Escalation.DefaultProfile.Threshold
        }
        if p.ExpectedOutputTokens == 0 {
            p.ExpectedOutputTokens = c.Escalation.DefaultProfile.ExpectedOutputTokens
        }
        return p
    }
    return c.Escalation.DefaultProfile
}
