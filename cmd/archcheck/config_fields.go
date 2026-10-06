package main

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
)

// configField 是一个带 TOML 键的配置字段。
type configField struct {
	Struct string
	Name   string
	Key    string
}

// checkConfigFieldsAreConsumed 校验每个 TOML 配置字段都在 config 包之外有消费方。
//
// 动机：配置面就是能力声明的一部分。一个被解析、被校验、却无人读取的字段会让使用者
// 以为某项能力已经生效。这里把"配置必须被消费"变成可执行的检查，而不是靠评审记住。
//
// 已知局限：检查按字段名匹配选择器，因此当另一个结构体存在同名字段时会漏报
// （例如 ExecutionPlan.MaxSteps 会掩盖 Supervisor.MaxSteps）。它抓不住全部情况，
// 但能挡住"新增配置却忘了接线"这一类最常见的退化。改动配置时仍需人工确认消费方。
func checkConfigFieldsAreConsumed(root string) []string {
	fields, err := configFields(filepath.Join(root, "internal", "config"))
	if err != nil {
		return []string{"读取配置字段失败: " + err.Error()}
	}
	selectors, err := selectorNames(root)
	if err != nil {
		return []string{"扫描消费方失败: " + err.Error()}
	}
	var violations []string
	for _, field := range fields {
		if _, ok := selectors[field.Name]; ok {
			continue
		}
		violations = append(violations, fmt.Sprintf(
			"config.%s.%s (toml %q) 没有任何消费方：要么接上实现，要么删掉该配置项",
			field.Struct, field.Name, field.Key))
	}
	return violations
}

// configFields 从 config 包源码中解析出所有 TOML 配置旋钮。
//
// 这里必须处理本仓库的 duration 写法：TOML 键挂在 `XText` 字符串上，真正的运行期
// 值落在 `X time.Duration`（tag 为 "-"）。因此旋钮是 `X` 而不是 `XText`——若只检查
// 带 tag 的字段，整个 duration 配置类都会被漏掉。
func configFields(dir string) ([]configField, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	var fields []configField
	fset := token.NewFileSet()
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".go") || strings.HasSuffix(entry.Name(), "_test.go") {
			continue
		}
		file, parseErr := parser.ParseFile(fset, filepath.Join(dir, entry.Name()), nil, 0)
		if parseErr != nil {
			return nil, parseErr
		}
		ast.Inspect(file, func(node ast.Node) bool {
			declaration, ok := node.(*ast.GenDecl)
			if !ok || declaration.Tok != token.TYPE {
				return true
			}
			for _, spec := range declaration.Specs {
				typeSpec, ok := spec.(*ast.TypeSpec)
				if !ok {
					continue
				}
				structType, ok := typeSpec.Type.(*ast.StructType)
				if !ok {
					continue
				}
				fields = append(fields, structFields(typeSpec.Name.Name, structType)...)
			}
			return true
		})
	}
	return fields, nil
}

// structFields 返回一个结构体里真正的配置旋钮。
func structFields(structName string, structType *ast.StructType) []configField {
	// present 必须包含所有字段（含 tag 为 "-" 的运行期值字段），否则 Text 兄弟字段
	// 找不到对应的旋钮，整个 duration 配置类都会退化成误报。
	present := make(map[string]struct{})
	keys := make(map[string]string)
	for _, item := range structType.Fields.List {
		for _, name := range item.Names {
			present[name.Name] = struct{}{}
		}
		key, ok := tomlKey(item)
		if !ok {
			continue
		}
		for _, name := range item.Names {
			keys[name.Name] = key
		}
	}
	var fields []configField
	for name, key := range keys {
		target := name
		if strings.HasSuffix(name, "Text") {
			value := strings.TrimSuffix(name, "Text")
			if _, ok := present[value]; !ok {
				// 没有对应的运行期值字段，说明它本身就是旋钮。
				fields = append(fields, configField{Struct: structName, Name: name, Key: key})
				continue
			}
			target = value
		}
		fields = append(fields, configField{Struct: structName, Name: target, Key: key})
	}
	return fields
}

// tomlKey 返回字段的 TOML 键；没有 tag 或 tag 为 "-" 时返回 false。
func tomlKey(field *ast.Field) (string, bool) {
	if field.Tag == nil {
		return "", false
	}
	tag := strings.Trim(field.Tag.Value, "`")
	const marker = `toml:"`
	index := strings.Index(tag, marker)
	if index < 0 {
		return "", false
	}
	value := tag[index+len(marker):]
	if end := strings.Index(value, `"`); end >= 0 {
		value = value[:end]
	}
	value = strings.Split(value, ",")[0]
	if value == "" || value == "-" {
		return "", false
	}
	return value, true
}

// selectorNames 收集仓库内（config 包之外）所有非测试文件里出现的选择器字段名。
func selectorNames(root string) (map[string]struct{}, error) {
	names := make(map[string]struct{})
	fset := token.NewFileSet()
	for _, base := range []string{"cmd", "internal", "eval"} {
		walkErr := filepath.WalkDir(filepath.Join(root, base), func(path string, entry os.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if entry.IsDir() {
				name := entry.Name()
				if name == "testdata" || name == "var" || strings.HasPrefix(name, ".") {
					return filepath.SkipDir
				}
				return nil
			}
			if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
				return nil
			}
			if strings.HasPrefix(filepath.ToSlash(path), filepath.ToSlash(filepath.Join(root, "internal", "config"))) {
				return nil
			}
			file, parseErr := parser.ParseFile(fset, path, nil, 0)
			if parseErr != nil {
				return parseErr
			}
			ast.Inspect(file, func(node ast.Node) bool {
				selector, ok := node.(*ast.SelectorExpr)
				if !ok {
					return true
				}
				names[selector.Sel.Name] = struct{}{}
				return true
			})
			return nil
		})
		if walkErr != nil {
			return nil, walkErr
		}
	}
	return names, nil
}
