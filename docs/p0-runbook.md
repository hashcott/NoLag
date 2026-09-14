# P0 Runbook: route quality measurement

The question: does any VPS provider give a better path to regional game servers
than a Vietnamese ISP's default route, at peak hours?

Nothing else gets built until this has an answer. See the design document
section 13.

## What to rent

**Landmarks** — hosts we control, placed where the game servers are. Rent the
smallest instance each provider offers.

| Landmark | Where | Why |
|---|---|---|
| `landmark-sgp` | AWS `ap-southeast-1` or Azure `southeastasia` | Main region for Vietnamese players |
| `landmark-tyo` | AWS `ap-northeast-1` or Azure `japaneast` | Where the matchmaker spills over |

A landmark must be a host we control, not a game server or a random internet
address: a real game server does not answer stray probes, and a random host
rate-limits them. Measuring against something that ignores us measures the
wrong thing.

**Candidate VPSes** — at least three, each from a *different* provider, all in
or near Singapore. Different providers is the point: they buy different transit,
and transit quality is the entire variable being tested. Must be KVM, since P1
needs `/dev/net/tun` on whichever one wins.

**Measurement clients** — one machine on each Vietnamese ISP you intend to
support: Viettel, VNPT, FPT. A home PC or a cheap mini PC left running is fine.
It needs no special hardware; it needs to be on that ISP's network.

## Setup

Build once, copy everywhere:

```bash
GOOS=linux GOARCH=amd64 go build -o gnl-probe ./cmd/gnl-probe
```

**On each landmark.** `--allow` takes the public addresses permitted to probe
it: every candidate VPS, plus every Vietnamese measurement client.

```bash
sudo ./install-probe.sh --mode server \
  --allow <vps-1-ip>,<vps-2-ip>,<vps-3-ip>,<vn-client-1-ip>,<vn-client-2-ip>
```

**On each candidate VPS.** It is both an echo server (so Vietnamese clients can
measure leg B against it) and a client (so it can measure leg C to the
landmarks):

```bash
sudo ./install-probe.sh --mode server --allow <vn-client-1-ip>,<vn-client-2-ip>
sudo ./install-probe.sh --mode client
```

Then add cron. Every 20 minutes, one 60-second run per landmark:

```cron
*/20 * * * * /usr/local/bin/gnl-probe client -target <landmark-sgp-ip>:51830 \
  -leg C -from vps-sgp-vultr -to landmark-sgp -duration 60s \
  -out /var/lib/gnl/results.jsonl
```

**On each Vietnamese measurement client.** One leg A run per landmark, and one
leg B run per candidate VPS:

```cron
*/20 * * * * /usr/local/bin/gnl-probe client -target <landmark-sgp-ip>:51830 \
  -leg A -from vn-viettel -to landmark-sgp -duration 60s -out /var/lib/gnl/results.jsonl
*/20 * * * * /usr/local/bin/gnl-probe client -target <vps-1-ip>:51830 \
  -leg B -from vn-viettel -to vps-sgp-vultr -duration 60s -out /var/lib/gnl/results.jsonl
*/20 * * * * /usr/local/bin/gnl-probe client -target <vps-2-ip>:51830 \
  -leg B -from vn-viettel -to vps-sgp-digitalocean -duration 60s -out /var/lib/gnl/results.jsonl
```

Names in `-from` and `-to` must be **identical everywhere**. The analysis joins
legs by those strings; a VPS called `vps-sgp-vultr` on one machine and
`vultr-sgp` on another produces two candidates, each missing half its data.

## Before the fleet: one real dry run

Everything in this phase was written and tested on macOS. The server path of
`install-probe.sh` — the systemd unit, the iptables chain, the `systemctl
enable --now` — has never been executed against real systemd and real netfilter.
A sandbox run with those commands stubbed proves the script issues them in the
right order with the right arguments; it cannot prove the unit file is valid to a
real systemd, or that the rules actually take effect.

So before installing on the whole fleet, do one throwaway run:

```bash
# on a scratch Linux VPS you are willing to destroy
sudo ./install-probe.sh --mode server --allow <your-ip>
systemctl status gnl-probe        # must be active (running)
iptables -L GNL_PROBE -n -v       # ACCEPT for your IP, then DROP
ss -ulnp | grep 51830             # bound on all interfaces
```

Then probe it from the allowed address (expect replies) and from a different
address (expect silence). If both hold, the script is good for the fleet.

Doing this on one host costs ten minutes. Skipping it risks discovering a bad
unit file on every host at once, at the start of a week-long campaign.

## Running it

Leave it for **seven full days**. Do not stop early on a good first evening —
the whole point is what happens at peak hours across a week, including the
worst one.

Check part-way that data is accumulating:

```bash
wc -l /var/lib/gnl/results.jsonl
tail -3 /var/lib/gnl/results.jsonl
```

## Reading the answer

Collect every `results.jsonl` onto one machine, concatenate, and run:

```bash
cat vn-*/results.jsonl vps-*/results.jsonl > campaign.jsonl
gnl-analyze -in campaign.jsonl
```

The table has one row per **VPS and landmark pair**, because a provider can beat
the ISP's route to Singapore while losing to Tokyo. Read the rows, not just the
final verdict line.

If the output begins with a `WARNING: N unparseable line(s) skipped`, a machine
died mid-write at some point. A handful of torn lines out of thousands is normal
and the rest of the campaign is still good. A large count means something else is
wrong — check that every host wrote to a local disk rather than a network mount.

A candidate passes only when all five hold at peak hours:

- `B + C < A` — the tunnel is genuinely faster
- jitter (p95 − p50) **under** 10 ms on both tunnel legs
- loss **under** 0.5% on both tunnel legs
- p99 **under** p50 + 30 ms on both tunnel legs
- at least 20 runs inside the peak window, counted on the **weaker** of the two
  tunnel legs

Those thresholds are strict: a value sitting exactly on a bar fails. Six lost
packets out of 1200 is exactly 0.5%, and that is a fail, not a pass.

The run floor exists because a median taken over one sample is not evidence. If a
candidate reports too few runs, the verdict says so in its own words — "too
little data to judge, not evidence of a bad path" — and that is a different
finding from a slow route. It means collection broke; fix the cron and keep
running, do not write the provider off.

`-min-runs` lowers the floor. Use it only to inspect a partial campaign, never to
reach a GO.

Exit code 0 means GO, exit code 1 means NO-GO.

## What each outcome means

**At least one PASS.** Proceed to P1 with that provider. Record which one and
what the numbers were: it becomes the baseline every later change is judged
against.

**Everything FAILS on latency.** The providers tried do not have a better path.
Try more — different providers buy different transit, and this is the one
variable worth spending money on. If a broad set all fail, there is no product,
and that is a real answer: stopping here costs one week instead of six months.

**Everything FAILS on jitter or loss, while latency wins.** Do not proceed yet.
A jittery tunnel is worse to play on than a slower steady ISP route, and
shipping it produces users who cannot say what is wrong but know it is worse.
Try providers with better peering, or a different region.
