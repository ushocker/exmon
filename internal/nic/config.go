package nic

// Config controls the network adapter monitor.
type Config struct {
	Enabled          bool     `json:"enabled"`
	PollSeconds      int      `json:"pollSeconds"`      // how often counters are read
	HeartbeatSeconds int      `json:"heartbeatSeconds"` // NIC/status line at least this often; 0 = off
	IncludeVirtual   bool     `json:"includeVirtual"`   // also watch virtual adapters (Hyper-V, VPN, ...)
	VirtualPatterns  []string `json:"virtualPatterns"`  // name/description fragments that mark an adapter as virtual
}

// DefaultConfig returns the settings used when nothing else is configured.
func DefaultConfig() Config {
	return Config{
		Enabled:          true,
		PollSeconds:      15,
		HeartbeatSeconds: 60,
		VirtualPatterns: []string{
			"Hyper-V", "vEthernet", "VMware", "VirtualBox", "Bluetooth", "WAN Miniport",
			"Wi-Fi Direct", "Kernel Debug", "Loopback", "TAP-", "Wintun", "WireGuard",
			"PANGP", "Cisco AnyConnect", "Zscaler", "Fortinet", "Juniper", "Tailscale",
		},
	}
}
