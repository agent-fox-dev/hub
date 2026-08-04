# Errata: MaxRetries Zero-Value Semantics (Spec 10)

**Spec:** 10_durable_job_queue
**Requirements:** 10-REQ-6.2, 10-REQ-6.E3

## Issue

The spec requires both:

1. **10-REQ-6.2:** "falls back to defaults (max_retries=20) for any unspecified
   fields" when a per-type retry policy is registered.
2. **10-REQ-6.E3:** "IF max_retries is configured as 0, THEN the first failure
   immediately transitions the job to dead_letter without any retry attempt."

In Go, the zero value of `int` is `0`, making it impossible to distinguish
"MaxRetries was not set" from "MaxRetries was explicitly set to 0" when both
are represented as `RetryPolicy{MaxRetries: 0}`.

## Resolution

`RetryPolicy.withDefaults()` uses a heuristic: if at least one other field
(Base, Multiplier, or Cap) has a non-zero value, MaxRetries=0 is treated as
"unset" and defaults to 20. If all other fields are also zero (i.e., the caller
constructed `&RetryPolicy{MaxRetries: 0}` or `&RetryPolicy{}`), MaxRetries=0
is treated as "explicitly set to zero retries."

This means the combination `&RetryPolicy{Base: 10*time.Second, MaxRetries: 0}`
will **not** honour the explicit zero — MaxRetries will be defaulted to 20.
Callers who need custom timing with zero retries should pass the full policy:
`&RetryPolicy{Base: 10*time.Second, MaxRetries: 0, Multiplier: 1, Cap: 1}`.

Alternatively, pass `nil` for the policy pointer to get all defaults
(MaxRetries=20), and only construct a `&RetryPolicy{...}` when explicitly
overriding fields.

## Impact

- `TestBackoff_PerTypePolicyOverrides` (TS-10-20) passes: `&RetryPolicy{Base: 10s}`
  has `hasOtherFields=true`, so MaxRetries defaults to 20.
- `TestBackoff_MaxRetriesZeroImmediateDeadLetter` (TS-10-E21) passes:
  `&RetryPolicy{MaxRetries: 0}` has `hasOtherFields=false`, so MaxRetries stays 0.

## Retry Count Increment on Dead-Letter

Per 10-REQ-5.3 and 10-REQ-6.E1, retry_count is always incremented **before**
checking against max_retries for retryable errors. The dead-letter transition
stores the incremented retry_count in the database. For non-retryable
(permanent) errors, retry_count is **not** incremented (10-REQ-5.4).
