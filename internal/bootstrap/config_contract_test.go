package bootstrap

import (
	"crypto/sha256"
	"fmt"
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
		"external-controller: 0.0.0.0:9090",
		"external-ui: /usr/local/share/mihomo/ui",
	} {
		if !strings.Contains(config, required) {
			t.Errorf("seeded config is missing %q", required)
		}
	}

	for _, forbidden := range []string{
		"tun:",
		"tproxy-port:",
		"redir-port:",
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
		"GoogleCN: {type: file, behavior: classical, format: yaml, path: /usr/local/share/mihomo/rules/Ruleset/GoogleCN.yaml}",
		"Bing: {type: file, behavior: classical, format: yaml, path: /usr/local/share/mihomo/rules/Ruleset/Bing.yaml}",
		"OneDrive: {type: file, behavior: classical, format: yaml, path: /usr/local/share/mihomo/rules/Ruleset/OneDrive.yaml}",
		"Microsoft: {type: file, behavior: classical, format: yaml, path: /usr/local/share/mihomo/rules/Ruleset/Microsoft.yaml}",
		"Telegram: {type: file, behavior: classical, format: yaml, path: /usr/local/share/mihomo/rules/Ruleset/Telegram.yaml}",
		"ChinaCompanyIp: {type: file, behavior: ipcidr, format: yaml, path: /usr/local/share/mihomo/rules/ChinaCompanyIp.yaml}",
		"ChinaIp: {type: file, behavior: ipcidr, format: yaml, path: /usr/local/share/mihomo/rules/ChinaIp.yaml}",
		"RULE-SET,ChinaIp,🎯 全球直连",
	} {
		if !strings.Contains(config, required) {
			t.Errorf("seeded config is missing %q", required)
		}
	}
	if strings.Contains(config, "raw.githubusercontent.com") || strings.Contains(config, "type: http") || strings.Contains(config, "GEOIP,CN,") {
		t.Error("seeded config depends on an online rule or subscription provider")
	}
}

func TestMihomoTemplateAndRuntimeAssetsArePinned(t *testing.T) {
	t.Parallel()

	template, err := os.ReadFile("../../config/config.yaml")
	if err != nil {
		t.Fatalf("read seeded config: %v", err)
	}
	if got, want := fmt.Sprintf("%x", sha256.Sum256(template)), "705568e96a76961b1593ac59163b07bb1aba5de1f7532794f9ab86afded3e07a"; got != want {
		t.Fatalf("seeded config SHA-256 = %s, want pinned %s", got, want)
	}
	dockerfile, err := os.ReadFile("../../Dockerfile")
	if err != nil {
		t.Fatalf("read Dockerfile: %v", err)
	}
	for _, pin := range []string{
		"MIHOMO_VERSION=v1.19.30",
		"MIHOMO_SHA256_AMD64=cbe553d0319a414bd3a372c5976a252155b2c4882b66bce88a4d6bba9571a553",
		"MIHOMO_SHA256_ARM64=58896873736d28628f66de3677c8654fa0f180662523148e136cff4f6e890069",
		"ACL4SSR_REF=6e27259b8625e360699c014f98f978ee7408c644",
		"ACL4SSR_SHA256=72229e2f0a38fc9776720a20dd4ecb44fdd0b0704bbf1f5141732562a237bff2",
		"EXTERNAL_UI_VERSION=v1.273.0",
		"EXTERNAL_UI_SHA256=076e05d2e3dc6641a0ec281aa4b97a18193fbcc379d139762c32d90adb22793c",
		"EXTERNAL_UI_LICENSE_SHA256=cd0735ba06f26a0008bbca399890c7ca87fe129aacc302c2e33fb03e60a4e8c3",
	} {
		if !strings.Contains(string(dockerfile), pin) {
			t.Errorf("Dockerfile is missing pinned asset %q", pin)
		}
	}
}

func TestDockerBuildContextExcludesSubscriptionSecret(t *testing.T) {
	t.Parallel()

	content, err := os.ReadFile("../../.dockerignore")
	if err != nil {
		t.Fatalf("read .dockerignore: %v", err)
	}
	if !strings.Contains("\n"+string(content)+"\n", "\nsubscription.url\n") {
		t.Fatal(".dockerignore does not exclude subscription.url")
	}
}
