# Setting up the control plane

The control plane (`gnl-control`) holds contributor keys, device slots, relay
records and published game profiles. It is **not** on the data path. Players'
packets never pass through it, so it can run anywhere with a public HTTPS
address, and its latency to players does not matter.

You need:

- A Linux host, any distribution with systemd.
- PostgreSQL 13 or newer.
- A domain name and a TLS certificate. `gnl-control` speaks plain HTTP and
  expects a reverse proxy in front of it.
- The `gnl-control` and `gnl-profile` binaries: from the CI artifact
  `gamenolag-linux-amd64`, or built from source (see below).

## 1. Build or fetch the binaries

```bash
git clone https://github.com/hashcott/NoLag.git && cd NoLag
GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go build -trimpath -o gnl-control ./cmd/gnl-control
GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go build -trimpath -o gnl-profile ./cmd/gnl-profile
sudo install -m 0755 gnl-control gnl-profile /usr/local/bin/
```

## 2. Create the database

```bash
sudo -u postgres createuser --pwprompt gamenolag
sudo -u postgres createdb --owner gamenolag gamenolag
```

The schema is applied every time `gnl-control` starts. Every statement is
idempotent, so there is no separate migration step.

## 3. Run it under systemd

`/etc/gnl/control.env`, mode `0600`, owned by root:

```bash
GNL_DSN=postgres://gamenolag:<password>@127.0.0.1:5432/gamenolag
```

`/etc/systemd/system/gnl-control.service`:

```ini
[Unit]
Description=GameNoLag control plane
After=network-online.target postgresql.service
Wants=network-online.target

[Service]
EnvironmentFile=/etc/gnl/control.env
ExecStart=/usr/local/bin/gnl-control -listen 127.0.0.1:8080 -trust-proxy
DynamicUser=yes
Restart=on-failure
NoNewPrivileges=yes
ProtectSystem=strict
ProtectHome=yes

[Install]
WantedBy=multi-user.target
```

```bash
sudo systemctl daemon-reload
sudo systemctl enable --now gnl-control
curl -s http://127.0.0.1:8080/healthz
```

### Flags

| Flag | Default | Meaning |
|---|---|---|
| `-dsn` | `$GNL_DSN` | Postgres connection string. Required |
| `-listen` | `:8080` | HTTP listen address. Bind to loopback when a proxy is in front |
| `-trust-proxy` | off | Use `X-Forwarded-For` for rate limiting. **Set it only when your proxy really sets that header.** Otherwise every client picks its own rate-limit bucket |
| `-stale-after` | `5m` | Mark a relay `down` after this long without a sync |
| `-poll-secs` | `10` | How often relays are told to sync |
| `-mint-key` | — | Mint one contributor key, print it, and exit |

## 4. Put TLS in front

Relay tokens and contributor keys travel as bearer tokens. Over plain HTTP,
anyone on the path can copy them. Both installers refuse a non-`https` control
URL.

A minimal Caddy configuration obtains and renews the certificate by itself:

```
cp.example.com {
    reverse_proxy 127.0.0.1:8080
}
```

With nginx, pass the client address so `-trust-proxy` counts real callers:

```nginx
location / {
    proxy_pass http://127.0.0.1:8080;
    proxy_set_header X-Forwarded-For $remote_addr;
}
```

Check it from outside: `curl -s https://cp.example.com/healthz`.

## 5. Mint contributor keys

```bash
sudo sh -c 'set -a; . /etc/gnl/control.env; exec /usr/local/bin/gnl-control -mint-key'
```

The key is printed **once**. Only its hash is stored, so it cannot be recovered.
Give it to the contributor over a channel you trust. By default a key activates
up to three devices; that is `contributor_key.max_devices` in the database.

## 6. Verify each relay from outside

A new relay stays `pending` and is never offered to players until a check from
outside the relay proves its UDP port is open. See
[Setting up a relay → Verify from outside](setup-relay.md#5-verify-from-outside).

Relay states:

| Status | Meaning | Offered to players |
|---|---|---|
| `pending` | Registered, never verified from outside | no |
| `up` | Verified reachable and still syncing | yes, once `trusted_after` (48 h after registration) has passed |
| `unreachable` | An external check failed; the reason is stored | no |
| `down` | No sync for `-stale-after` | no |

```sql
SELECT id, region, status, active_peers, capacity, last_seen, trusted_after,
       unreachable_detail
  FROM relay ORDER BY status, id;
```

## 7. Publish a game profile

Until a profile exists, every relay's egress allowlist is empty and relays
forward nothing. That is the intended starting state.

Profiles are built from what contributors observed, cross-checked against
ranges the cloud providers publish. Run without `-publish` first. It prints
what it would publish and changes nothing.

```bash
export GNL_DSN=postgres://gamenolag:<password>@127.0.0.1:5432/gamenolag
gnl-profile -game valorant -aws-regions ap-southeast-1,ap-northeast-1
# review the list, then:
gnl-profile -game valorant -aws-regions ap-southeast-1,ap-northeast-1 -publish
```

| Flag | Meaning |
|---|---|
| `-game` | Game id, as reported in observations. Required |
| `-aws-regions` | AWS regions whose published ranges count |
| `-azure-regions`, `-azure-url` | Azure regions, and the URL of the current Service Tags JSON |
| `-asns` | ASNs whose announced prefixes count |
| `-min-reporters` | Independent keys an address needs (default 3) |
| `-publish` | Actually publish. Without it, nothing is written |

Addresses that match no published range are listed as unverified and are never
added. They are usually voice chat or a CDN, and adding them by hand routes
everything else in the same block too.

Relays pick up a new profile within one sync. Clients pick it up on their next
connect, or on *Refresh game list*.

## Day-to-day operations

| Task | How |
|---|---|
| See the fleet | The `SELECT` in step 6 |
| Free a device slot | The contributor calls `DELETE /v1/devices/{id}` with their key, or run `DELETE FROM device WHERE id = '…';` |
| Revoke a key | `UPDATE contributor_key SET status = 'revoked' WHERE key_hash = '…';` then `DELETE FROM device WHERE key_hash = '…';` The hash is `printf %s 'GNL-…' \| sha256sum`. **Both steps are needed:** a revoked key cannot register relays or activate new devices, but devices it already activated keep receiving sessions until their rows are deleted |
| Remove a relay | `DELETE FROM relay WHERE id = '…';` Its peer bindings go with it |
| Logs | `journalctl -u gnl-control -f` |
| Back up | `pg_dump gamenolag`. The database is the whole state; the binary is stateless |

## Building the binaries yourself

Needs Go 1.25 or newer:

```bash
go test ./...
GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go build -trimpath -o dist/ ./cmd/...
```

For the deeper operator story — first relay, binding a peer by hand, what each
check proves — see the [P1 runbook](../p1-runbook.md).
