I want to enhance the "afc workspace create" command. In order to authenticate against an upstream repo, refrered to by flag --git-url, I want to provide credentials in form of a personal access token (PAT) or username/password. Add these flags:

--git-pat for a PAT
--git-username, --git-password for a username/password combination.

The use of these two ways of athentication types is mutual exclusive.

The credentials will be store using the functionallity behind the "secrets" store, ie the provided credentials will be stored as if they were created eg using the "afc secrets create" CLI calls.

The mapping will be as follows:

--git-pat       -> GIT_PAT
--git-username  -> GIT_USERNAME
--git-password  -> GIT_PASSWORD

The secrets will be stored with the scope of the workspace.

The credentials will be used to access protected upstream repos, for push and pull operations against them.

Implement using the credentials when creating a new workspace from a private repo.

Do not implement any syncing back to the upstream repo, this will come later.