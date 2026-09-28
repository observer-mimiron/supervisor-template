package auth

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"testing"

	"github.com/observer-mimiron/supervisor-template/internal/application"
	"github.com/observer-mimiron/supervisor-template/internal/config"
	"github.com/observer-mimiron/supervisor-template/internal/domain/identity"
)

func TestStaticBearerAuthenticatorRequiresConfiguredToken(t *testing.T) {
	digest := sha256.Sum256([]byte("secret"))
	t.Setenv("AUTH_TEST_TOKEN", hex.EncodeToString(digest[:]))
	authenticator := NewStaticBearerAuthenticator([]config.BearerCredential{{TokenSHA256Env: "AUTH_TEST_TOKEN", TenantID: "tenant", SubjectID: "user"}})
	subject, err := authenticator.Authenticate(context.Background(), "secret")
	if err != nil || subject != (identity.Subject{TenantID: "tenant", SubjectID: "user"}) {
		t.Fatalf("authentication = %#v, %v", subject, err)
	}
	if _, err := authenticator.Authenticate(context.Background(), ""); err != application.ErrUnauthenticated {
		t.Fatalf("empty token error = %v", err)
	}
	if _, err := authenticator.Authenticate(context.Background(), "wrong"); err != application.ErrUnauthenticated {
		t.Fatalf("wrong token error = %v", err)
	}
}

func TestOwnerRunAuthorizerSeparatesResourceOwnership(t *testing.T) {
	owner := identity.Subject{TenantID: "tenant", SubjectID: "owner"}
	if err := (OwnerRunAuthorizer{}).Authorize(context.Background(), owner, owner); err != nil {
		t.Fatal(err)
	}
	if err := (OwnerRunAuthorizer{}).Authorize(context.Background(), owner, identity.Subject{TenantID: "tenant", SubjectID: "other"}); err != application.ErrAccessDenied {
		t.Fatalf("cross-subject authorization = %v", err)
	}
}
