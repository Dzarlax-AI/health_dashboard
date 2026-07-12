package tenants

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"net/url"
	"regexp"
	"strconv"
	"strings"

	"github.com/google/uuid"
)

const minimumMasterSecretBytes = 32

var tenantRolePattern = regexp.MustCompile(`^[a-z][a-z0-9_]{0,62}$`)

type SecretVersion struct {
	Version int
	Secret  []byte
}

type CredentialDeriver struct {
	Current  SecretVersion
	Previous *SecretVersion
}

type TenantIsolationConfig struct {
	Enabled       bool
	AdminDSN      string
	RegistryDSN   string
	TenantDSNBase string
	Credentials   CredentialDeriver
}

func TenantRoleName(id uuid.UUID) string {
	return "health_t_" + strings.ReplaceAll(id.String(), "-", "")
}

func (d CredentialDeriver) Derive(id uuid.UUID, role string, version int) (string, error) {
	if err := d.validate(); err != nil {
		return "", err
	}
	if id == uuid.Nil {
		return "", errors.New("tenant ID must not be nil")
	}
	if !tenantRolePattern.MatchString(role) {
		return "", errors.New("tenant database role is not a valid lowercase PostgreSQL identifier")
	}
	secret, err := d.secretFor(version)
	if err != nil {
		return "", err
	}
	mac := hmac.New(sha256.New, secret)
	_, _ = mac.Write([]byte("health-tenant-db-v1\x00version=" + strconv.Itoa(version) + "\x00" + id.String() + "\x00" + role))
	return base64.RawURLEncoding.EncodeToString(mac.Sum(nil)), nil
}

func (d CredentialDeriver) validate() error {
	if err := validateSecretVersion("current", d.Current); err != nil {
		return err
	}
	if d.Previous == nil {
		return nil
	}
	if err := validateSecretVersion("previous", *d.Previous); err != nil {
		return err
	}
	if d.Previous.Version == d.Current.Version {
		return errors.New("current and previous master secret versions must differ")
	}
	return nil
}

func validateSecretVersion(label string, secret SecretVersion) error {
	if secret.Version <= 0 {
		return fmt.Errorf("%s master secret version must be positive", label)
	}
	if len(secret.Secret) < minimumMasterSecretBytes {
		return fmt.Errorf("%s master secret must decode to at least %d bytes", label, minimumMasterSecretBytes)
	}
	return nil
}

func (d CredentialDeriver) secretFor(version int) ([]byte, error) {
	if version == d.Current.Version {
		return d.Current.Secret, nil
	}
	if d.Previous != nil && version == d.Previous.Version {
		return d.Previous.Secret, nil
	}
	return nil, fmt.Errorf("master secret version %d is not configured", version)
}

// ParseTenantIsolationConfig parses and validates isolation settings without
// opening a database connection or performing any other side effect.
func ParseTenantIsolationConfig(lookupEnv func(string) (string, bool)) (TenantIsolationConfig, error) {
	var cfg TenantIsolationConfig
	enabledValue := envValue(lookupEnv, "TENANT_DB_ISOLATION_ENABLED")
	if enabledValue != "" {
		enabled, err := strconv.ParseBool(enabledValue)
		if err != nil {
			return cfg, errors.New("TENANT_DB_ISOLATION_ENABLED must be a boolean")
		}
		cfg.Enabled = enabled
	}
	if !cfg.Enabled {
		return cfg, nil
	}

	cfg.AdminDSN = envValue(lookupEnv, "ADMIN_DATABASE_URL")
	cfg.RegistryDSN = envValue(lookupEnv, "REGISTRY_DATABASE_URL")
	cfg.TenantDSNBase = envValue(lookupEnv, "TENANT_DATABASE_URL_BASE")
	for name, value := range map[string]string{
		"ADMIN_DATABASE_URL":       cfg.AdminDSN,
		"REGISTRY_DATABASE_URL":    cfg.RegistryDSN,
		"TENANT_DATABASE_URL_BASE": cfg.TenantDSNBase,
	} {
		if value == "" {
			return cfg, fmt.Errorf("%s is required when tenant database isolation is enabled", name)
		}
	}
	if err := validateTenantDSNBase(cfg.TenantDSNBase); err != nil {
		return cfg, fmt.Errorf("TENANT_DATABASE_URL_BASE: %w", err)
	}

	current, err := parseSecretVersion(lookupEnv, "TENANT_DB_MASTER_SECRET", "TENANT_DB_MASTER_SECRET_VERSION")
	if err != nil {
		return cfg, err
	}
	cfg.Credentials.Current = current
	previousSecret := envValue(lookupEnv, "TENANT_DB_PREVIOUS_MASTER_SECRET")
	previousVersion := envValue(lookupEnv, "TENANT_DB_PREVIOUS_MASTER_SECRET_VERSION")
	if previousSecret != "" || previousVersion != "" {
		previous, err := parseSecretVersion(lookupEnv, "TENANT_DB_PREVIOUS_MASTER_SECRET", "TENANT_DB_PREVIOUS_MASTER_SECRET_VERSION")
		if err != nil {
			return cfg, err
		}
		cfg.Credentials.Previous = &previous
	}
	if err := cfg.Credentials.validate(); err != nil {
		return cfg, err
	}
	return cfg, nil
}

