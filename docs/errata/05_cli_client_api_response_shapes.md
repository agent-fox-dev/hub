# Errata: Spec 05 CLI Client API Response Shape Divergences

## 1. POST /api/v1/auth/callback response field names

**Spec 05 says** (05-REQ-6.4): Extract `api_key` from `response.api_key.key`
and `key_id` from `response.api_key.id`.

**Spec 02 defines** (actual): The response uses `response.api_key.token`
(the full composite key `af_<key_id>_<secret>`) and `response.api_key.key_id`
(not `.id`). There is also a `response.api_key.secret` field (plaintext
secret only).

**Resolution**: Store `response.api_key.token` as `api_key` in config
(matches Bearer auth format). Store `response.api_key.key_id` as `key_id`.
Test mocks use the spec 02 response shape.

## 2. GET /api/v1/auth/providers response wrapper

**Spec 05 test spec** (TS-05-15, TS-05-21): Mocks the response as a bare
array `['github']`.

**Spec 02 defines** (actual): Response is wrapped in
`{"providers": [{"name": "github", "authorize_url": "...", "scopes": "..."}]}`.

**Resolution**: Tests use the spec 02 response shape. The CLI must extract
provider names from the `name` field of each object in the `providers` array.

## 3. POST /api/v1/keys/:key_id/refresh response field names

**Spec 05** (05-REQ-8.1, TS-05-23): Assumes response has `id` and `key`
fields (`{id:'new-kid', key:'new-key'}`).

**Spec 02 defines** (actual): Response is a flat object with `key_id` and
`token` (or `secret`) fields:
`{key_id, user_id, secret, token, created_at, expires_at, revoked_at}`.

**Resolution**: Extract `cfg.APIKey` from `response.token` (the full
`af_<key_id>_<secret>` string), and `cfg.KeyID` from `response.key_id`.

## 4. Config file field count

**Master PRD** (docs/01_prd.md): States "Three fields only: hub_url,
user_id, api_key".

**Spec 05** (05-REQ-1.1): Adds a fourth field `key_id` to avoid extra API
calls for keys refresh/revoke.

**Resolution**: Follow spec 05 with four fields. This is a reasonable design
evolution.

## 5. Error envelope format

**Spec 01** (server_foundation): Standard envelope is
`{"error": {"code": <int>, "message": "..."}}`.

**Spec 05**: References "the error message from the JSON response body"
without specifying envelope structure.

**Resolution**: The CLI error parser attempts to unmarshal the nested
`{"error": {"message": "..."}}` or `{"error": {"code": N, "message": "..."}}`
envelope first. Falls back to `Error: unexpected response from server
(HTTP <status>).` for non-JSON bodies.
