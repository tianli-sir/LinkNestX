package config

import (
	"fmt"
	"net"
	"strings"
)

func validateExposure(c Config) error {
	host, _, err := net.SplitHostPort(c.BindAddress)
	if err != nil {
		return fmt.Errorf("invalid bind address")
	}
	ip := net.ParseIP(host)
	if host == "localhost" || (ip != nil && ip.IsLoopback()) {
		return nil
	}
	if c.JWTSigningKey == c.JWTRefreshKey {
		return fmt.Errorf("public binding requires distinct signing and refresh secrets")
	}
	for _, key := range []string{c.JWTSigningKey, c.JWTRefreshKey} {
		if len(key) < 32 || len(key) > 4096 || strings.Contains(strings.ToLower(key), "change-me") {
			return fmt.Errorf("public binding requires independently generated secrets of at least 32 bytes")
		}
		distinct := map[rune]bool{}
		for _, r := range key {
			distinct[r] = true
		}
		if len(distinct) < 12 {
			return fmt.Errorf("public binding requires independently generated high-entropy secrets")
		}
	}
	return nil
}
