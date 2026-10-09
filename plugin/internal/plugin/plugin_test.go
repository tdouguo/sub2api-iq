package plugin

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/feeeei/sub2api-plugin-framework/hostemu"
	"github.com/feeeei/sub2api-plugin-framework/plugintest"
	"github.com/feeeei/sub2api-plugin-framework/sdk"

	pluginroot "github.com/tdouguo/sub2api-iq/plugin"
)

func info(t *testing.T) sdk.Info {
	t.Helper()
	return sdk.MustInfoFromManifestSource(pluginroot.ManifestSource)
}

func factory(t *testing.T) sdk.Plugin { return New(info(t)) }

// TestConformance 运行框架的标准一致性测试：身份、配置前健康、配置生命周期、转发帧协议。
// 其中 streaming_response 子测试专门防止「先把响应读进内存再交还宿主」的回归。
func TestConformance(t *testing.T) {
	expected := info(t)
	plugintest.RunConformance(t, factory, plugintest.ConformanceOptions{ExpectedInfo: &expected})
}

func TestConfigDefaults(t *testing.T) {
	cfg, err := Codec.Decode(nil)
	if err != nil {
		t.Fatal(err)
	}
	// 默认必须关闭：安装插件不应立即产生评测流量与费用。
	if cfg.Enabled || cfg.DefaultEnabled {
		t.Fatalf("默认配置不应启用评测: %+v", cfg)
	}
	if cfg.Queue.WorkerCount != 4 || cfg.Queue.MaxSize != 256 {
		t.Fatalf("队列默认值不符: %+v", cfg.Queue)
	}
	if cfg.IQAPI.TimeoutSec != 30 || cfg.IQAPI.Model == "" {
		t.Fatalf("IQ API 默认值不符: %+v", cfg.IQAPI)
	}
	if !cfg.LoopDetection.Enabled || !cfg.LoopDetection.CheckHost {
		t.Fatalf("防循环检测必须默认开启: %+v", cfg.LoopDetection)
	}
}

func TestConfigValidation(t *testing.T) {
	rejected := map[string]string{
		"未知字段":       `{"__unknown__":1}`,
		"启用但缺少地址":    `{"enabled":true}`,
		"非法地址":       `{"iq_api":{"base_url":"ftp://x"}}`,
		"超时越界":       `{"iq_api":{"timeout_seconds":0}}`,
		"重试越界":       `{"iq_api":{"max_retries":9}}`,
		"worker 越界":  `{"queue":{"worker_count":0}}`,
		"队列容量越界":     `{"queue":{"max_size":0}}`,
		"采样率越界":      `{"accounts":{"1":{"sample_ratio":1.5}}}`,
		"空账号 ID":     `{"accounts":{" ":{}}}`,
		"采集上限越界":     `{"capture":{"max_request_body_bytes":10}}`,
		"TTL 超过宿主上限": `{"capture":{"record_ttl_hours":99999}}`,
		"根节点为数组":     `[]`,
	}
	for name, raw := range rejected {
		if _, err := Codec.Decode([]byte(raw)); err == nil {
			t.Errorf("%s 应被拒绝: %s", name, raw)
		}
	}

	// 未启用时允许留空，安装后即可保存一份空配置。
	if _, err := Codec.Decode([]byte(`{"enabled":false}`)); err != nil {
		t.Errorf("未启用时应允许缺少 base_url: %v", err)
	}
	cfg, err := Codec.Decode([]byte(`{"enabled":true,"iq_api":{"base_url":"https://iq.example.com/v1","api_key":"k"}}`))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.IQAPI.BaseURL != "https://iq.example.com/v1" {
		t.Fatalf("地址未保留: %+v", cfg.IQAPI)
	}
}

