package bootstrap

import (
	"os"
	"strings"
	"testing"
)

func TestSeededConfigExposesOnlyServerListeners(t *testing.T) {
	t.Parallel()

	content, err := os.ReadFile("../../config/config.yaml")
	if err != nil {
		t.Fatalf("read seeded config: %v", err)
	}
	config := string(content)

	for _, required := range []string{
		"mixed-port: 7890",
		"allow-lan: true",
		"bind-address: \"*\"",
		"external-controller: 127.0.0.1:9090",
	} {
		if !strings.Contains(config, required) {
			t.Errorf("seeded config is missing %q", required)
		}
	}

	for _, forbidden := range []string{
		"tun:",
		"tproxy-port:",
		"redir-port:",
		"external-controller: 0.0.0.0",
	} {
		if strings.Contains(config, forbidden) {
			t.Errorf("seeded config contains forbidden server-mode setting %q", forbidden)
		}
	}
}
