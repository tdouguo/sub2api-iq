// 插件进程入口。保持极简：身份来自嵌入的清单，逻辑在 internal/plugin。
package main

import (
	"github.com/feeeei/sub2api-plugin-framework/sdk"

	pluginroot "github.com/tdouguo/sub2api-iq/plugin"
	"github.com/tdouguo/sub2api-iq/plugin/internal/plugin"
)

func main() {
	info := sdk.MustInfoFromManifestSource(pluginroot.ManifestSource)
	sdk.Serve(plugin.New(info))
}
