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
	command := exec.Command("go", "list", "-json", "./internal/...")
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
	if !strings.HasPrefix(pkg, "github.com/observer-mimiron/supervisor-template/internal/") {
		return false
	}
	if strings.HasPrefix(pkg, "github.com/observer-mimiron/supervisor-template/internal/domain/") {
		return strings.HasPrefix(imported, "github.com/observer-mimiron/supervisor-template/internal/application/") ||
			strings.HasPrefix(imported, "github.com/observer-mimiron/supervisor-template/internal/interfaces/") ||
			strings.HasPrefix(imported, "github.com/observer-mimiron/supervisor-template/internal/infrastructure/")
	}
	if strings.HasPrefix(pkg, "github.com/observer-mimiron/supervisor-template/internal/application/") || pkg == "github.com/observer-mimiron/supervisor-template/internal/application" {
		return strings.HasPrefix(imported, "github.com/observer-mimiron/supervisor-template/internal/interfaces/") ||
			strings.HasPrefix(imported, "github.com/observer-mimiron/supervisor-template/internal/infrastructure/")
	}
	if strings.HasPrefix(pkg, "github.com/observer-mimiron/supervisor-template/internal/interfaces/") {
		// HTTP tracing uses the observability contract as a transport concern.
		return strings.HasPrefix(imported, "github.com/observer-mimiron/supervisor-template/internal/infrastructure/") && !strings.HasPrefix(imported, "github.com/observer-mimiron/supervisor-template/internal/infrastructure/observability")
	}
	return false
}
