Implement the functionallity behind the "afc workspace create" command. 

## Location of the workspaces:

Server configuration needs a location for code and other metadat, the "WORKSPACE_ROOT".

Add a new section to config.toml:

```
[workspace]
path = "./workspace
```

Path resolution happens following the same logic as for the database, taking XDG_DATA_HOME into account.

## workspaces

Each workspace owns a subdirectory in WORKSPACE_ROOT, matching the workspace slug.

## code

The code pointed to by flag "--git-url" and "--branch" is checked out into a folder "trunk", relative to the workspace folder.

## git operations

Git operations are done "in the service", ie in the go code. Use golang library 

https://github.com/go-git/go-git

for this.

Research the go-git library. An example of how to checkout code is here:

https://github.com/go-git/go-git/blob/main/_examples/clone/main.go

## git server

When adding the go-git lib for git operations, make preparations for running a git server inside of hub. A git client should be able to push / pull from the internal git server in the same way it would do eg with github or gitlab.

All repos managed by hub are "private" by default.

Authentication uses the user keys or PATs managed by the hub.