var tenantDSNCredentialKeys = map[string]struct{}{
	"user":        {},
	"password":    {},
	"passfile":    {},
	"service":     {},
	"servicefile": {},
	"sslcert":     {},
	"sslkey":      {},
	"sslpassword": {},
}

func validateTenantDSNBase(raw string) error {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return errors.New("must not be empty")
	}
	for i := 0; i < len(raw); i++ {
		if raw[i] >= 0x80 {
			return errors.New("must contain ASCII characters only")
		}
		if raw[i] < 0x20 && !isASCIIWhitespace(raw[i]) {
			return errors.New("must not contain ASCII control characters")
		}
	}
	if strings.Contains(raw, "://") {
		if !strings.HasPrefix(raw, "postgres://") && !strings.HasPrefix(raw, "postgresql://") {
			return errors.New("must use the lowercase postgres or postgresql URL scheme")
		}
		parsed, err := url.Parse(raw)
		if err != nil {
			return errors.New("must be a valid PostgreSQL URL")
		}
		if parsed.Scheme != "postgres" && parsed.Scheme != "postgresql" {
			return errors.New("must use the postgres or postgresql URL scheme")
		}
		if parsed.User != nil {
			return errors.New("must not contain URL user information")
		}
		if parsed.Fragment != "" {
			return errors.New("must not contain a URL fragment")
		}
		if parsed.Hostname() == "" {
			return errors.New("must contain an explicit database host")
		}
		if strings.Trim(parsed.EscapedPath(), "/") == "" {
			return errors.New("must contain an explicit database name")
		}
		query, err := url.ParseQuery(parsed.RawQuery)
		if err != nil {
			return errors.New("must contain a valid URL query")
		}
		seenQueryKeys := make(map[string]struct{})
		for key, values := range query {
			if key == "" {
				return errors.New("must not contain an empty query parameter name")
			}
			normalizedKey := strings.ToLower(key)
			if key != normalizedKey {
				return fmt.Errorf("query parameter %q must be lowercase", key)
			}
			if _, duplicate := seenQueryKeys[normalizedKey]; duplicate {
				return fmt.Errorf("must not contain duplicate parameter %q", key)
			}
			seenQueryKeys[normalizedKey] = struct{}{}
			if len(values) != 1 {
				return fmt.Errorf("must not contain duplicate parameter %q", key)
			}
			if _, credential := tenantDSNCredentialKeys[normalizedKey]; credential {
				return fmt.Errorf("must not contain credential parameter %q", key)
			}
		}
	} else {
		values, err := parseKeywordDSN(raw)
		if err != nil {
			return err
		}
		for key := range values {
			if _, credential := tenantDSNCredentialKeys[strings.ToLower(key)]; credential {
				return fmt.Errorf("must not contain credential parameter %q", key)
			}
		}
		if values["host"] == "" && values["hostaddr"] == "" {
			return errors.New("must contain an explicit database host or hostaddr")
		}
		if values["dbname"] == "" {
			return errors.New("must contain an explicit database name")
		}
	}
	return nil
}

