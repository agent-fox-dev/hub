Change to CLI afc

afc accepts two flags on most of its commands:
- "hub-url"
- "api-key"

Entering these two on each command is not practical. I want CLI "afc", and other future CLIs that we might create, to use a default config file 
$HOME/.af/config.toml that contains the current users "api-key" and the default "hub-url".

afc checks for this file on start and creates it if it does not exist.

An existing config file will not be updated, except when creating workspace keys with commands:
- afc keys create
- afc keys revoke
- afc keys refresh

Add a new command
- afc keys default <key-id> to set the default key.

workspace api key are also stored in $HOME/.af/config.toml

using the flags "hub-url" and "api-key" overrides any setting sin the config file.

update the CLI documentation and add a section about clinet configuration to docs/configuration.md