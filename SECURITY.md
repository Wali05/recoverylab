# Security

Please report security problems privately through [GitHub's advisory form](https://github.com/Wali05/recoverylab/security/advisories/new) rather than a public issue. I'll reply as soon as I can, and I'm happy to credit you in the release notes.

Only the latest release gets fixes.

## What RecoveryLab trusts

- **Scenario files are code.** A `service.command` runs on your machine with your permissions. Only run scenarios you wrote or have read.
- **Targets are local.** Requests, state checks, `init`, and `discover --base` only go to loopback addresses, and redirects aren't followed. A report about reaching a remote host through a scenario is in scope.
- **Secrets stay in headers.** `{{env:NAME}}` is expanded only in header values, and reports never include headers. A token that shows up in a report or log is a bug worth reporting.
- **Downloads are checked.** `install.sh` and the GitHub Action compare each release binary with the published SHA-256 sums before running it. The binaries aren't signed.