// parseKeywordDSN accepts the ASCII keyword/value subset used by deployments:
// whitespace-separated key=value pairs with libpq-style quotes and backslashes.
func parseKeywordDSN(raw string) (map[string]string, error) {
	values := make(map[string]string)
	for i := 0; i < len(raw); {
		for i < len(raw) && isASCIIWhitespace(raw[i]) {
			i++
		}
		if i == len(raw) {
			break
		}
		start := i
		for i < len(raw) && !isASCIIWhitespace(raw[i]) && raw[i] != '=' {
			i++
		}
		if start == i {
			return nil, errors.New("invalid PostgreSQL keyword/value connection string")
		}
		key := raw[start:i]
		if key != strings.ToLower(key) {
			return nil, fmt.Errorf("keyword parameter %q must be lowercase", key)
		}
		if _, duplicate := values[key]; duplicate {
			return nil, fmt.Errorf("must not contain duplicate parameter %q", key)
		}
		for i < len(raw) && isASCIIWhitespace(raw[i]) {
			i++
		}
		if i == len(raw) || raw[i] != '=' {
			return nil, errors.New("invalid PostgreSQL keyword/value connection string")
		}
		i++
		for i < len(raw) && isASCIIWhitespace(raw[i]) {
			i++
		}
		var value strings.Builder
		if i < len(raw) && raw[i] == '\'' {
			i++
			closed := false
			for i < len(raw) {
				switch raw[i] {
				case '\\':
					if i+1 >= len(raw) {
						return nil, errors.New("invalid PostgreSQL keyword/value connection string")
					}
					value.WriteByte(raw[i+1])
					i += 2
				case '\'':
					i++
					closed = true
				default:
					value.WriteByte(raw[i])
					i++
				}
				if closed {
					break
				}
			}
			if !closed {
				return nil, errors.New("invalid PostgreSQL keyword/value connection string")
			}
			if i < len(raw) && !isASCIIWhitespace(raw[i]) {
				return nil, errors.New("invalid PostgreSQL keyword/value connection string")
			}
		} else {
			for i < len(raw) && !isASCIIWhitespace(raw[i]) {
				if raw[i] == '\\' {
					if i+1 >= len(raw) {
						return nil, errors.New("invalid PostgreSQL keyword/value connection string")
					}
					value.WriteByte(raw[i+1])
					i += 2
				} else {
					value.WriteByte(raw[i])
					i++
				}
			}
		}
		values[key] = value.String()
	}
	return values, nil
}

func isASCIIWhitespace(value byte) bool {
	switch value {
	case ' ', '\t', '\n', '\r', '\v', '\f':
		return true
	default:
		return false
	}
}

func parseSecretVersion(lookupEnv func(string) (string, bool), secretName, versionName string) (SecretVersion, error) {
	encoded := envValue(lookupEnv, secretName)
	if encoded == "" {
		return SecretVersion{}, fmt.Errorf("%s is required when tenant database isolation is enabled", secretName)
	}
	secret, err := decodeSecret(encoded)
	if err != nil {
		return SecretVersion{}, fmt.Errorf("%s must be base64 encoded", secretName)
	}
	versionText := envValue(lookupEnv, versionName)
	version, err := strconv.Atoi(versionText)
	if err != nil || version <= 0 {
		return SecretVersion{}, fmt.Errorf("%s must be a positive integer", versionName)
	}
	return SecretVersion{Version: version, Secret: secret}, nil
}

func decodeSecret(encoded string) ([]byte, error) {
	for _, encoding := range []*base64.Encoding{base64.RawURLEncoding, base64.URLEncoding, base64.RawStdEncoding, base64.StdEncoding} {
		if decoded, err := encoding.DecodeString(encoded); err == nil {
			return decoded, nil
		}
	}
	return nil, errors.New("invalid base64")
}

func envValue(lookupEnv func(string) (string, bool), name string) string {
	value, _ := lookupEnv(name)
	return strings.TrimSpace(value)
}
