# Errata: Spec 03 Key Response and Error Envelope Format

## 1. Error Envelope Format (Critical)

**Spec 03** test stubs (TS-03-15, TS-03-E5, TS-03-E8, TS-03-E11, TS-03-E12,
TS-03-E14) originally used a **flat** format:

```json
{"error": "forbidden", "message": "You do not have access"}
```

**Spec 02 REQ-8.1** defines a **nested** format:

```json
{"error": {"code": "403", "message": "You do not have access"}}
```

**Resolution**: All CLI test stubs in group 3 (`keys_test.go`) use the nested
format from spec 02 REQ-8.1. The implementation (`parseHTTPError` in task 5.4)
must parse the nested `{"error": {"code": "...", "message": "..."}}` shape.

## 2. Key Create/Refresh Response Shape (Major)

**Spec 03** test stubs (TS-03-10, TS-03-11, TS-03-13) originally expected a
standalone `secret` field:

```json
{"key_id": "k1", "secret": "plaintext-secret", "workspace_id": "ws-123"}
```

**Spec 02 REQ-7.1** returns the secret embedded in a composite `key` field:

```json
{"key": "af_<key_id>_<secret>", "key_id": "...", "expires_at": "...", "role": "..."}
```

**Resolution**: All CLI test stubs in group 3 use the spec 02 composite `key`
field format. Tests assert on the `key` field (not `secret`) and check for the
presence of the secret substring within it.
