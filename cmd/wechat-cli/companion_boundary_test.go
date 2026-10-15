package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCompanionErrorsRedactDiagnostics(t *testing.T) {
	companionUseTestCPU(t, func(context.Context, companionPrompt, map[string]any, companionAskRequest, companionStreamEmitter) (companionCPUResult, error) {
		return companionCPUResult{}, fmt.Errorf("CPU failed: token=fixture-secret password=fixture-password wxid_fixture_account")
	})
	handler, token := companionTestHandler(false)
	for _, path := range []string{"/api/ask", "/api/ask-stream"} {
		req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(`{"question":"hello"}`))
		companionAuthorizeTestRequest(req, token)
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		body := rec.Body.String()
		if !strings.Contains(body, "cpu_error") || !strings.Contains(body, "[redacted]") {
			t.Fatalf("missing sanitized CPU error: %s", body)
		}
		for _, secret := range []string{"fixture-secret", "fixture-password", "wxid_fixture_account"} {
			if strings.Contains(body, secret) {
				t.Fatalf("%s exposed diagnostic secret", path)
			}
		}
	}
}

func TestCompanionStreamingLogsRedactBeforeTruncating(t *testing.T) {
	input := "error: Authorization: Bearer synthetic-secret\nfailed api_key=fixture-key\n" +
		"error: token=\"" + strings.Repeat("s", companionCPULogTextMax+20) + "\"\n"
	var events []string
	companionScanCPUOutput(strings.NewReader(input), func(event string, data map[string]any) {
		if event != "cpu_log" {
			t.Fatalf("unexpected event %q", event)
		}
		events = append(events, data["text"].(string))
	}, nil)
	if len(events) != 3 {
		t.Fatalf("log events = %#v", events)
	}
	for _, line := range events {
		if !strings.Contains(line, "[redacted]") || strings.Contains(line, "synthetic-secret") || strings.Contains(line, "fixture-key") || strings.Contains(line, "ssss") {
			t.Fatalf("unsafe streaming log: %s", line)
		}
	}
}

func TestCompanionStrictReadOnlyBlocksUploadsAndCPU(t *testing.T) {
	t.Setenv("WECHAT_CLI_STRICT_READ_ONLY", "1")
	stateDir := filepath.Join(t.TempDir(), "state")
	t.Setenv("WECHAT_CLI_STATE_DIR", stateDir)
	companionUseTestCPU(t, func(context.Context, companionPrompt, map[string]any, companionAskRequest, companionStreamEmitter) (companionCPUResult, error) {
		t.Error("strict read-only invoked CPU")
		return companionCPUResult{Answer: "unexpected"}, nil
	})
	handler, token := companionTestHandler(false)
	for _, path := range []string{"/api/ask", "/api/ask-stream", "/api/upload"} {
		t.Run(path, func(t *testing.T) {
			var body bytes.Buffer
			contentType := "application/json"
			if path == "/api/upload" {
				writer := multipart.NewWriter(&body)
				part, err := writer.CreateFormFile("files", "private.txt")
				if err != nil {
					t.Fatal(err)
				}
				_, _ = io.WriteString(part, "fixture-only")
				if err := writer.Close(); err != nil {
					t.Fatal(err)
				}
				contentType = writer.FormDataContentType()
			} else {
				body.WriteString(`{"question":"fixture-only"}`)
			}
			req := httptest.NewRequest(http.MethodPost, path, &body)
			companionAuthorizeTestRequest(req, token)
			req.Header.Set("Content-Type", contentType)
			rec := httptest.NewRecorder()
			handler.ServeHTTP(rec, req)
			if rec.Code != http.StatusForbidden || !strings.Contains(rec.Body.String(), "strict_read_only") {
				t.Fatalf("strict endpoint status=%d body=%s", rec.Code, rec.Body.String())
			}
		})
	}
	if _, err := os.Stat(stateDir); !os.IsNotExist(err) {
		t.Fatalf("strict requests created state: %v", err)
	}
}

func TestReadCompanionJSONRequiresOneBoundedObject(t *testing.T) {
	const max = 2 * 1024 * 1024
	for _, tc := range []struct {
		name  string
		body  string
		valid bool
	}{
		{"object", `{"question":"hello"}`, true},
		{"whitespace", " \n{}\t ", true},
		{"at_limit", "{}" + strings.Repeat(" ", max-2), true},
		{"empty", "", false},
		{"null", "null", false},
		{"array", "[]", false},
		{"unknown_field", `{"unknown":1}`, false},
		{"second_object", "{} {}", false},
		{"trailing_data", "{} junk", false},
		{"oversized_padding", "{}" + strings.Repeat(" ", max-1), false},
		{"oversized_string", `{"question":"` + strings.Repeat("x", max) + `"}`, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPost, "/api/ask", strings.NewReader(tc.body))
			var dst companionAskRequest
			if err := readCompanionJSON(req, &dst); (err == nil) != tc.valid {
				t.Fatalf("valid=%v error=%v", tc.valid, err)
			}
		})
	}
}

func TestCompanionCPUOutputContinuesAfterOversizedLine(t *testing.T) {
	input := "error token=" + strings.Repeat("s", 2<<20) + "\nerror: later diagnostic\n"
	var got []string
	companionScanCPUOutput(strings.NewReader(input), nil, func(line string) { got = append(got, line) })
	if len(got) != 2 || !strings.Contains(got[0], "omitted") || got[1] != "error: later diagnostic" {
		t.Fatalf("oversized output stopped the drain: lines=%d", len(got))
	}
	if strings.Contains(got[0], "ssss") {
		t.Fatal("omitted output exposed payload")
	}
}

