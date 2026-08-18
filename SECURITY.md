# Security policy

## Supported version

Security fixes currently target the latest commit on `main`. The project has
not published a supported binary release yet.

## Report a vulnerability

Use the repository's private GitHub security-advisory form. Do not include
credentials, private configuration, or exploit details in a public issue.

If private reporting is unavailable, open a minimal public issue that asks the
maintainer to enable a private reporting channel. Do not include vulnerability
details in that issue.

Do not file public issues that include live `~/.nicos-dev/session-pressure`
state, host process dumps, or session identifiers.

Include this information:

- the affected commit or version
- the macOS version and architecture
- reproduction steps
- the expected impact

## Sensitive boundaries

Review is especially important for changes involving:

- policy evaluation and work admission
- the local control API socket
- storage reclaim apply paths
- helper install and LaunchAgent labels
- process-tree identity and disk-write attribution
