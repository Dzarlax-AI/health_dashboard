package api

import "github.com/google/uuid"

type PushDeviceRegisterRequest struct {
	RegistrationAttemptID *string `json:"registration_attempt_id,omitempty"`
	DeviceID              string  `json:"device_id"`
	APNsToken             string  `json:"apns_token"`
	APNsEnv               string  `json:"apns_env" jsonschema:"enum=production,enum=sandbox"`
	BundleID              string  `json:"bundle_id"`
	Locale                string  `json:"locale" jsonschema:"enum=en,enum=ru,enum=sr"`
	InboxContractVersion  *int    `json:"inbox_contract_version,omitempty"`
}

type PushDeviceRegisterResponse struct {
	DeviceID             uuid.UUID `json:"device_id"`
	BindingID            uuid.UUID `json:"binding_id"`
	Enabled              bool      `json:"enabled"`
	RegistrationVersion  int64     `json:"registration_version"`
	APNsEnv              string    `json:"apns_env" jsonschema:"enum=production,enum=sandbox"`
	InboxContractVersion *int      `json:"inbox_contract_version,omitempty"`
}
