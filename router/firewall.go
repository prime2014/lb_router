package router

type FirewallConfig struct {
	BlockedIps  map[string]bool
	MaxBodySize int64
}

func NewFirewall(blockedIps map[string]bool, maxBodySize int64) *FirewallConfig {
	return &FirewallConfig{
		BlockedIps:  blockedIps,
		MaxBodySize: maxBodySize,
	}
}

func (f *FirewallConfig) ValidateblockedIps(clientIp string) bool {
	if f.BlockedIps[clientIp] {
		return true
	}

	return false
}
