package oauth

import (
	"encoding/json"
	"net/http"
	"net/url"
	"strings"
)

const protectedResourcePath = "/.well-known/oauth-protected-resource"

type ProtectedResourceMetadata struct {
	Resource              string   `json:"resource"`
	AuthorizationServers  []string `json:"authorization_servers"`
	ScopesSupported       []string `json:"scopes_supported,omitempty"`
	ResourceDocumentation string   `json:"resource_documentation,omitempty"`
}

type MetadataConfig struct {
	Resource              string
	AuthorizationServers  []string
	Scopes                []string
	ResourceDocumentation string
}

func NewProtectedResourceMetadata(cfg MetadataConfig) ProtectedResourceMetadata {
	return ProtectedResourceMetadata{
		Resource:              strings.TrimRight(cfg.Resource, "/"),
		AuthorizationServers:  cfg.AuthorizationServers,
		ScopesSupported:       cfg.Scopes,
		ResourceDocumentation: cfg.ResourceDocumentation,
	}
}

func MetadataURL(resource string) string {
	u, err := url.Parse(resource)
	if err != nil {
		return ""
	}
	return u.Scheme + "://" + u.Host + protectedResourcePath + strings.TrimRight(u.EscapedPath(), "/")
}

func MetadataPath(resource string) string {
	u, err := url.Parse(resource)
	if err != nil {
		return ""
	}
	return protectedResourcePath + strings.TrimRight(u.EscapedPath(), "/")
}

func Challenge(resource string, scopes []string) string {
	parts := []string{`resource_metadata="` + MetadataURL(resource) + `"`}
	if len(scopes) > 0 {
		parts = append(parts, `scope="`+strings.Join(scopes, " ")+`"`)
	}
	return "Bearer " + strings.Join(parts, ", ")
}

func MetadataHandler(metadata ProtectedResourceMetadata) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			w.Header().Set("Allow", http.MethodGet)
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(metadata)
	}
}
