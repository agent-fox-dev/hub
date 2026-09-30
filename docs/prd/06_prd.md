I want to add a subsystem that allows to manage secrets and variables for a user, organizations and workspaces.

Secrets and variables are key/value pairs stored in the database. 

For now, secrets are not cryptographically protected ie stored "as plain text" inside the database. This will change in the future.

## Onwership

Secrets/variables are defined/owned by
- a workspace
- an organization
- a user

## Resolution

Secrets/variables are resolved in this order:
1) workspace
2) organization
3) user

This means workspace is most specific and user is most generic.

## Permissions

Following new permissions are added:

- secrets:manage    -> create, update, delete of secrets
- secrets:list      -> list the names of the secrets, but not their values
- secrets:write     -> update of a secret
- secrets:delete    -> delete a secret

- vars:manage       -> create, list, update, delete of variables
- vars:read         -> read, list of variables
- vars:write        -> read, list, update of variables
- vars:delete       -> delete a variable

## CLI commands

### secrets

afc secrets list    -> shows the secret names, but not their values
afc secrets create  key=value,key=value
afc secrets update  key=value
afc secrets delete  key

create/update/delete use the following flags to specifiy the secrets ownership:
--org <id or slug>
--workspace <slug>
--user <id or username or email>

For "create", all three flags can be combined.
For "list","update","delete" only one selector is allowed.

If not flag is provided, --user with the current user from either API key or PAT is assumed.

### variables

afc vars list    -> shows the variables key/values pairs
afc vars create  key=value,key=value
afc vars update  key=value
afc vars delete  key

create/update/delete use the following flags to specifiy the variables ownership:
--org <id or slug>
--workspace <slug>
--user <id or username or email>

If not flag is provided, --user with the current user from either API key or PAT is assumed.

For "create", all three flags can be combined.
For "list","update","delete" only one selector is allowed.



