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

func TestSeededConfigUsesLocalACL4SSRRulesAndMemorySubscription(t *testing.T) {
	t.Parallel()

	content, err := os.ReadFile("../../config/config.yaml")
	if err != nil {
		t.Fatalf("read seeded config: %v", err)
	}
	config := string(content)
	for _, required := range []string{
		"path: ./subscription.yaml",
		"RULE-SET,LocalAreaNetwork,🎯 全球直连",
		"RULE-SET,BanAD,🛑 广告拦截",
		"RULE-SET,ProxyGFWlist,🚀 节点选择",
		"MATCH,🐟 漏网之鱼",
		"GoogleCN: {type: file, behavior: classical, format: yaml, path: /usr/local/share/ssclash/rules/Ruleset/GoogleCN.yaml}",
		"Bing: {type: file, behavior: classical, format: yaml, path: /usr/local/share/ssclash/rules/Ruleset/Bing.yaml}",
		"OneDrive: {type: file, behavior: classical, format: yaml, path: /usr/local/share/ssclash/rules/Ruleset/OneDrive.yaml}",
		"Microsoft: {type: file, behavior: classical, format: yaml, path: /usr/local/share/ssclash/rules/Ruleset/Microsoft.yaml}",
		"Telegram: {type: file, behavior: classical, format: yaml, path: /usr/local/share/ssclash/rules/Ruleset/Telegram.yaml}",
		"ChinaCompanyIp: {type: file, behavior: ipcidr, format: yaml, path: /usr/local/share/ssclash/rules/ChinaCompanyIp.yaml}",
	} {
		if !strings.Contains(config, required) {
			t.Errorf("seeded config is missing %q", required)
		}
	}
	if strings.Contains(config, "raw.githubusercontent.com") || strings.Contains(config, "type: http") {
		t.Error("seeded config depends on an online rule or subscription provider")
	}
}
