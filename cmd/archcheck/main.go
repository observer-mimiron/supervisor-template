// Command archcheck enforces the repository's small set of inward package rules.
package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
)

type packageInfo struct {
	ImportPath string
	Imports    []string
}

func main() {
	command := exec.Command("go", "list", "-json", "./internal/...", "./cmd/...")
	output, err := command.Output()
	if err != nil {
		fmt.Fprintf(os.Stderr, "archcheck: go list failed: %v\n", err)
		os.Exit(2)
	}
	var violations []string
	decoder := json.NewDecoder(bytes.NewReader(output))
	for {
		var pkg packageInfo
		err := decoder.Decode(&pkg)
		if err == io.EOF {
			break
		}
		if err != nil {
			fmt.Fprintf(os.Stderr, "archcheck: invalid go list output: %v\n", err)
			os.Exit(2)
		}
		for _, imported := range pkg.Imports {
			if forbidden(pkg.ImportPath, imported) {
				violations = append(violations, pkg.ImportPath+" -> "+imported)
			}
			if nonStdlibDomainImport(pkg.ImportPath, imported) {
				violations = append(violations, pkg.ImportPath+" -> "+imported+" (领域包只能依赖标准库与 internal/domain)")
			}
		}
	}
	if len(violations) > 0 {
		for _, violation := range violations {
			fmt.Fprintln(os.Stderr, "archcheck:", violation)
		}
		os.Exit(1)
	}
	fmt.Println("archcheck: package dependency direction OK")

	root, err := os.Getwd()
	if err != nil {
		fmt.Fprintf(os.Stderr, "archcheck: 无法确定工作目录: %v\n", err)
		os.Exit(2)
	}
	configViolations := checkConfigFieldsAreConsumed(root)
	if len(configViolations) > 0 {
		for _, violation := range configViolations {
			fmt.Fprintln(os.Stderr, "archcheck:", violation)
		}
		os.Exit(1)
	}
	fmt.Println("archcheck: every toml configuration field has a consumer")
}

func forbidden(pkg, imported string) bool {
	const module = "github.com/observer-mimiron/supervisor-template/"
	if strings.HasPrefix(pkg, module+"cmd/") {
		return strings.HasPrefix(imported, module+"internal/application")
	}
	if !strings.HasPrefix(pkg, module+"internal/") {
		return false
	}
	if strings.HasPrefix(pkg, module+"internal/domain/") {
		return strings.HasPrefix(imported, module+"internal/application/") ||
			strings.HasPrefix(imported, module+"internal/interfaces/") ||
			strings.HasPrefix(imported, module+"internal/infrastructure/")
	}
	if strings.HasPrefix(pkg, module+"internal/application/") || pkg == module+"internal/application" {
		return strings.HasPrefix(imported, module+"internal/interfaces/") ||
			strings.HasPrefix(imported, module+"internal/infrastructure/")
	}
	if strings.HasPrefix(pkg, module+"internal/interfaces/") {
		// HTTP tracing uses the observability contract as a transport concern.
		return strings.HasPrefix(imported, module+"internal/infrastructure/") && !strings.HasPrefix(imported, module+"internal/infrastructure/observability")
	}
	if strings.HasPrefix(pkg, module+"internal/infrastructure/") {
		return imported == module+"internal/config" || strings.HasPrefix(imported, module+"internal/config/")
	}
	return false
}

// nonStdlibDomainImport 判断领域包是否引入了标准库与 internal/domain 之外的依赖。
//
// 宪法 I 要求领域代码不依赖 HTTP 框架、Agent 框架、数据库、MCP、具体模型或传输协议。
// 上面 forbidden 只比较**模块内**路径前缀，对模块外的 import 一律返回 false，因此领域包
// 引入任何第三方库（例如 eino、gorm）都会静默通过——两条规则不能互相代替。
//
// 标准库的判定用 Go 自身的惯例：import path 的第一段不含 "." 即标准库
// （`crypto/sha256`、`errors`、`time`）；第三方必然是域名形式（`github.com/...`）。
// 领域包之间互相依赖是允许的。
func nonStdlibDomainImport(pkg, imported string) bool {
	const module = "github.com/observer-mimiron/supervisor-template/"
	if !strings.HasPrefix(pkg, module+"internal/domain/") {
		return false
	}
	if strings.HasPrefix(imported, module+"internal/domain/") {
		return false
	}
	first, _, _ := strings.Cut(imported, "/")
	return strings.Contains(first, ".")
}
