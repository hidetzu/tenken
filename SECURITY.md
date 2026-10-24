# Security policy

## Reporting a vulnerability

Please report a vulnerability privately, through GitHub's
[private vulnerability reporting](https://github.com/hidetzu/tenken/security/advisories/new),
not in a public issue or pull request.

Say what you found, the tenken version (`tenken --version`), and how to reproduce it. A site you control that
shows the problem helps; please do not point tenken at sites you do not own to demonstrate it.

## Supported versions

Only the latest release is supported. tenken is in pre-release (`v0.1.0-dev.*`), so a fix ships in a new release,
not as a patch to an older one.

## Scope

tenken fetches the URL you give it and the pages and links it finds there. What it connects to, and what it
does not, is in [`README.md`](README.md#security) and [`docs/cli-reference.md`](docs/cli-reference.md) §5.2.
