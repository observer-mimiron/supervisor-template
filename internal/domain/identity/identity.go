// Package identity 定义请求所属的认证主体。
//
// 本包只表达租户和主体标识，不验证凭证，也不决定资源授权。
package identity

import "strings"

// Subject 是已认证调用方在租户内的稳定标识。
type Subject struct {
	TenantID  string
	SubjectID string
}

// Valid 判断主体是否同时携带租户和主体标识。
func (s Subject) Valid() bool {
	return strings.TrimSpace(s.TenantID) != "" && strings.TrimSpace(s.SubjectID) != ""
}

// Equal 判断两个主体是否属于同一租户中的同一调用方。
func (s Subject) Equal(other Subject) bool {
	return s.TenantID == other.TenantID && s.SubjectID == other.SubjectID
}
