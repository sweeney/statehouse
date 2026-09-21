# Service tokens

Statehouse accepts two kinds of Bearer token: a **user access token** from
someone signed in through the UI, and a **service token** obtained by a sibling
service with the OAuth 2.0 `client_credentials` grant. This document is about
the second kind — how to call the API as a service, what statehouse checks, what
it deliberately does not check, and how to tighten it.

## Why this exists

Until [#69](https://github.com/sweeney/statehouse/issues/69), only the first
kind worked. `requireAuth` called `verifier.Parse`, and `identity/common`'s
`Parse` refuses a service token outright:

```go
// jwks_verifier.go:205 — Parse validates tokenStr as an identity user access token.
if typ == "at+jwt" {
    return errors.New("service token not accepted as user token")
}
```

`ParseServiceToken` sits on the same verifier and was never called, so a service
token came back as "malformed" and the caller got a bare 401 that never said the
token *type* was the problem. Every endpoint was unreachable by every sibling
service.

The visible cost was duplication. Countinghouse and greenhouse could not call
statehouse, so they read the `devices_home` config namespace directly, and three
services each grew their own copy of `DeviceConfig` — free to drift, with
nothing to catch it. [#67](https://github.com/sweeney/statehouse/pull/67) added
`room`, `floor` and `covers` to `/config/devices` precisely so a consumer could
resolve a device to a place, and the consumers that want that are services —
which is why those fields shipped addressed to callers who could not reach
them.

## Calling the API as a service

Register a client with the identity service, then use the `TokenSource` from
`identity/common`, which fetches and caches a token and refreshes it 30s before
expiry:

```go
import (
    "net/http"

    "github.com/sweeney/identity/common/auth"
)

tokens := &auth.TokenSource{
    BaseURL:      "https://id.example.net",
    ClientID:     "countinghouse",
    ClientSecret: os.Getenv("IDENTITY_CLIENT_SECRET"),
}

req, _ := http.NewRequestWithContext(ctx, http.MethodGet,
    "https://statehouse.example.net/config/devices", nil)
tok, err := tokens.Token(ctx)
if err != nil {
    return err
}
req.Header.Set("Authorization", "Bearer "+tok)
resp, err := http.DefaultClient.Do(req)
```

On a 401, call `tokens.Invalidate()` and retry **once**. The cached token may
have been revoked, or the signing key may have rotated. Do not retry in a loop:
a 401 that survives a fresh token is a configuration problem, and hammering the
endpoint will not fix it.

From the shell:

```
TOKEN=$(curl -s -X POST https://id.example.net/oauth/token \
  -d grant_type=client_credentials \
  -d client_id=countinghouse \
  -d client_secret="$SECRET" | jq -r .access_token)

curl -s -H "Authorization: Bearer $TOKEN" https://statehouse.example.net/config/devices
```

Service tokens reach **every** endpoint a user token reaches. There is no
per-endpoint distinction between the two principals — see
[What a service token authorises](#what-a-service-token-authorises).

## What statehouse checks

A request arrives with `Authorization: Bearer <jwt>`.

1. **Which parser.** The JOSE `typ` header picks it: `at+jwt` is a service
   token, anything else is treated as a user token. The header is read without
   verifying the signature, which is safe because `typ` is *inside* the signed
   header — a forged one routes the token to a parser that then rejects it.
   Nothing is granted at this step; it only chooses which function does the
   checking. The comparison is exact, mirroring `identity/common`'s own test, so
   a token never lands in a parser that would refuse it on a technicality.
2. **Signature, issuer and expiry**, by `ParseServiceToken` against the JWKS
   published by the configured identity service. A token must also carry a
   `client_id`; one without names no principal, so there would be nothing to
   log, allowlist or revoke.
3. **Audience**, if `required_audience` is configured.
4. **Client allowlist**, if `allowed_clients` is configured.
5. **Scope**, if `required_scope` is configured.

The order is deliberate: everything about whether the token is *ours* is settled
before anything about whether the principal is *permitted*. Checking the
allowlist first would answer 403 — "your token is fine, you just lack
privilege" — to someone replaying a token minted for a different service.

### What it does not check

A user token's `IsActive` flag has no equivalent here. A service principal has
no account to deactivate and no role, so "is this client still allowed" is
answered by the identity service continuing to issue it tokens (and by the
allowlist below), not by a claim on the token.

## Configuration

```yaml
auth:
  service_tokens:
    enabled: true
    # required_audience: https://statehouse.example.net
    # required_scope: statehouse:read
    # allowed_clients:
    #   - countinghouse
    #   - greenhouse
```

| Key | Default | Effect |
|---|---|---|
| `enabled` | `true` | Accept service tokens at all. `false` restores the user-token-only behaviour. |
| `required_audience` | unset | Require the token's `aud` to include this value, matched whole. |
| `required_scope` | unset | Require the token's space-delimited `scope` to contain this one scope, matched whole. |
| `allowed_clients` | unset | Permit only these `client_id`s, matched exactly. |

Absent block means defaults, which means service tokens are accepted with no
further restriction.

### Why the default is permissive

Requiring a scope or an audience by default would mean every sibling service
403s until the identity service is taught to stamp that claim — the same outage
as the bug this replaces, arrived at by a different route. `TokenSource` posts
only `grant_type`, `client_id` and `client_secret`, so whatever `aud` and
`scope` a token carries is decided entirely by the client's registration on the
identity side. Statehouse cannot know what that is.

So the shipped default is "any token this instance's identity service signed",
and the three restrictions are there to be turned on as identity grows the
claims to support them. That default is not "no security": it still requires a
valid, unexpired, correctly-signed token from a single configured issuer, for a
client that the identity service chose to register.

### Hardening, in the order worth doing it

1. **`allowed_clients`** — the only one that needs nothing new from the identity
   service. If two services should read house state, naming them costs one
   config line and immediately bounds the blast radius of any other client's
   leaked secret.
2. **`required_audience`** — the standard defence against token redirection.
   Without it, a token minted for a *sibling* service is accepted here too, so
   any service that can obtain a token from this issuer can read house state.
   Set it as soon as identity stamps an audience.
3. **`required_scope`** — least privilege, once scopes exist. One scope, matched
   whole; `statehouse:readonly` does not satisfy `statehouse:read`.

A config that could never match is refused at startup rather than at request
time: an audience or scope containing a space, or an empty string in
`allowed_clients` (which matches no client but does switch the allowlist on, so
the list reads as "permit nothing"). Each of those would otherwise start
cleanly, report healthy, and turn away every caller.

### One note on audience

The audience is enforced against the parsed service claims here, not by setting
`JWKSVerifierConfig.RequiredAudience`. That field applies to **user** tokens as
well, and user tokens carry no `aud` — setting it would lock every human caller
out of the API. `TestUserToken_UnaffectedByServiceAudienceRequirement` pins
that, because the bug it prevents is invisible until a person tries to load the
UI.

## Responses

Statehouse answers failures with the RFC 6750 challenge, so a client can tell
"get a new token" from "this will never work".

| Situation | Status | `WWW-Authenticate` |
|---|---|---|
| No `Authorization` header | 401 | `Bearer realm="…"` — no error code, per RFC 6750 §3.1 |
| Not a Bearer scheme, or empty credential | 401 | bare challenge |
| Malformed token, bad signature, unknown key, wrong issuer | 401 | `error="invalid_token"`, `error_description="token is malformed, or signed by a key this server does not trust"` |
| Expired token | 401 | `error="invalid_token"`, `error_description="token expired"` |
| User token, account not active | 401 | `error="invalid_token"`, `error_description="user account is not active"` |
| Service token, but `enabled: false` | 401 | `error="invalid_token"`, `error_description="service token presented but this server does not accept service tokens"` |
| Service token, audience does not include this server | 401 | `error="invalid_token"`, `error_description="token audience does not include this server"` |
| Service token, `client_id` not on the allowlist | 403 | `error="insufficient_scope"`, `error_description="client is not permitted to call this API"` |
| Service token, missing the required scope | 403 | `error="insufficient_scope"`, `scope="<required>"` |

401 means *try again with a better token*. 403 means *this token will never
work here*; retrying is wasted.

RFC 6750 defines only three error codes — `invalid_request`, `invalid_token` and
`insufficient_scope` — so a client that is not on the allowlist gets
`insufficient_scope` too. It is the code that means "this token does not carry
enough privilege for this resource", which is the case, even though "scope" is a
slightly narrow word for it.

The `Bearer` scheme is matched case-insensitively (RFC 7235 §2.1) and extra
whitespace before the credential is tolerated. A correct client that sends
`bearer` should not get a 401 it cannot debug from the outside.

## Security and data protection

**What is behind this door.** Statehouse serves household telemetry: which
devices are drawing power, whether the house reads as occupied, asleep or empty,
recent activity, and — since #67 — the room and floor each device sits in. On
this property rooms are named after the people who sleep in them. Taken
together, that is a record of when a specific person is at home and what they
are doing. It is not device trivia, and the point of an auth change here is to
widen access to legitimate consumers without widening it to anything else.

**The API is read-only.** There is no write endpoint, so a service token cannot
change state, and the worst outcome of a leaked service credential is
disclosure, not tampering. That is what makes a single accept/reject decision
adequate today, and it is also why the same decision would *not* be adequate if
a write endpoint were ever added — see below.

**Tokens are never logged.** Not the token, not a prefix of it, not a hash.
Rejections log the reason; accepted service calls log `client_id` and `jti` at
debug level, which is enough to answer "which service read this, and when"
without putting a usable credential in the journal.

**Log level is chosen against flooding.** Malformed-token rejections log at
debug, because anyone who can reach the port can produce them. Policy
rejections — audience, allowlist, scope — log at warn, because reaching one
requires a validly signed token, so it means a real client is misconfigured or a
real token is being replayed at the wrong service. Only the second kind is
worth waking up for.

**`/metrics` counts, it does not identify.** The `auth` block carries outcome
counters (`service_tokens_accepted_total`, `rejected_audience_total`, and so
on). A per-client breakdown there would turn a scrape endpoint into a log of
which services called and when, growing without bound as clients come and go.

**Failures say enough to debug and no more.** The descriptions in the table
above are fixed constants. Nothing from the presented token, and nothing about
other principals, is reflected back — a 401 body is the one response an
unauthenticated stranger can always read. Telling a caller their own token
expired discloses nothing they could not read off the token themselves.

**The challenge cannot be restructured by config.** `realm` is the configured
issuer — operator input. A quote in it would silently end the realm and let the
rest of the string pose as further auth-params, so quotes, backslashes and
control characters are dropped before the header is written.

**Rate limiting is not done here.** `identity/common` ships a `ratelimit`
package and statehouse does not use it. Statehouse listens on a private network,
every endpoint is read-only, and JWKS keys are cached in memory so a flood of
bad tokens costs signature verification rather than a fetch per request. If it
is ever exposed more widely, per-IP limiting on the 401 path is the first thing
to add.

## If it ever gets a write endpoint

A single accept/reject decision for the whole API is right for a read-only
service and wrong for one that can be told to do something. If a write endpoint
is added, the shape to reach for is a scope per capability — `statehouse:read`
versus `statehouse:write` — checked per route rather than once in the
middleware, so a consumer that only needs `/config/devices` cannot also drive
the house. Adding it later is a change to the middleware and the route table;
nothing in this design has to be undone first.

The related question is whether service tokens should be scoped to *fewer*
endpoints than user tokens even now — config only, say, since that is what
prompted #69. They are not, deliberately. Every endpoint here is read-only and
the same identity service vouches for both principals, so a split would be a
line drawn on a guess about future consumers, and the first service that wanted
`/state/devices` would have to reopen the question. `allowed_clients` bounds
*who* calls without pretending to know *what* they will need.

## Troubleshooting

**Every call 401s with `invalid_token`.** Check the issuer first: statehouse
validates `iss` against its own `identity.base_url`, so a token from a different
identity deployment fails here even though it is perfectly valid. Then check
clock skew — an `exp` a few seconds in the past reads as expired.

**It worked yesterday and 401s today.** The signing key rotated and the cached
JWKS went stale, or the client's secret was rotated. `/healthz` and `/metrics`
both report JWKS state; `jwks_fetch_errors_total` climbing means statehouse
cannot reach the identity service, and `jwks_stale_served_total` means it is
serving from a cache it could not refresh.

**403 with `insufficient_scope` and no scope in the challenge.** That is the
allowlist, not scope: this deployment sets `allowed_clients` and the caller's
`client_id` is not on it. A `scope` parameter in the challenge means it really
is scope.

**A service token 401s but a user token works.** Check `enabled` under
`auth.service_tokens`. The `error_description` says
`service token presented but this server does not accept service tokens` in that
case. The startup log also states the posture: `inbound authentication enabled`
with `service_tokens=false`.

**Nothing requires a token at all.** `identity.base_url` is unset, which
disables inbound authentication entirely. Statehouse logs
`inbound authentication is DISABLED` at startup when that is the case.

## Related

- `internal/httpapi/auth.go` — the middleware.
- `internal/httpapi/cors.go` — the CORS wrapper that sits above it. It answers
  preflights ahead of authentication (a browser never attaches credentials to
  one) and exposes `WWW-Authenticate`, which is what lets browser JS read the
  error codes in the table above.
- `docs/floorplan-vocabulary.md` — what `/config/devices` serves, and what it
  deliberately does not.
- `internal/httpapi/service_token_test.go` — the behaviour, as tests.
- [`identity/common/auth`](https://github.com/sweeney/identity) — `TokenSource`,
  `JWKSVerifier`, `ServiceTokenClaims`.
