# Security policy

## Supported versions

| Version | Supported |
| --- | --- |
| v0.1.x | Yes, once the first `v0.1.0` tag is published |
| unreleased `*-dev` builds | Best-effort, no compatibility promise |

## Reporting a vulnerability

Do not disclose credential exposure, authentication bypasses, remote-code-execution paths, or privacy leaks in a public issue. Use the repository host's private security advisory feature once the public repository exists.

Until then, report the issue privately to the project owner. A public security contact will be documented before the first release.

## Security principles

- Agents are outbound-only and read-only in v0.1.
- Node credentials are unique, revocable, and stored as hashes by the server.
- Proxy credentials and subscription URLs remain on the reporting node.
- Telemetry is disabled by default.
- Domains, URLs, and full destinations are not collected by default.
- All-in-one deployments expose only the LanTally HTTP endpoint; storage ports remain internal.
- Internet-exposed deployments require HTTPS through a trusted reverse proxy or equivalent secure transport.

