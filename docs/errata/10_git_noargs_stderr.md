# Errata: git no-args usage output goes to stdout, not stderr

**Spec:** 10_gitcmd
**Requirement:** 10-REQ-2.E1
**Date:** 2026-08-03

## Spec Assumption

The specification states:

> WHEN a caller invokes Run with no args, THE GitRunner.Run SHALL invoke
> 'git' with no subcommand; **git prints a usage message to stderr** and exits
> non-zero; return the resulting GitError without any additional
> programmer-mistake guard.

## Actual Behavior

On modern git versions (tested with git 2.x on macOS), `git` invoked with no
subcommand writes its usage message to **stdout**, not stderr. Stderr is empty.

```
$ git > /tmp/stdout 2> /tmp/stderr
$ wc -c /tmp/stdout /tmp/stderr
    2290 /tmp/stdout
       0 /tmp/stderr
```

## Impact

The test `TestRun_NoArgs_ReturnsGitError` originally asserted
`len(gitErr.Stderr) > 0`. This fails because git writes usage to stdout.

## Resolution

The test was updated to check that at least one of stdout or `gitErr.Stderr`
contains output, rather than requiring stderr specifically. This matches the
implementation correctly (it captures whatever git sends to each stream) and
is resilient to platform differences in git's output routing.

The core test contract (returns `*GitError` with non-zero `ExitCode`) is
preserved unchanged.
