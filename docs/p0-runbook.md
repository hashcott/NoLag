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

Also build `gnl-analyze` now, so it is ready when the campaign finishes:

```bash
go build -o gnl-analyze ./cmd/gnl-analyze
```

`gnl-analyze` never runs on a measurement host — only `gnl-probe` gets deployed
to the landmarks, VPSes and Vietnamese clients. `gnl-analyze` runs later, once,
on whichever machine you collect the results files onto (see "Reading the
answer" below), so build it for that machine, not necessarily `linux/amd64`.

**On each landmark.** `--allow` takes the public addresses permitted to probe
it: every candidate VPS, plus every Vietnamese measurement client.

```bash
sudo ./install-probe.sh --mode server \
  --allow <vps-1-ip>,<vps-2-ip>,<vps-3-ip>,<vn-viettel-ip>,<vn-vnpt-ip>,<vn-fpt-ip>
```

Every address that will ever probe this host must be in this list. One missing
client does not produce an error — it produces a week of total loss for that ISP,
which reads like a terrible route rather than a closed port.

**On each candidate VPS.** It is both an echo server (so Vietnamese clients can
measure leg B against it) and a client (so it can measure leg C to the
landmarks):

```bash
sudo ./install-probe.sh --mode server --allow <vn-viettel-ip>,<vn-vnpt-ip>,<vn-fpt-ip>
sudo ./install-probe.sh --mode client
```

Every address that will ever probe this host must be in this list. One missing
client does not produce an error — it produces a week of total loss for that ISP,
which reads like a terrible route rather than a closed port.

Then add cron. Every 20 minutes, one 60-second run per landmark — one line per
landmark, so two rented landmarks means two lines here, not one:

```cron
*/20 * * * * /usr/local/bin/gnl-probe client -target <landmark-sgp-ip>:51830 -leg C -from vps-sgp-vultr -to landmark-sgp -duration 60s -out /var/lib/gnl/results.jsonl
*/20 * * * * /usr/local/bin/gnl-probe client -target <landmark-tyo-ip>:51830 -leg C -from vps-sgp-vultr -to landmark-tyo -duration 60s -out /var/lib/gnl/results.jsonl
```

Crontab has no line continuation — the command field runs to end of line — so
each entry above is written on one line, however long, and pasted as-is. A file
with a backslash continuation is rejected whole by `crontab file`, which leaves
whatever crontab already existed (usually empty) in place with no error visible
at a glance.

The installer has already created `/var/lib/gnl` and given it to the account that
ran `sudo`, so either that account's crontab or root's will work. Do not paste
these lines into `/etc/cron.d`: files there need an extra user field before the
command, and without it cron reads `/usr/local/bin/gnl-probe` as a username and
silently discards the entry.

If you run `gnl-probe` by hand to test before trusting it to cron, run it as the
same account the cron entries above will use. The installer only chowns the
`/var/lib/gnl` *directory*; a hand run under `sudo` creates `results.jsonl`
owned by root, and every cron run afterwards under the unprivileged account
fails silently with EACCES into cron mail that nobody reads. If you already did
this, `chown` the file to match.

**On each Vietnamese measurement client.** First install the binary, exactly as on the VPS hosts. Client mode configures no
firewall and no service; it only places `gnl-probe` where cron can find it:

```bash
sudo ./install-probe.sh --mode client
```

Then add cron. One leg A run per landmark (repeat the leg-A line once per rented
landmark — a missing leg-A-to-Tokyo line makes every Tokyo row fail "no leg A
baseline for this ISP"), and one leg B run per candidate VPS:

```cron
*/20 * * * * /usr/local/bin/gnl-probe client -target <landmark-sgp-ip>:51830 -leg A -from vn-viettel -to landmark-sgp -duration 60s -out /var/lib/gnl/results.jsonl
*/20 * * * * /usr/local/bin/gnl-probe client -target <landmark-tyo-ip>:51830 -leg A -from vn-viettel -to landmark-tyo -duration 60s -out /var/lib/gnl/results.jsonl
*/20 * * * * /usr/local/bin/gnl-probe client -target <vps-1-ip>:51830 -leg B -from vn-viettel -to vps-sgp-vultr -duration 60s -out /var/lib/gnl/results.jsonl
*/20 * * * * /usr/local/bin/gnl-probe client -target <vps-2-ip>:51830 -leg B -from vn-viettel -to vps-sgp-digitalocean -duration 60s -out /var/lib/gnl/results.jsonl
```

Crontab has no line continuation — the command field runs to end of line — so
each entry above is written on one line, however long, and pasted as-is. A file
with a backslash continuation is rejected whole by `crontab file`, which leaves
whatever crontab already existed (usually empty) in place with no error visible
at a glance.

The block above is for the Viettel host. On the VNPT and FPT machines use the
same four lines with `-from vn-vnpt` and `-from vn-fpt`. Changing `-from` is not
cosmetic: the analysis groups every leg by that exact string, so a box that still
says `vn-viettel` files its measurements under Viettel's baseline and the two
ISPs blend into one candidate that looks complete and is wrong.

The installer has already created `/var/lib/gnl` and given it to the account that
ran `sudo`, so either that account's crontab or root's will work. Do not paste
these lines into `/etc/cron.d`: files there need an extra user field before the
command, and without it cron reads `/usr/local/bin/gnl-probe` as a username and
silently discards the entry.

If you run `gnl-probe` by hand to test before trusting it to cron, run it as the
same account the cron entries above will use, or `chown` the resulting
`results.jsonl` back afterwards — a root-owned file from a `sudo` test run fails
every later cron-owned append with EACCES, silently, into cron mail nobody
reads.

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

On a freshly provisioned VPS, let `chrony` (or whatever NTP client the image
ships) finish its initial sync before starting collection. RTT is derived from
`time.Now()` read twice on the same host, once at send and once at receive; a
large step correction landing mid-flight of a brand-new VM's clock — common
right after boot — corrupts or discards samples taken around it. Check
`chronyc tracking` (or equivalent) shows a small, stable offset before you add
the cron entries.

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

Collect each host's `results.jsonl` into a directory named after that host —
`vn-viettel/results.jsonl`, `vn-vnpt/results.jsonl`, `vn-fpt/results.jsonl`,
`vps-sgp-vultr/results.jsonl`, `vps-sgp-digitalocean/results.jsonl`, and so on —
onto one machine, then concatenate and run `gnl-analyze` there (built earlier,
in "Setup"; it never runs on a measurement host):

```bash
cat vn-*/results.jsonl vps-*/results.jsonl > campaign.jsonl
gnl-analyze -in campaign.jsonl
```

The table has one row per **ISP, VPS and landmark** combination. The same VPS
appears once per ISP and once per landmark, and those rows can disagree: each ISP
has its own baseline, so a provider can beat Viettel's route while losing to
FPT's. Read the rows, not just the final verdict line.

If the output contains, near the top, a `WARNING: N unparseable line(s) skipped`, a machine
died mid-write at some point. A handful of torn lines out of thousands is normal
and the rest of the campaign is still good. A large count means something else is
wrong — check that every host wrote to a local disk rather than a network mount.

The JITTER and LOSS columns show the worst single run of the week on either
tunnel leg — useful context, but not what decides the verdict. The J-OVER and
L-OVER columns show the *share* of runs that breached each bar, and that share
is the actual gate (see "A candidate passes" below): no more than
`-max-breach-pct` of runs may breach. A single bad evening on a home
connection, or one VPS reboot, no longer fails an otherwise clean week on its
own — that was the old behavior and it failed almost any real path.

The peak window is **20:00–22:00 Vietnam time (UTC+7)** by default — this is the
window the design document prescribes, and it is the verdict of record. Widen
it with `-peak-start`/`-peak-end` if you want to look at the shoulder hours, but
treat any run outside 20:00–22:00 as informational only: the shoulder hours are
less congested, which flatters both the baseline and the tunnel, so a verdict
taken over a wider window is not the one that counts. `gnl-analyze` prints the
window it used on every run. Records outside it are discarded entirely.

Timezones cannot be got wrong here: every record carries an absolute UTC
timestamp and the conversion happens at analysis time, so it does not matter what
timezone any measuring host is set to. `-peak-start` and `-peak-end` change the
window if you ever need a different one.

A candidate passes only when all six hold at peak hours:

- `B + C < A` by at least `-min-gain-ms` (default 5 ms) — the tunnel must beat
  the ISP route clearly, not by an amount two independently-measured legs
  summed against a third cannot tell apart from noise
- jitter (p95 − p50) at or over 10 ms on no more than `-max-breach-pct` of runs
  (default 5%) on either tunnel leg
- loss at or over 0.5% on no more than `-max-breach-pct` of runs on either
  tunnel leg
- p99 at or over p50 + 30 ms on no more than `-max-breach-pct` of runs on
  either tunnel leg
- at least 20 usable runs inside the peak window, counted on the **weakest of
  all three legs, baseline included** — leg A is the denominator of the whole
  comparison, so a single leg-A sample taken during one ISP spike must not be
  allowed to carry the entire verdict on its own

The three latency and loss bars are strict: a run sitting exactly on the bar
counts as a breach, not a pass. Six lost packets out of 1200 is exactly 0.5%,
and that run breaches. What decides the verdict is the *share* of runs that
breach, not whether any run ever breaches — gating on the single worst run
across ~84 runs a week fails almost any real path, since the odds that none of
them ever has a bad evening are close to zero. The run floor is a minimum:
exactly 20 usable runs passes. A run that lost every packet carries no latency
information and does not count toward the floor, even though it still counts
toward loss.

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

Record **which ISP and which landmark** it passed for, not just which provider. A
PASS on one ISP's row is not a statement about the other two, and P1 inherits
whatever you write down here.

**Everything FAILS on latency.** The providers tried do not have a better path.
Try more — different providers buy different transit, and this is the one
variable worth spending money on. If a broad set all fail, there is no product,
and that is a real answer: stopping here costs one week instead of six months.

**Everything FAILS on jitter or loss, while latency wins.** Do not proceed yet.
A jittery tunnel is worse to play on than a slower steady ISP route, and
shipping it produces users who cannot say what is wrong but know it is worse.
Try providers with better peering, or a different region.