// TestNormalizeIsIdempotent 覆盖宿主保存失败回滚时会重复校验+应用的场景。
func TestNormalizeIsIdempotent(t *testing.T) {
	first, err := Codec.Normalize([]byte(`{"enabled":true,"iq_api":{"base_url":"https://iq.example.com"}}`))
	if err != nil {
		t.Fatal(err)
	}
	second, err := Codec.Normalize(first)
	if err != nil {
		t.Fatal(err)
	}
	if string(first) != string(second) {
		t.Fatalf("规范化必须幂等:\n1=%s\n2=%s", first, second)
	}
}

func TestShouldEvaluatePriority(t *testing.T) {
	p := New(info(t))
	cfg, err := Codec.Decode([]byte(`{
		"enabled": true,
		"default_enabled": false,
		"iq_api": {"base_url": "https://iq.example.com/v1"},
		"accounts": {"42": {"enabled": true, "sample_ratio": 1}, "43": {"enabled": false, "sample_ratio": 1}}
	}`))
	if err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		account int64
		want    bool
	}{
		{42, true},  // 账号规则开启
		{43, false}, // 账号规则显式关闭，优先于默认
		{99, false}, // 未命中规则，回落到 default_enabled=false
	}
	for _, tc := range cases {
		if got := p.shouldEvaluate(&cfg, &sdk.RequestMeta{AccountID: tc.account}); got != tc.want {
			t.Errorf("account %d: shouldEvaluate=%v want %v", tc.account, got, tc.want)
		}
	}

	// 总开关关闭时，任何账号规则都不再生效。
	off, err := Codec.Decode([]byte(`{"enabled":false,"default_enabled":true,"accounts":{"42":{"enabled":true}}}`))
	if err != nil {
		t.Fatal(err)
	}
	if p.shouldEvaluate(&off, &sdk.RequestMeta{AccountID: 42}) {
		t.Error("总开关关闭时不应评测任何账号")
	}
}

func TestSampleRatioBounds(t *testing.T) {
	p := New(info(t))
	cfg, err := Codec.Decode([]byte(`{"enabled":true,"iq_api":{"base_url":"https://iq.example.com/v1"},"accounts":{"7":{"enabled":true,"sample_ratio":0}}}`))
	if err != nil {
		t.Fatal(err)
	}
	if p.shouldEvaluate(&cfg, &sdk.RequestMeta{AccountID: 7}) {
		t.Error("sample_ratio=0 表示全不评测")
	}
	full, err := Codec.Decode([]byte(`{"enabled":true,"default_enabled":true,"iq_api":{"base_url":"https://iq.example.com/v1"}}`))
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 20; i++ {
		if !p.shouldEvaluate(&full, &sdk.RequestMeta{AccountID: 1}) {
			t.Fatal("采样率为 1 时必须全部评测")
		}
	}
}

// TestLoopDetectionSkipsSelf 覆盖防自评循环：评测 API 与被评测请求同站点时必须跳过。
func TestLoopDetectionSkipsSelf(t *testing.T) {
	cfg, err := Codec.Decode([]byte(`{
		"enabled": true,
		"default_enabled": true,
		"iq_api": {"base_url": "https://iq.example.com/v1"}
	}`))
	if err != nil {
		t.Fatal(err)
	}
	p := New(info(t))

	same, _ := http.NewRequest(http.MethodPost, "https://iq.example.com/v1/chat/completions", nil)
	if !p.isLoopRequest(&cfg, same) {
		t.Error("与被评测 API 同 host 的请求必须被判定为循环")
	}

	other, _ := http.NewRequest(http.MethodPost, "https://api.openai.com/v1/chat/completions", nil)
	if p.isLoopRequest(&cfg, other) {
		t.Error("不同 host 的请求不应被判定为循环")
	}

	// 关闭检测后不再拦截。
	disabled, err := Codec.Decode([]byte(`{"iq_api":{"base_url":"https://iq.example.com/v1"},"loop_detection":{"enabled":false}}`))
	if err != nil {
		t.Fatal(err)
	}
	if p.isLoopRequest(&disabled, same) {
		t.Error("检测关闭时不应拦截")
	}
}

