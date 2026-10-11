package router

import (
	"strings"
)

// Config represents the raw config file structure
type Config struct {
	Server ServerConfig `mapstructure:"server"`
	Router RouterConfig `mapstructure:"router"`
	Logs   LogConfig    `mapstructure:"logs"`
}

type ServerConfig struct {
	Port       string `mapstructure:"port"`
	Listener   string `mapstructure:"listener"`
	ServerName string `mapstructure:"server_name"`
	CertFile   string `mapstructure:"certFile"`
	CertKey    string `mapstructure:"certKey"`
}

type RouterConfig struct {
	Mode      string   `mapstructure:"mode"`
	Algorithm string   `mapstructure:"algorithm"`
	URLs      []string `mapstructure:"urls"`
}

type LogConfig struct {
	Path  string `mapstructure:"path"`
	Level string `mapstructure:"level"`
}

// Map string algorithm names to RoutingType enum
func ParseRoutingType(raw string) RoutingType {
	switch strings.ToLower(raw) {
	case "round_robin", "roundrobin", "1":
		return RoundRobin
	case "ip_hash", "iphash", "2":
		return IPHash
	case "consistent_hash", "consistenthash", "3":
		return ConsistentHash
	default:
		return RoundRobin
	}
}

// Map string connection mode to ConnectionType enum
func ParseConnectionType(raw string) ConnectionType {
	switch strings.ToLower(raw) {
	case "tcp":
		return TcpMode
	case "http":
		return HttpMode
	default:
		return HttpMode
	}
}
