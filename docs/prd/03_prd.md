chnage the behaviour of command

afc login

and

afc admin users create.

when a new user logs in for the first time or when an admin creates a new user, create also an organization for the user. The new organization is named matching the user's username. The user is the owner of the organization and it's only member.

This is to make sure that when a user creates a workspace, we get a hierachical namespace "<username>/<workspace_slug>".
