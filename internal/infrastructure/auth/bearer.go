// Package auth 提供凭证解析和 run 所有者授权适配器。
//
// 本包只把已配置凭证映射为主体并比较主体所有权，不决定 Agent Tool 权限或审批。
package auth

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"os"
	"strings"

	"github.com/observer-mimiron/supervisor-template/internal/application"
	"github.com/observer-mimiron/supervisor-template/internal/config"
	"github.com/observer-mimiron/supervisor-template/internal/domain/identity"
)

type bearerCredential struct {
	tokenSHA256Env string
	subject        identity.Subject
}

// StaticBearerAuthenticator 使用环境中的 token SHA-256 哈希认证固定主体。
type StaticBearerAuthenticator struct {
	credentials []bearerCredential
}

// NewStaticBearerAuthenticator 创建不持有原始 token 的静态 Bearer 认证器。
func NewStaticBearerAuthenticator(credentials []config.BearerCredential) *StaticBearerAuthenticator {
	configured := make([]bearerCredential, 0, len(credentials))
	for _, credential := range credentials {
		configured = append(configured, bearerCredential{
			tokenSHA256Env: credential.TokenSHA256Env,
			subject:        identity.Subject{TenantID: credential.TenantID, SubjectID: credential.SubjectID},
		})
	}
	return &StaticBearerAuthenticator{credentials: configured}
}

// Authenticate 将 Bearer token 与环境中配置的 SHA-256 哈希作常量时间比较。
func (a *StaticBearerAuthenticator) Authenticate(_ context.Context, token string) (identity.Subject, error) {
	if a == nil || strings.TrimSpace(token) == "" {
		return identity.Subject{}, application.ErrUnauthenticated
	}
	digest := sha256.Sum256([]byte(token))
	for _, credential := range a.credentials {
		expected, ok := tokenDigest(credential.tokenSHA256Env)
		if ok && subtle.ConstantTimeCompare(digest[:], expected) == 1 {
			return credential.subject, nil
		}
	}
	return identity.Subject{}, application.ErrUnauthenticated
}

// tokenDigest 读取合法的 32 字节 token 哈希；错误配置与未知凭证同样拒绝。
func tokenDigest(env string) ([]byte, bool) {
	digest, err := hex.DecodeString(strings.TrimSpace(os.Getenv(env)))
	if err != nil || len(digest) != sha256.Size {
		return nil, false
	}
	return digest, true
}

// OwnerRunAuthorizer 只允许 run 的创建主体访问该 run。
type OwnerRunAuthorizer struct{}

// Authorize 比较租户和主体，拒绝跨主体访问同一个 run。
func (OwnerRunAuthorizer) Authorize(_ context.Context, subject, owner identity.Subject) error {
	if subject.Valid() && subject.Equal(owner) {
		return nil
	}
	return application.ErrAccessDenied
}