func TestHostOf(t *testing.T) {
	cases := map[string]string{
		"https://iq.example.com/v1":        "iq.example.com",
		"http://IQ.Example.com:8080/x?y=1": "iq.example.com:8080",
		"iq.example.com/v1":                "iq.example.com",
		"  https://a.b.c/  ":               "a.b.c",
	}
	for input, want := range cases {
		if got := hostOf(input); got != want {
			t.Errorf("hostOf(%q)=%q want %q", input, got, want)
		}
	}
}

// TestNotConfiguredIsNotSent 覆盖 request_sent 语义：
// 尚未应用配置时请求确定未发出，宿主可以安全地换号重试。
func TestNotConfiguredIsNotSent(t *testing.T) {
	h := plugintest.New(t, New(info(t)))
	req, _ := http.NewRequest(http.MethodGet, "http://127.0.0.1:9/", nil)
	_, err := h.Forward(context.Background(), req, hostemu.ForwardMeta{})
	var te *hostemu.TransportError
	if !errors.As(err, &te) {
		t.Fatalf("期望 error 帧，得到 %T: %v", err, err)
	}
	if te.RequestSent {
		t.Errorf("未配置时 request_sent 必须为 false: %+v", te)
	}
	if !strings.Contains(te.Code, "NOT_CONFIGURED") {
		t.Errorf("错误码应为 NOT_CONFIGURED，得到 %q", te.Code)
	}
}

// TestHealthStatusJSON 保证宿主在应用配置前调用 Health 时也健康，
// 且 status_json 是可解析的 JSON 对象。
func TestHealthStatusJSON(t *testing.T) {
	h := plugintest.New(t, New(info(t)))
	health, err := h.Health(context.Background())
	if err != nil || !health.GetHealthy() {
		t.Fatalf("Health 必须健康: err=%v msg=%s", err, health.GetMessage())
	}
	var status map[string]any
	if err := json.Unmarshal([]byte(health.GetStatusJson()), &status); err != nil {
		t.Fatalf("status_json 必须是 JSON 对象: %v", err)
	}
	if status["configured"] != false {
		t.Errorf("尚未应用配置时 configured 应为 false: %v", status["configured"])
	}
	if _, ok := status["host_services"]; !ok {
		t.Error("status_json 应报告宿主服务可用性")
	}
}

// TestIdentityMatchesManifest 确认二进制内嵌的身份与清单一致 —— 不一致会导致宿主启动即 Kill。
func TestIdentityMatchesManifest(t *testing.T) {
	got := New(info(t)).Info()
	if got.ID != "com.sub2api.iq" {
		t.Errorf("插件 ID 不符: %q", got.ID)
	}
	if got.Version != "0.0.1" {
		t.Errorf("插件版本不符: %q", got.Version)
	}
	if len(got.Capabilities) != 1 || got.Capabilities[0] != "openai.oauth.outbound_transport.v1" {
		t.Errorf("能力声明不符: %v", got.Capabilities)
	}
}

// TestApplyConfigIsIdempotent 覆盖宿主回滚时重复应用同一配置的场景。
func TestApplyConfigIsIdempotent(t *testing.T) {
	p := New(info(t))
	ctx := context.Background()
	raw, err := Codec.Normalize([]byte(`{"enabled":true,"iq_api":{"base_url":"https://iq.example.com/v1"}}`))
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 3; i++ {
		if err := p.ApplyConfig(ctx, raw); err != nil {
			t.Fatalf("第 %d 次应用失败: %v", i+1, err)
		}
	}
	if _, pool, _ := p.current(); pool == nil {
		t.Fatal("应用配置后传输池不应为空")
	}
	// 非法配置必须被拒绝，且保留旧配置。
	if err := p.ApplyConfig(ctx, []byte(`{"nope":1}`)); err == nil {
		t.Fatal("未知字段应被拒绝")
	}
	if cfg, _, _ := p.current(); cfg == nil || !cfg.Enabled {
		t.Fatal("应用失败后必须保留旧配置")
	}
}
