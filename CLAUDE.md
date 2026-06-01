# Statehouse — Claude guidance

## OpenAPI spec

When adding or removing an HTTP endpoint, update `internal/httpapi/openapi.yaml`.
The path coverage test in `internal/httpapi/spec_test.go` will fail CI if routes and spec paths drift out of sync.
