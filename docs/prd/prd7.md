I want to enhance the "afc workspace create" command. In order to authenticate against an upstream repo, refrered to by flag --git-url, I want to provide credentials in form of a personal access token (PAT) or username/password. Add these flags:
  
  --git-pat for a PAT
  --git-username, --git-password for a username/password combination.

  The use of these two ways of athentication is mutual exclusive.
  The credentials should be store in the database.
  