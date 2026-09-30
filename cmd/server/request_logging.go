package main

import (
	"log"
	"net/http"
	"path"
	"strings"
	"time"
)

func loggedRequestPath(requestPath string) string {
	const webhookPrefix = "/api/telegram/webhook/"
	// ServeMux redirects unclean paths before routing. Mask the original
	// path as well as anything that resolves to the webhook route.
	cleaned := path.Clean(requestPath)
	if strings.HasPrefix(requestPath, webhookPrefix) || strings.HasPrefix(cleaned, webhookPrefix) || cleaned == strings.TrimSuffix(webhookPrefix, "/") {
		return webhookPrefix + "[redacted]"
	}
	return requestPath
}

func logRequests(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		next.ServeHTTP(w, r)
		log.Printf("%s %s %s %v", r.RemoteAddr, r.Method, loggedRequestPath(r.URL.Path), time.Since(start).Round(time.Millisecond))
	})
}
