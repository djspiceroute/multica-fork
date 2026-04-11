package handler

import (
	"net/http/httptest"
	"os"
	"testing"
)

func TestIsTrustedCLISyncRequest_NoSecret(t *testing.T) {
	t.Setenv("MULTICA_CLI_SYNC_SECRET", "")
	req := httptest.NewRequest("PATCH", "/api/issues/x", nil)
	req.Header.Set(cliSyncHeader, "1")

	if !isTrustedCLISyncRequest(req) {
		t.Fatal("expected trusted CLI sync request when header is set and no secret configured")
	}
}

func TestIsTrustedCLISyncRequest_WithSecret(t *testing.T) {
	t.Setenv("MULTICA_CLI_SYNC_SECRET", "test-secret")

	req := httptest.NewRequest("PATCH", "/api/issues/x", nil)
	req.Header.Set(cliSyncHeader, "1")
	req.Header.Set(cliSyncSecretHeader, "wrong")
	if isTrustedCLISyncRequest(req) {
		t.Fatal("expected untrusted request when secret header is wrong")
	}

	req2 := httptest.NewRequest("PATCH", "/api/issues/x", nil)
	req2.Header.Set(cliSyncHeader, "1")
	req2.Header.Set(cliSyncSecretHeader, "test-secret")
	if !isTrustedCLISyncRequest(req2) {
		t.Fatal("expected trusted request when secret header matches")
	}
}

func TestIsTrustedCLISyncRequest_MissingHeader(t *testing.T) {
	_ = os.Unsetenv("MULTICA_CLI_SYNC_SECRET")
	req := httptest.NewRequest("PATCH", "/api/issues/x", nil)
	if isTrustedCLISyncRequest(req) {
		t.Fatal("expected untrusted request when CLI sync header is missing")
	}
}
