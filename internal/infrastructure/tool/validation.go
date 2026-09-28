package tool

import (
	"errors"
	"fmt"
	"strings"
	"unicode/utf8"

	domaintool "github.com/observer-mimiron/supervisor-template/internal/domain/tool"
)

func validateInput(contract domaintool.Contract, input map[string]string) error {
	if input == nil {
		return errors.New("Tool 输入不能为空")
	}
	for key, value := range input {
		if strings.TrimSpace(key) == "" || !utf8.ValidString(value) || strings.ContainsAny(value, "\x00\r\n") {
			return errors.New("Tool 输入包含非法字段")
		}
	}
	for _, required := range contract.RequiredInputs {
		if strings.TrimSpace(input[required]) == "" {
			return fmt.Errorf("Tool 输入缺少 %s", required)
		}
	}
	if contract.MaxInputBytes > 0 {
		total := 0
		for key, value := range input {
			total += len(key) + len(value)
		}
		if total > contract.MaxInputBytes {
			return errors.New("Tool 输入超过大小限制")
		}
	}
	return nil
}

func validateOutput(contract domaintool.Contract, output string) error {
	if strings.TrimSpace(output) == "" {
		return errors.New("Tool 输出为空")
	}
	if !utf8.ValidString(output) {
		return errors.New("Tool 输出不是有效 UTF-8")
	}
	for _, char := range output {
		if char == '\x00' || (char < 0x20 && char != '\n' && char != '\r' && char != '\t') {
			return errors.New("Tool 输出包含控制字符")
		}
	}
	if contract.MaxOutputBytes > 0 && len(output) > contract.MaxOutputBytes {
		return errors.New("Tool 输出超过大小限制")
	}
	return nil
}
