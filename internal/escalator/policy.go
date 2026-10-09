package escalator

import (
	"net/http"
	"strings"

	"github.com/slaghuis/cache-proxy/internal/config"
)

type Mode string

const (
	ModeAuto     Mode = "auto"
	ModeLocal    Mode = "local-only"
	ModeCloud    Mode = "cloud-only"
	ModeAlways   Mode = "escalate-always"
)

type Policy struct {
	Mode    Mode
	Profile config.EscalationProfile
	Tag     string
}

func ResolvePolicy(cfg *config.Config, headers http.Header) Policy {
	tag := headers.Get("x-task-tag")
	mode := Mode(strings.ToLower(headers.Get("x-escalation")))
	if mode == "" {
		mode = ModeAuto
	}
	return Policy{
		Mode:    mode,
		Profile: cfg.ProfileFor(tag),
		Tag:     tag,
	}
}

func (p Policy) ShouldEscalate() bool {
	return p.Mode == ModeAuto || p.Mode == ModeAlways
}

func (p Policy) LocalEligible() bool {
	return p.Mode == ModeAuto || p.Mode == ModeLocal
}

func (p Policy) ForceCloud() bool {
	return p.Mode == ModeCloud || p.Mode == ModeAlways
}