package oauth

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strings"
)

// Config is the opt-in OAuth resource-server configuration. SubjectMap maps
// exact issuer subjects to existing Health usernames; token profile fields
// never select a tenant.
type Config struct {
	Issuer     string
	JWKSURL    string
	Resource   string
	ReadScope  string
	SubjectMap map[string]string
}

// ConfigFromEnv leaves OAuth disabled when all MCP_OAUTH_* values are absent.
// A partial configuration fails startup instead of weakening authentication.
func ConfigFromEnv(getenv func(string) string) (*Config, error) {
	fields := []string{"MCP_OAUTH_ISSUER", "MCP_OAUTH_JWKS_URL", "MCP_OAUTH_RESOURCE", "MCP_OAUTH_READ_SCOPE", "MCP_OAUTH_SUBJECT_MAP"}
	values := make([]string, len(fields))
	active := false
	for i, name := range fields {
		values[i] = getenv(name)
		active = active || values[i] != ""
	}
	if !active {
		return nil, nil
	}
	for i, name := range fields {
		if values[i] == "" {
			return nil, fmt.Errorf("%s is required when MCP OAuth is enabled", name)
		}
	}
	cfg := &Config{Issuer: values[0], JWKSURL: values[1], Resource: values[2], ReadScope: values[3]}
	if err := json.Unmarshal([]byte(values[4]), &cfg.SubjectMap); err != nil {
		return nil, fmt.Errorf("MCP_OAUTH_SUBJECT_MAP must be a JSON object: %w", err)
	}
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	return cfg, nil
}

func (c *Config) Validate() error {
	if c == nil {
		return errors.New("nil OAuth configuration")
	}
	if err := validateHTTPSURL("MCP_OAUTH_ISSUER", c.Issuer); err != nil {
		return err
	}
	if err := validateHTTPSURL("MCP_OAUTH_JWKS_URL", c.JWKSURL); err != nil {
		return err
	}
	if err := validateHTTPSURL("MCP_OAUTH_RESOURCE", c.Resource); err != nil {
		return err
	}
	u, _ := url.Parse(c.Resource)
	if u.Path != "/mcp" {
		return errors.New("MCP_OAUTH_RESOURCE must identify the public /mcp endpoint")
	}
	if c.ReadScope == "" || strings.ContainsAny(c.ReadScope, " \t\r\n\"\\") {
		return errors.New("MCP_OAUTH_READ_SCOPE must be one nonempty scope token")
	}
	if len(c.SubjectMap) == 0 {
		return errors.New("MCP_OAUTH_SUBJECT_MAP must map at least one subject")
	}
	for subject, username := range c.SubjectMap {
		if subject == "" || strings.TrimSpace(subject) != subject || username == "" || strings.TrimSpace(username) != username {
			return errors.New("MCP_OAUTH_SUBJECT_MAP contains an empty or padded subject or username")
		}
	}
	return nil
}
