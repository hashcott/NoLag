# GameNoLag documentation

**English** · [Tiếng Việt](../vi/README.md) · [简体中文](../zh-CN/README.md)

## Start here

| If you are… | Read |
|---|---|
| A player | [User guide](user-guide.md) |
| Installing or packaging the Windows client | [Setting up the client](setup-client.md) |
| Contributing a VPS as a relay | [Setting up a relay](setup-relay.md) |
| Running the service for others | [Setting up the control plane](setup-control-plane.md) |
| Wanting to understand how it fits together | [Architecture](architecture.md) |
| Contributing code | [CONTRIBUTING](../../CONTRIBUTING.md) |
| Reporting a vulnerability | [SECURITY](../../SECURITY.md) |

## Bringing up a whole deployment, in order

1. **Control plane.** Postgres, `gnl-control` behind TLS, then mint a
   contributor key.
   → [setup-control-plane.md](setup-control-plane.md)
2. **First relay.** A KVM VPS near the game servers: run `relay-v1.sh`, open
   UDP 51820 at the provider, and verify it from outside with
   `gnl-relaycheck`.
   → [setup-relay.md](setup-relay.md)
3. **Game profile.** `gnl-profile` dry run, review the output, then
   `-publish`.
   → [setup-control-plane.md § 7](setup-control-plane.md#7-publish-a-game-profile)
4. **Client.** Install the Windows bundle with the contributor key, then
   Connect.
   → [setup-client.md](setup-client.md)

The relay is offered to players once it is `up` and its 48-hour observation
window has passed.

## Deep references (English)

These explain the reasoning behind each step and what every check proves.

- [Windows client runbook](../windows-client-runbook.md)
- [P1 runbook: control plane and first relay](../p1-runbook.md)
- [P0 runbook: route-quality measurement](../p0-runbook.md)
