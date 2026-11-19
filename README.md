# moca-sbx-service-enrollment

A simple repository to follow who is enrolled in what moca sandbox
service. Also contains the companion code to run on the control
planes.

## repository structure

* data/<reponame>/users.txt - list, in no particular order, of users
enrolled in the system. Enrollment is contingent on the user ID being valid.

* data/<reponame>/deleted.txt - list, in no particular order, of users
  forcibly disenrolled from the system. This overrides validty of ID.

User ID structure:

* For w3IDs: `oidc:<w3id>`
* For github IDs: `github:<username>` or `github:<userid>`

