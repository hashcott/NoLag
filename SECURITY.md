# Security policy

GameNoLag runs a privileged service on players' PCs and forwards traffic on
contributors' VPSes, so a vulnerability can reach somebody else's machine. We
take reports seriously.

## Reporting a vulnerability

**Do not open a public issue.** Report it privately through GitHub: go to
**Security → Report a vulnerability** on
[hashcott/NoLag](https://github.com/hashcott/NoLag/security/advisories/new).

Please include:

- The component (`gnl-service`, `gnl-ui`, `gnl-agent`, `gnl-control`, an
  installer…) and the commit or build you tested.
- What an attacker needs and what they gain. For example: a local unprivileged
  user, a network position, a contributor key or a relay token.
- Steps to reproduce, or a proof of concept.

We aim to acknowledge a report within 3 working days and to agree a disclosure
date with you. You are credited in the advisory unless you ask otherwise.

## Supported versions

While the project is pre-release, only the latest `main` is supported.

## What is in scope

Of particular interest, because each is a boundary the design relies on:

- The named pipe between `gnl-ui` and `gnl-service`: anything beyond the four
  parameterless verbs, or access by anyone other than the interactive user.
- Privilege escalation through the install directories,
  `ProgramData\GameNoLag`, or the installers.
- A relay that forwards anything outside the published game ranges, exceeds
  the rate cap, or acts as an open proxy.
- One client receiving another client's traffic.
- Bypassing the control plane's authentication, rate limits, device slots,
  `trusted_after` or reachability checks.
- Getting an address into a game profile without the required independent
  reports and a published-range match.
- Anything that makes the client touch a game process.

Out of scope:

- Volumetric denial of service against a single relay.
- Issues that need administrator or root on the victim's own machine.
- Findings in third-party dependencies with no path through GameNoLag.
