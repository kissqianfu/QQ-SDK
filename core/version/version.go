// Package version 提供本 SDK 的模块版本与统一 UA：版本取自构建元数据（即依赖里钉的 tag），不落文件。
package version

import (
	"runtime/debug"
	"strings"
)

// modulePath 是本 SDK 的模块路径，用于在构建元数据里定位自身。
const modulePath = "github.com/kissqianfu/QQ-SDK"

// Version 返回模块版本：值来自构建元数据，即使用方 go.mod 里钉的 tag；仓库内直跑为 devel。
func Version() string {
	bi, ok := debug.ReadBuildInfo()
	if !ok {
		return "devel"
	}
	if bi.Main.Path == modulePath {
		return normalize(bi.Main.Version)
	}
	for _, d := range bi.Deps {
		if d.Path == modulePath {
			return normalize(d.Version)
		}
	}
	return "devel"
}

// UserAgent 是请求头统一 UA：平台据此识别接入方与版本。
func UserAgent() string { return "qqbot-sdk/" + Version() }

// normalize 去掉 tag 的 v 前缀，空值或 (devel) 统一成 devel（不把占位符发给平台）。
func normalize(v string) string {
	v = strings.TrimPrefix(v, "v")
	if v == "" || v == "(devel)" {
		return "devel"
	}
	return v
}
