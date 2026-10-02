package oauth

import (
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"time"
)

func newNoRedirectHTTPClient(base *http.Client) *http.Client {
	if base == nil {
		base = &http.Client{}
	}
	client := *base
	client.Timeout = 5 * time.Second
	client.CheckRedirect = func(*http.Request, []*http.Request) error {
		return errors.New("oauth HTTP redirects are not allowed")
	}
	return &client
}

func validateHTTPSURL(name, raw string) error {
	u, err := url.ParseRequestURI(raw)
	if err != nil || u.Scheme != "https" || u.Host == "" || u.User != nil || u.Fragment != "" || u.RawQuery != "" || u.Opaque != "" {
		return fmt.Errorf("%s must be a valid HTTPS URL", name)
	}
	return nil
}
