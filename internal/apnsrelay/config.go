package apnsrelay

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"

	"github.com/google/uuid"

	"health-receiver/internal/apnspush"
	"health-receiver/internal/storage"
)

const (
	AppIDHealthSync         = "health_sync"
	DefaultHealthAppleAppID = "EMVJLWPLVL.com.dzarlax.health-sync"
)

// PayloadPolicy encapsulates app-specific allowlist validation and neutral
// APNs payload generation so one Relay core can serve multiple iOS apps.
type PayloadPolicy interface {
	ValidateDelivery(kind, locale string, contractVersion int) error
	BuildPayload(kind, locale string, itemID, regID uuid.UUID) ([]byte, error)
	BuildCollapseID(kind string, itemID uuid.UUID) string
}

// HealthSyncPayloadPolicy enforces the strict Health Sync neutral push allowlist.
type HealthSyncPayloadPolicy struct{}

func (HealthSyncPayloadPolicy) ValidateDelivery(kind, locale string, contractVersion int) error {
	if !storage.IsValidInboxKind(kind) {
		return errors.New("unsupported push kind")
	}
	if !storage.ValidPushLocale(locale) {
		return errors.New("unsupported locale")
	}
	if storage.RequiredInboxContractVersion(kind) > contractVersion {
		return ErrKindUnsupportedForGrant
	}
	return nil
}

func (HealthSyncPayloadPolicy) BuildPayload(kind, locale string, itemID, regID uuid.UUID) ([]byte, error) {
	return apnspush.BuildPayloadForKind(kind, locale, itemID, regID)
}

// BuildCollapseID builds an opaque APNs collapse ID containing only the event
// kind and opaque item UUID (<= 57 bytes, strictly <= Apple's 64-byte limit,
// and never containing local dates or user-provided text).
func (HealthSyncPayloadPolicy) BuildCollapseID(kind string, itemID uuid.UUID) string {
	return kind + ":" + itemID.String()
}

var (
	ErrKindUnsupportedForGrant = errors.New("kind_unsupported_for_grant")
)

// AppConfig defines one iOS application served by the multi-app Relay core.
type AppConfig struct {
	AppID            string
	AppleAppID       string
	APNsTopic        string
	AllowSandbox     bool
	ProductionSender apnspush.TransportSender
	SandboxSender    apnspush.TransportSender
	Policy           PayloadPolicy
}

func (a *AppConfig) SenderForEnv(env string) apnspush.TransportSender {
	if a == nil {
		return nil
	}
	switch env {
	case storage.PushEnvProduction:
		return a.ProductionSender
	case storage.PushEnvSandbox:
		if !a.AllowSandbox {
			return nil
		}
		if a.SandboxSender != nil {
			return a.SandboxSender
		}
		return a.ProductionSender
	default:
		return nil
	}
}

func (a *AppConfig) AllowsEnvForAttest(attestEnv, apnsEnv string) bool {
	if a == nil {
		return false
	}
	switch attestEnv {
	case AttestEnvProduction:
		return apnsEnv == storage.PushEnvProduction && a.ProductionSender != nil
	case AttestEnvDevelopment:
		return a.AllowSandbox && apnsEnv == storage.PushEnvSandbox && a.SenderForEnv(storage.PushEnvSandbox) != nil
	default:
		return false
	}
}

// Registry maps app_id -> AppConfig.
type Registry struct {
	apps map[string]*AppConfig
}

func NewRegistry(apps ...*AppConfig) (*Registry, error) {
	r := &Registry{apps: make(map[string]*AppConfig, len(apps))}
	for _, app := range apps {
		if app == nil {
			continue
		}
		app.AppID = strings.TrimSpace(app.AppID)
		app.AppleAppID = strings.TrimSpace(app.AppleAppID)
		app.APNsTopic = strings.TrimSpace(app.APNsTopic)
		if app.AppID == "" || app.AppleAppID == "" || app.APNsTopic == "" || app.Policy == nil {
			return nil, fmt.Errorf("invalid app config for %q", app.AppID)
		}
		if app.ProductionSender == nil && (!app.AllowSandbox || app.SandboxSender == nil) {
			return nil, fmt.Errorf("app %q has no APNs sender configured", app.AppID)
		}
		if _, dup := r.apps[app.AppID]; dup {
			return nil, fmt.Errorf("duplicate app_id %q", app.AppID)
		}
		r.apps[app.AppID] = app
	}
	if len(r.apps) == 0 {
		return nil, errors.New("at least one app must be configured in APNs relay")
	}
	return r, nil
}

func (r *Registry) Get(appID string) (*AppConfig, bool) {
	if r == nil {
		return nil, false
	}
	app, ok := r.apps[strings.TrimSpace(appID)]
	return app, ok
}

type fileAppConfig struct {
	AppID                     string `json:"app_id"`
	AppleAppID                string `json:"apple_app_id"`
	APNsTopic                 string `json:"apns_topic"`
	Policy                    string `json:"policy"`
	AllowSandbox              bool   `json:"allow_sandbox"`
	ProductionCredentialsFile string `json:"production_credentials_file,omitempty"`
	ProductionCredentialsJSON string `json:"production_credentials_json,omitempty"`
	SandboxCredentialsFile    string `json:"sandbox_credentials_file,omitempty"`
	SandboxCredentialsJSON    string `json:"sandbox_credentials_json,omitempty"`
}

