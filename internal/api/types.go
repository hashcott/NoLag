// Package api is the wire contract between the relay agent and the control
// plane. They are deployed separately and will be at different versions at
// times, so this package holds types and validation only: no I/O, no database
// handles, no HTTP. Anything that cannot be published belongs elsewhere, because
// the agent is open source.
package api

import (
	"fmt"
	"net/netip"
)

// Peer is one client the relay must accept.
//
// InnerIP is always a single-address prefix. WireGuard's AllowedIPs on the relay
// side is what stops one client claiming another's address, so a peer that
// carried a wider prefix could receive traffic meant for somebody else.
type Peer struct {
	PublicKey string `json:"public_key"`
	InnerIP   string `json:"inner_ip"`
}

// Validate rejects a peer that must never reach the kernel.
func (p Peer) Validate() error {
	if p.PublicKey == "" {
		return fmt.Errorf("api: peer has no public key")
	}
	if p.InnerIP == "" {
		return fmt.Errorf("api: peer %.8s has no inner IP", p.PublicKey)
	}
	pfx, err := netip.ParsePrefix(p.InnerIP)
	if err != nil {
		return fmt.Errorf("api: peer %.8s inner IP %q: %w", p.PublicKey, p.InnerIP, err)
	}
	if !pfx.Addr().Is4() || pfx.Bits() != 32 {
		return fmt.Errorf("api: peer %.8s inner IP %q must be a single IPv4 address (/32)", p.PublicKey, p.InnerIP)
	}
	return nil
}

// RegisterRequest is sent once, when a contributor first installs the agent.
type RegisterRequest struct {
	ContributorKey string `json:"contributor_key"`
	PublicKey      string `json:"public_key"` // base64 WireGuard public key
	Endpoint       string `json:"endpoint"`   // host:port reachable from the internet
	Region         string `json:"region"`
	Hostname       string `json:"hostname"`
}

// RegisterResponse carries the relay's own token and address assignment.
// RelayToken is shown exactly once and is never retrievable afterwards.
type RegisterResponse struct {
	RelayID     string `json:"relay_id"`
	RelayToken  string `json:"relay_token"`
	InnerSubnet string `json:"inner_subnet"` // e.g. "10.77.0.0/16"
	InnerIP     string `json:"inner_ip"`     // the relay's own address, e.g. "10.77.0.1/16"
	ListenPort  int    `json:"listen_port"`
}

// RelayStatus is what the agent reports on every sync.
type RelayStatus struct {
	ActivePeers  int     `json:"active_peers"` // handshake seen within the last 180s
	TotalPeers   int     `json:"total_peers"`
	RxBytes      int64   `json:"rx_bytes"`
	TxBytes      int64   `json:"tx_bytes"`
	LoadAvg1     float64 `json:"load_avg_1"`
	AgentVersion string  `json:"agent_version"`
}

// SyncRequest carries status up.
type SyncRequest struct {
	Status RelayStatus `json:"status"`
}

// SyncResponse carries the complete desired state down.
//
// Complete, not incremental: the agent diffs it against the kernel and applies
// the difference, so a dropped response, a restarted agent or a hand-edited
// interface all self-heal on the next poll.
type SyncResponse struct {
	Peers     []Peer   `json:"peers"`
	GameCIDRs []string `json:"game_cidrs"`
	PollSecs  int      `json:"poll_secs"`
}

// ErrorResponse is the body of every non-2xx reply.
type ErrorResponse struct {
	Error string `json:"error"`
	Hint  string `json:"hint,omitempty"`
}
