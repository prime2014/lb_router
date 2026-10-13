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
	HTTPPort   string `mapstructure:"http_port"`
	HTTPSPort  string `mapstructure:"https_port"`
	ServerName string `mapstructure:"server_name"`
	CertFile   string `mapstructure:"certFile"`
	CertKey    string `mapstructure:"certKey"`
}

type RouterConfig struct {
	Mode          string   `mapstructure:"mode"`
	ProxyProtocol bool     `mapstructure:"proxy_protocol"`
	Algorithm     string   `mapstructure:"algorithm"`
	URLs          []string `mapstructure:"urls"`
}

func (c *Config) Normalize() {
	if c.Server.HTTPPort == "" {
		c.Server.HTTPPort = "80"
	}
	if c.Server.HTTPSPort == "" {
		c.Server.HTTPSPort = "443"
	}
	if c.Router.Mode == "" {
		c.Router.Mode = "http"
	}
	if c.Router.Algorithm == "" {
		c.Router.Algorithm = "round_robin"
	}

}

func DefaulConfig() Config {
	return Config{
		Server: ServerConfig{
			HTTPPort:  "80",
			HTTPSPort: "443",
		},
		Router: RouterConfig{
			Mode:          "http",
			Algorithm:     "round_robin",
			ProxyProtocol: false,
		},
		Logs: LogConfig{
			Path:  "./logs",
			Level: "debug",
		},
	}
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