func TestCompanionCPUStreamBoundsLinesAndRetainsPartialTail(t *testing.T) {
	path := filepath.Join(t.TempDir(), "stream.jsonl")
	oversized := strings.Repeat("x", 2<<20) + "\n"
	partial := `{"type":"item.completed","item":{"type":"agent_message","text":"tail answer"}}`
	if err := os.WriteFile(path, []byte(oversized+partial[:20]), 0o600); err != nil {
		t.Fatal(err)
	}
	state := newCompanionCPUStreamState()
	var events []string
	emit := func(event string, data map[string]any) { events = append(events, event+":"+fmt.Sprint(data)) }
	companionReadCPUStreamFile(path, state, emit, false)
	if state.offsets[path] != int64(len(oversized)) {
		t.Fatalf("partial tail consumed: %d", state.offsets[path])
	}
	if len(events) != 1 || !strings.Contains(events[0], "omitted") {
		t.Fatalf("missing bounded-line warning: %#v", events)
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	_, err = io.WriteString(f, partial[20:]+"\n")
	f.Close()
	if err != nil {
		t.Fatal(err)
	}
	companionReadCPUStreamFile(path, state, emit, false)
	if len(events) != 2 || !strings.Contains(events[1], "tail answer") {
		t.Fatalf("tail not resumed exactly once: %#v", events)
	}
}

func TestCompanionParsesNestedErrorEnvelope(t *testing.T) {
	tool, _, code, err, ok := companionParseToolEnvelope(`{"ok":false,"error":{"code":"chat_not_found","message":"missing chat token=fixture-secret","tool":"chat_timeline","command":"timeline"}}`)
	if !ok || tool != "chat_timeline" || code != "chat_not_found" || err == nil || !strings.Contains(err.Error(), "missing chat") {
		t.Fatalf("lost nested error: tool=%s code=%s err=%v ok=%v", tool, code, err, ok)
	}
	trace := companionBuildToolTrace("x", tool, "timeline", nil, nil, code, err, 0)
	if strings.Contains(trace.Error+trace.Summary, "fixture-secret") || !strings.Contains(trace.Error, "[redacted]") {
		t.Fatalf("unsafe trace: %#v", trace)
	}
}

func TestCompanionEnvelopePreservesLargeMessageIDs(t *testing.T) {
	const id = "7710666891970547832"
	_, data, _, _, ok := companionParseToolEnvelope(`{"ok":true,"data":{"local_id":` + id + `,"amount":1.25}}`)
	if !ok {
		t.Fatal("envelope rejected")
	}
	encoded, err := json.Marshal(data)
	if err != nil || !strings.Contains(string(encoded), id) || !strings.Contains(string(encoded), "1.25") {
		t.Fatalf("numeric payload changed: %s %v", encoded, err)
	}
	rows := companionParseJSONLineMessages(`{"message":{"local_id":` + id + `,"text":"fixture"}}`)
	encoded, _ = json.Marshal(rows)
	if !strings.Contains(string(encoded), id) {
		t.Fatalf("JSONL id rounded: %s", encoded)
	}
}

func TestCompanionToolDiagnosticsRedactSecrets(t *testing.T) {
	input := map[string]any{"command": "curl -H 'Authorization: Bearer fixture-command-secret' https://example.invalid", "api_key": "fixture-arg-secret", "nested": map[string]any{"password": "fixture-nested-secret"}}
	info := companionCPUCommandInfoFromNameInput("exec_command", input)
	trace := companionBuildCPUCommandTrace("x", info, "error", "request failed token=fixture-result-secret", 1)
	encoded, _ := json.Marshal(trace)
	for _, secret := range []string{"fixture-command-secret", "fixture-arg-secret", "fixture-nested-secret", "fixture-result-secret"} {
		if strings.Contains(string(encoded), secret) {
			t.Fatalf("diagnostic leaked %s: %s", secret, encoded)
		}
	}
	if !strings.Contains(string(encoded), "redacted") {
		t.Fatalf("missing redactions: %s", encoded)
	}
}

func TestCompanionFailedUploadRollsBackPrivateCopies(t *testing.T) {
	stateDir := t.TempDir()
	t.Setenv("WECHAT_CLI_STATE_DIR", stateDir)
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	part, err := writer.CreateFormFile("files", "private.txt")
	if err != nil {
		t.Fatal(err)
	}
	_, _ = io.WriteString(part, "private fixture")
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	form, err := multipart.NewReader(&body, writer.Boundary()).ReadForm(1024)
	if err != nil {
		t.Fatal(err)
	}
	defer form.RemoveAll()
	files := append(form.File["files"], &multipart.FileHeader{Filename: "missing.txt"})
	attachments, err := companionSaveUploadBatch(files)
	if err == nil || len(attachments) != 0 {
		t.Fatalf("failed batch = %#v, %v", attachments, err)
	}
	_ = filepath.WalkDir(stateDir, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			t.Fatal(err)
		}
		if !entry.IsDir() {
			t.Errorf("failed upload left private data: %s", path)
		}
		return nil
	})
}

func TestCompanionTrustedAttachmentRebuildsURL(t *testing.T) {
	stateDir := t.TempDir()
	t.Setenv("WECHAT_CLI_STATE_DIR", stateDir)
	path := filepath.Join(stateDir, "companion-uploads", "20261015", "fixture.txt")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("fixture"), 0o600); err != nil {
		t.Fatal(err)
	}
	got := companionTrustedAttachments([]companionAttachment{{Path: path, URL: "https://example.invalid/forged"}})
	if len(got) != 1 || got[0].URL != "/api/attachment/20261015/fixture.txt" {
		t.Fatalf("trusted path kept forged URL: %#v", got)
	}
}
