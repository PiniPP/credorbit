package engine

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

func TestRotateVerifyBedrockFallsBack(t *testing.T) {
	te := newTestEnv(t)
	var hits atomic.Int32
	br := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		w.Header().Set("Content-Type", "application/json")
		if strings.Contains(r.Header.Get("Authorization"), "ASIA111100000001") {
			w.Header().Set("X-Amzn-ErrorType", "AccessDeniedException")
			w.WriteHeader(403)
			_, _ = io.WriteString(w, `{"message":"denied"}`)
			return
		}
		_, _ = io.WriteString(w, `{"output":{"message":{"role":"assistant","content":[{"text":"p"}]}},"stopReason":"max_tokens","usage":{"inputTokens":1,"outputTokens":1,"totalTokens":2},"metrics":{"latencyMs":5}}`)
	}))
	defer br.Close()
	o := te.options(nil)
	o.BedrockEndpoint = br.URL
	e, err := New(o)
	if err != nil {
		t.Fatal(err)
	}
	te.mustSet(e, "export.enabled", "true", "rotation.bedrock_region", "eu-west-1", "rotation.bedrock_model", "test-model", "rotation.verify_bedrock", "true")
	if err := e.SetRotationAccounts([]string{"team-dev", "team-prd"}); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if _, err := e.Export(ctx, "alpha", Scope{Kind: ScopeSelected, Profiles: []string{"team-dev", "team-prd"}}, ExportOptions{}); err != nil {
		t.Fatal(err)
	}
	for range 3 {
		_, to, err := e.Rotate(ctx, RotateOptions{Strategy: "round-robin"})
		if err != nil || to != "team-prd" {
			t.Fatalf("to=%q err=%v", to, err)
		}
	}
	if _, _, err := e.Rotate(ctx, RotateOptions{Set: "team-dev"}); err == nil {
		t.Error("set to an account without Bedrock access succeeded")
	}
	if err := e.SetRotationAccounts([]string{"team-dev"}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := e.Rotate(ctx, RotateOptions{}); err == nil || !strings.Contains(err.Error(), "team-dev") {
		t.Errorf("all verification failures err = %v", err)
	}
	if hits.Load() == 0 {
		t.Error("bedrock not called")
	}
}
