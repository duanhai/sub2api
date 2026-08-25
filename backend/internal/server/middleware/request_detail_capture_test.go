package middleware

import (
	"bytes"
	"compress/gzip"
	"strings"
	"testing"
)

func TestDecodeCapturedBodyGzip(t *testing.T) {
	var compressed bytes.Buffer
	writer := gzip.NewWriter(&compressed)
	_, _ = writer.Write([]byte(`{"model":"gpt-test","input":"hello"}`))
	_ = writer.Close()

	decoded, truncated, err := decodeCapturedBody(compressed.Bytes(), "gzip", 256*1024)
	if err != nil {
		t.Fatalf("decodeCapturedBody() error = %v", err)
	}
	if truncated {
		t.Fatal("decodeCapturedBody() unexpectedly truncated")
	}
	if decoded != `{"model":"gpt-test","input":"hello"}` {
		t.Fatalf("decoded = %q", decoded)
	}
}

func TestDecodeCapturedBodyHonorsDecodedLimit(t *testing.T) {
	decoded, truncated, err := decodeCapturedBody([]byte(strings.Repeat("x", 20)), "identity", 10)
	if err != nil {
		t.Fatalf("decodeCapturedBody() error = %v", err)
	}
	if !truncated {
		t.Fatal("decodeCapturedBody() should report truncation")
	}
	if decoded != strings.Repeat("x", 10) {
		t.Fatalf("decoded length = %d, want 10", len(decoded))
	}
}
