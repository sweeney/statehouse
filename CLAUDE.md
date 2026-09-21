# Statehouse — Claude guidance

## OpenAPI spec

When adding or removing an HTTP endpoint, update `internal/httpapi/openapi.yaml`.
The path coverage test in `internal/httpapi/spec_test.go` will fail CI if routes and spec paths drift out of sync.

The same applies to response *fields*: `internal/httpapi/spec_fields_test.go` checks both
directions — a DTO field with no schema property, and a schema property no DTO emits —
for the types listed in its `documentedSchemas` map. Add an entry there when a response
DTO gains a schema.

## Device placement

`room`, `floor` and `covers` come from the devices namespace and are relayed, never
derived — in particular `floor` is not parsed out of the `<floor>.<slug>` room id. See
`docs/floorplan-vocabulary.md` for what statehouse deliberately does not relay
(`coordinate`, `note`) and why room is not an Influx tag.
