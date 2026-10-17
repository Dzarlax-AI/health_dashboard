package accessauth

import (
	"errors"
	"fmt"
	"net/url"
	"strings"
)

// Config is the opt-in Cloudflare Access browser authentication profile.
type Config struct {
	Issuer   string
	Audience string
	UserMap  map[string]string
	Origin   string
}

// ParseConfig reads the browser auth profile without making Cloudflare a
// dependency of the standalone API-key and local-password profile.
func ParseConfig(lookup func(string) (string, bool)) (*Config, error) {
	mode, _ := lookup("BROWSER_AUTH_MODE")
	if mode == "" || mode == "existing" {
		for _, name := range []string{"CLOUDFLARE_ACCESS_ISSUER", "CLOUDFLARE_ACCESS_AUD", "CLOUDFLARE_ACCESS_USER_MAP", "CLOUDFLARE_ACCESS_ORIGIN"} {
			if value, present := lookup(name); present && value != "" {
				return nil, fmt.Errorf("%s requires BROWSER_AUTH_MODE=cloudflare", name)
			}
		}
		return nil, nil
	}
	if mode != "cloudflare" {
		return nil, fmt.Errorf("unknown BROWSER_AUTH_MODE %q", mode)
	}
	get := func(name string) (string, error) {
		value, present := lookup(name)
		if !present || strings.TrimSpace(value) == "" {
			return "", fmt.Errorf("%s is required when BROWSER_AUTH_MODE=cloudflare", name)
		}
		if strings.TrimSpace(value) != value {
			return "", fmt.Errorf("%s must not contain surrounding whitespace", name)
		}
		return value, nil
	}
	issuer, err := get("CLOUDFLARE_ACCESS_ISSUER")
	if err != nil {
		return nil, err
	}
	audience, err := get("CLOUDFLARE_ACCESS_AUD")
	if err != nil {
		return nil, err
	}
	mapJSON, err := get("CLOUDFLARE_ACCESS_USER_MAP")
	if err != nil {
		return nil, err
	}
	origin, err := get("CLOUDFLARE_ACCESS_ORIGIN")
	if err != nil {
		return nil, err
	}
	users, err := ParseUserMap(mapJSON)
	if err != nil {
		return nil, fmt.Errorf("invalid CLOUDFLARE_ACCESS_USER_MAP: %w", err)
	}
	cfg := &Config{Issuer: issuer, Audience: audience, UserMap: users, Origin: origin}
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	return cfg, nil
}

func (c *Config) Validate() error {
	if c == nil {
		return errors.New("cloudflare access config is required")
	}
	if err := validateOrigin("CLOUDFLARE_ACCESS_ISSUER", c.Issuer); err != nil {
		return err
	}
	if err := validateOrigin("CLOUDFLARE_ACCESS_ORIGIN", c.Origin); err != nil {
		return err
	}
	if c.Audience == "" || strings.TrimSpace(c.Audience) != c.Audience {
		return errors.New("CLOUDFLARE_ACCESS_AUD must be a non-empty exact value")
	}
	if len(c.UserMap) == 0 {
		return errors.New("CLOUDFLARE_ACCESS_USER_MAP must contain at least one user")
	}
	for email, username := range c.UserMap {
		if email == "" || strings.TrimSpace(email) != email || username == "" || strings.TrimSpace(username) != username {
			return errors.New("CLOUDFLARE_ACCESS_USER_MAP entries must have exact non-empty email and username values")
		}
	}
	return nil
}

func validateOrigin(name, value string) error {
	u, err := url.Parse(value)
	if err != nil || u.Scheme != "https" || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || u.Path != "" || u.Opaque != "" {
		return fmt.Errorf("%s must be an exact HTTPS origin", name)
	}
	if strings.TrimSpace(value) != value || u.String() != value {
		return fmt.Errorf("%s must use canonical origin spelling", name)
	}
	return nil
}
