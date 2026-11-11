package releases

import (
	"fmt"
	"path/filepath"
	"sort"
	"strings"
)

type downloadArtifact struct {
	Download
	Format, Action, SizeLabel string
}

type downloadArchitecture struct {
	Arch, Label, Description string
	Primary                  *downloadArtifact
	Alternatives             []downloadArtifact
}

type downloadPlatform struct {
	ID, Title, Edition, Description, Requirement string
	Architectures                                []downloadArchitecture
}

type downloadPageData struct {
	Platforms []downloadPlatform
	Count     int
}

// Presentation only receives the verified catalog and never constructs release
// URLs or substitutes a different architecture for an unavailable artifact.
func buildDownloadPage(catalog Catalog) downloadPageData {
	page := downloadPageData{Platforms: []downloadPlatform{
		{
			ID: "windows", Title: "Windows 10 / 11", Edition: "标准桌面版",
			Description:   "为日常办公准备。聊天、文件上传与后台传输，在同一个工作区接续。",
			Requirement:   "适用于 Windows 10/11 · 64 位，已包含 WebView2 离线安装包。",
			Architectures: []downloadArchitecture{{Arch: "x86_64", Label: "64 位 · x64", Description: "适用于 Intel / AMD 64 位处理器"}},
		},
		{
			ID: "windows7", Title: "Windows 7 SP1", Edition: "兼容桌面版",
			Description: "为仍在使用旧系统的工作设备，保留熟悉的协作入口。",
			Requirement: "兼容版自带运行环境，无需 WebView2。请先确认已安装 Service Pack 1。",
			Architectures: []downloadArchitecture{
				{Arch: "x86_64", Label: "64 位 · x64", Description: "Windows 7 SP1 · 64 位"},
				{Arch: "x86", Label: "32 位 · x86", Description: "Windows 7 SP1 · 32 位"},
			},
		},
		{
			ID: "macos", Title: "macOS", Edition: "Mac 桌面版",
			Description: "从 Apple 芯片到 Intel，选择适合这台 Mac 的版本。",
			Requirement: "适用于 macOS 11 或更新版本，使用系统自带的 WebKit 运行环境。",
			Architectures: []downloadArchitecture{
				{Arch: "aarch64", Label: "Apple 芯片 · ARM64", Description: "适用于 M 系列芯片的 Mac"},
				{Arch: "x86_64", Label: "Intel 芯片 · x64", Description: "适用于 Intel 处理器的 Mac"},
			},
		},
	}}
	for pi := range page.Platforms {
		platform := &page.Platforms[pi]
		for ai := range platform.Architectures {
			arch := &platform.Architectures[ai]
			var artifacts []downloadArtifact
			for _, d := range catalog.Releases {
				if d.Platform == platform.ID && d.Arch == arch.Arch {
					format, action := downloadFormat(d.Name)
					if platform.ID == "windows7" && strings.EqualFold(filepath.Ext(d.Name), ".zip") {
						format, action = "ZIP 便携版", "下载 ZIP 便携版"
					}
					artifacts = append(artifacts, downloadArtifact{Download: d, Format: format, Action: action, SizeLabel: downloadSize(d.Size)})
				}
			}
			sort.SliceStable(artifacts, func(i, j int) bool {
				if left, right := downloadFormatOrder(artifacts[i].Name), downloadFormatOrder(artifacts[j].Name); left != right {
					return left < right
				}
				return compareDownloadVersion(artifacts[i].Version, artifacts[j].Version) > 0
			})
			if len(artifacts) > 0 {
				arch.Primary = &artifacts[0]
				arch.Alternatives = artifacts[1:]
				page.Count += len(artifacts)
			}
		}
	}
	return page
}

func downloadFormat(name string) (string, string) {
	switch strings.ToLower(filepath.Ext(name)) {
	case ".exe":
		return "EXE 安装包", "下载安装包"
	case ".msi":
		return "MSI 安装包", "下载 MSI 安装包"
	case ".dmg":
		return "DMG 安装包", "下载 DMG 安装包"
	case ".pkg":
		return "PKG 安装包", "下载 PKG 安装包"
	case ".zip":
		return "ZIP 压缩包", "下载 ZIP 压缩包"
	default:
		return "客户端文件", "下载客户端文件"
	}
}

func downloadFormatOrder(name string) int {
	switch strings.ToLower(filepath.Ext(name)) {
	case ".exe", ".dmg":
		return 0
	case ".msi", ".pkg":
		return 1
	case ".zip":
		return 2
	default:
		return 3
	}
}

func downloadSize(size int64) string {
	if size < 1024 {
		return fmt.Sprintf("%d B", size)
	}
	value := float64(size)
	for _, unit := range []string{"KB", "MB", "GB"} {
		value /= 1024
		if value < 1024 || unit == "GB" {
			return fmt.Sprintf("%.1f %s", value, unit)
		}
	}
	return ""
}

// SemVer precedence keeps 0.10 ahead of 0.9 and stable releases ahead of their
// prereleases. Build metadata does not affect precedence. readCatalog validates
// all versions before they reach this presentation helper.
func compareDownloadVersion(a, b string) int {
	ac, _, _ := strings.Cut(a, "+")
	bc, _, _ := strings.Cut(b, "+")
	ac, apre, ahas := strings.Cut(ac, "-")
	bc, bpre, bhas := strings.Cut(bc, "-")
	aa, ba := strings.Split(ac, "."), strings.Split(bc, ".")
	for i := range aa {
		if c := compareNumericIdentifier(aa[i], ba[i]); c != 0 {
			return c
		}
	}
	if !ahas && bhas {
		return 1
	}
	if ahas && !bhas {
		return -1
	}
	if !ahas {
		return 0
	}
	aa, ba = strings.Split(apre, "."), strings.Split(bpre, ".")
	for i := 0; i < len(aa) && i < len(ba); i++ {
		an, bn := numericIdentifier(aa[i]), numericIdentifier(ba[i])
		if an && !bn {
			return -1
		}
		if !an && bn {
			return 1
		}
		c := strings.Compare(aa[i], ba[i])
		if an && bn {
			c = compareNumericIdentifier(aa[i], ba[i])
		}
		if c != 0 {
			return c
		}
	}
	if len(aa) < len(ba) {
		return -1
	}
	if len(aa) > len(ba) {
		return 1
	}
	return 0
}

func compareNumericIdentifier(a, b string) int {
	if len(a) < len(b) {
		return -1
	}
	if len(a) > len(b) {
		return 1
	}
	return strings.Compare(a, b)
}

func numericIdentifier(value string) bool {
	return strings.Trim(value, "0123456789") == ""
}
