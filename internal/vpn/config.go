package vpn

// Config controls VPN detection. The pattern and process lists come from the
// AzureNetMonitor script; add your organisation's VPN client names.
type Config struct {
	Enabled          bool     `json:"enabled"`
	PollSeconds      int      `json:"pollSeconds"`
	HeartbeatSeconds int      `json:"heartbeatSeconds"` // VPN/status line at least this often; 0 = off
	PingCount        int      `json:"pingCount"`        // pings to a built-in VPN's server each poll
	AdapterPatterns  []string `json:"adapterPatterns"`  // name/description fragments of VPN adapters
	ProcessNames     []string `json:"processNames"`     // VPN client processes (without .exe)
}

// DefaultConfig returns the settings used when nothing else is configured.
func DefaultConfig() Config {
	return Config{
		Enabled:          true,
		PollSeconds:      30,
		HeartbeatSeconds: 60,
		PingCount:        3,
		AdapterPatterns: []string{
			"Cisco AnyConnect", "GlobalProtect", "PANGP", "Palo Alto", "Zscaler", "Fortinet",
			"FortiClient", "WireGuard", "OpenVPN", "TAP-Windows", "SonicWall", "Pulse Secure",
			"Juniper", "Check Point", "Tailscale",
		},
		ProcessNames: []string{"vpnui", "vpnagent", "PanGPA", "PanGPS", "ZSATunnel", "FortiClient", "dsNcService", "vpnc", "tailscaled"},
	}
}
