# @xenoviz/ruk package template

The release packaging step stages `scripts/npm/**` beside this manifest before
publishing. The postinstall script validates the matching optional native
package and places its verified binary at `bin/ruk`. Runtime therefore invokes
the native executable directly; Node is not retained as a command supervisor.

The seven platform package manifests are templates. Their release staging step
must replace the zero digest with the SHA-256 of the exact native binary and
include that binary at the declared path.
