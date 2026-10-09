// Package pluginroot 把 manifest.source.json 编进二进制，
// 使 GetInfo 与打包器读取同一份身份声明，避免二者漂移导致宿主拒绝启动。
package pluginroot

import _ "embed"

// ManifestSource 是 manifest.source.json 的原始内容。
//
//go:embed manifest.source.json
var ManifestSource []byte
