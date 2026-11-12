# Statehouse — Claude guidance

## OpenAPI spec

When adding or removing an HTTP endpoint, update `internal/httpapi/openapi.yaml`.
The path coverage test in `internal/httpapi/spec_test.go` will fail CI if routes and spec paths drift out of sync.

The same applies to response *fields*: `internal/httpapi/spec_fields_test.go` checks both
directions — a DTO field with no schema property, and a schema property no DTO emits —
for the types listed in its `documentedSchemas` map. Add an entry there when a response
DTO gains a schema.

## Device placement

`room`, `floor`, `covers` and `part_of` come from the devices namespace and are relayed,
never derived — in particular `floor` is not parsed out of the `<floor>.<slug>` room id,
and a probe does not inherit its appliance's room through `part_of` (#83). See
`docs/floorplan-vocabulary.md` for what statehouse deliberately does not relay
(`coordinate`, `note`) and why room is not an Influx tag.

## Local vs remote config

The local YAML holds only connection settings (`site`, `mqtt`, `identity`,
`remote_config`, `http`, `auth`, `recent_log`, `influx`). `adapters`, `energy`,
`availability`, `house`, `device_classes` and `devices` come only from the
config service, and `Load` refuses a local file that sets them. To switch an
adapter on or change behaviour, edit `statehouse_behaviour` and restart (SIGHUP
does not start or stop adapters). A new remote-only setting goes on a `yaml:"-"`
field and in `remoteOnlySections`; a new local one gets a yaml tag. See
`docs/remote-config.md`.
