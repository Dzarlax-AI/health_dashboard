package ctxdb

import (
	"context"

	"health-receiver/internal/storage"
)

type dbKey struct{}
type schemaKey struct{}
type isAdminKey struct{}

// WithDB stores a tenant DB and schema name in the context.
func WithDB(ctx context.Context, db *storage.DB, schema string) context.Context {
	ctx = context.WithValue(ctx, dbKey{}, db)
	ctx = context.WithValue(ctx, schemaKey{}, schema)
	return ctx
}

// WithIsAdmin stores the admin flag in the context.
func WithIsAdmin(ctx context.Context, isAdmin bool) context.Context {
	return context.WithValue(ctx, isAdminKey{}, isAdmin)
}

// IsAdminFromContext returns true if the current request user is an admin.
func IsAdminFromContext(ctx context.Context) bool {
	v, _ := ctx.Value(isAdminKey{}).(bool)
	return v
}

// FromContext retrieves the tenant DB from the context. Returns nil if not set.
func FromContext(ctx context.Context) *storage.DB {
	db, _ := ctx.Value(dbKey{}).(*storage.DB)
	return db
}

// SchemaFromContext retrieves the tenant schema name from the context.
func SchemaFromContext(ctx context.Context) string {
	s, _ := ctx.Value(schemaKey{}).(string)
	return s
}
