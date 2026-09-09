# Security policy

skali runs your applications, holds database and bucket credentials, and
manages the hosts it is installed on, so security reports are taken
seriously and handled privately.

## Reporting a vulnerability

Please do not open a public issue for a security problem. Use GitHub's
private vulnerability reporting instead: open the repository's
**Security** tab and choose **Report a vulnerability**. You will get an
acknowledgement within a few days and updates as the report is handled.

Include what you can: the affected component (CLI, `skalid`, the web
console, the installer, the host agent), the version (`skali --version`,
or the version shown on the console's sign-in page), reproduction steps,
and the impact you believe it has.

## Supported versions

Only the latest release receives fixes. There is no backporting to
earlier versions before 1.0.

## Scope

In scope: anything that lets a user or a workload exceed what their role
allows, exposes secrets or values, escapes an application container onto
a host, or tampers with builds, images, or the installer's trust
(checksums, join tokens, mTLS between nodes).

Out of scope: vulnerabilities in the operators skali deploys (k3s,
CloudNativePG, Traefik, cert-manager, SeaweedFS, Longhorn), which belong
to their upstream projects, unless skali's configuration of them is the
cause.

## Deployment trust boundary

This alpha is intended for trusted operators running trusted application code.
Project and environment roles authorize control-plane operations. They do not
provide network or container isolation suitable for hostile tenants. Application
pods currently share cluster networking and do not have a comprehensive
restricted pod-security policy. See [security boundaries and encryption-key
recovery](docs/security.md) before deploying workloads.
