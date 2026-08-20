# @xenoviz/ruk package template

The release packaging step stages `scripts/npm/**` beside this manifest before
publishing. The published `bin/ruk` entry is a thin Node launcher: it validates
the matching optional native package, places the verified binary when needed,
and executes that native command. A `postinstall` script performs the same
placement eagerly when lifecycle scripts are allowed; when they are blocked,
the first `ruk` invocation finishes installation instead.

Runtime therefore invokes the native executable after placement; Node is not
retained as a command supervisor once the native binary is on the command path.

The seven platform package manifests are templates. Their release staging step
must replace the zero digest with the SHA-256 of the exact native binary and
include that binary at the declared path.
