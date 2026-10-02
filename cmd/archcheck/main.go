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
		}
	}
	if len(violations) > 0 {
		for _, violation := range violations {
			fmt.Fprintln(os.Stderr, "archcheck:", violation)
		}
		os.Exit(1)
	}
	fmt.Println("archcheck: package dependency direction OK")
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