// LoadRegistryFromEnv loads the Relay multi-app configuration either from
// APNS_RELAY_APPS_FILE / APNS_RELAY_APPS_JSON or from single-app Health Sync
// environment variables (APNS_CREDENTIALS_* / APNS_KEY_ID+APNS_TEAM_ID+APNS_PRIVATE_KEY*).
func LoadRegistryFromEnv() (*Registry, error) {
	appsJSON := strings.TrimSpace(os.Getenv("APNS_RELAY_APPS_JSON"))
	appsFile := strings.TrimSpace(os.Getenv("APNS_RELAY_APPS_FILE"))
	if appsJSON == "" && appsFile != "" {
		raw, err := os.ReadFile(appsFile)
		if err != nil {
			return nil, fmt.Errorf("read APNS_RELAY_APPS_FILE: %w", err)
		}
		appsJSON = string(raw)
	}
	if appsJSON != "" {
		var specs []fileAppConfig
		if err := json.Unmarshal([]byte(appsJSON), &specs); err != nil {
			return nil, fmt.Errorf("parse relay apps config: %w", err)
		}
		apps := make([]*AppConfig, 0, len(specs))
		for _, spec := range specs {
			policy, err := resolvePolicy(spec.Policy, spec.AppID)
			if err != nil {
				return nil, err
			}
			prodSender, err := loadSenderForTopic(spec.APNsTopic, spec.ProductionCredentialsJSON, spec.ProductionCredentialsFile)
			if err != nil {
				return nil, fmt.Errorf("app %s production sender: %w", spec.AppID, err)
			}
			sandboxSender, err := loadSenderForTopic(spec.APNsTopic, spec.SandboxCredentialsJSON, spec.SandboxCredentialsFile)
			if err != nil {
				return nil, fmt.Errorf("app %s sandbox sender: %w", spec.AppID, err)
			}
			apps = append(apps, &AppConfig{
				AppID:            spec.AppID,
				AppleAppID:       spec.AppleAppID,
				APNsTopic:        spec.APNsTopic,
				AllowSandbox:     spec.AllowSandbox,
				ProductionSender: prodSender,
				SandboxSender:    sandboxSender,
				Policy:           policy,
			})
		}
		return NewRegistry(apps...)
	}

	// Single-app default mode: Health Sync.
	prodSender, err := apnspush.LoadSenderFromEnv()
	if err != nil {
		return nil, err
	}
	if prodSender == nil {
		return nil, errors.New("APNS credentials are required for APNs relay")
	}
	topic := prodSender.EffectiveTopic()
	appleAppID := strings.TrimSpace(os.Getenv("APNS_RELAY_APPLE_APP_ID"))
	if appleAppID == "" {
		appleAppID = DefaultHealthAppleAppID
	}
	allowSandbox := false
	if raw := strings.TrimSpace(os.Getenv("APNS_RELAY_ALLOW_SANDBOX")); raw != "" {
		parsed, parseErr := strconv.ParseBool(raw)
		if parseErr != nil {
			return nil, fmt.Errorf("invalid APNS_RELAY_ALLOW_SANDBOX: %w", parseErr)
		}
		allowSandbox = parsed
	}
	sandboxSender, err := loadSenderForTopic(
		topic,
		strings.TrimSpace(os.Getenv("APNS_SANDBOX_CREDENTIALS_JSON")),
		strings.TrimSpace(os.Getenv("APNS_SANDBOX_CREDENTIALS_FILE")),
	)
	if err != nil {
		return nil, fmt.Errorf("sandbox APNs sender: %w", err)
	}
	return NewRegistry(&AppConfig{
		AppID:            AppIDHealthSync,
		AppleAppID:       appleAppID,
		APNsTopic:        topic,
		AllowSandbox:     allowSandbox,
		ProductionSender: prodSender,
		SandboxSender:    sandboxSender,
		Policy:           HealthSyncPayloadPolicy{},
	})
}

func resolvePolicy(name, appID string) (PayloadPolicy, error) {
	if name == "" {
		name = appID
	}
	switch name {
	case AppIDHealthSync:
		return HealthSyncPayloadPolicy{}, nil
	default:
		return nil, fmt.Errorf("unknown payload policy %q", name)
	}
}

func loadSenderForTopic(topic, credJSON, credFile string) (apnspush.TransportSender, error) {
	if credJSON == "" && credFile == "" {
		return nil, nil
	}
	var raw []byte
	if credJSON != "" {
		raw = []byte(credJSON)
	} else {
		var err error
		raw, err = os.ReadFile(credFile)
		if err != nil {
			return nil, err
		}
	}
	cred, key, err := apnspush.ParseCredential(raw)
	if err != nil {
		return nil, err
	}
	return &apnspush.Sender{
		Topic:      topic,
		Credential: cred,
		Key:        key,
	}, nil
}
