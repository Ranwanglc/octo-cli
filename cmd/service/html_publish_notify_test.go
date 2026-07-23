package service

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
)

func TestHTMLPublishAndNotify_SendsDeterministicCard(t *testing.T) {
	var publishBody map[string]any
	var sendBody map[string]any
	root, _, _ := rootWithService(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/v1/docs":
			_ = json.NewDecoder(r.Body).Decode(&publishBody)
			_, _ = w.Write([]byte(`{"data":{"slug":"report_1","version":3,"doc_id":"doc-123","share_url":"https://docs.example.test/d/doc-123","registered":true,"status":"published"}}`))
		case "/v1/bot/sendMessage":
			_ = json.NewDecoder(r.Body).Decode(&sendBody)
			_, _ = w.Write([]byte(`{"message_id":42,"message_seq":7}`))
		default:
			http.NotFound(w, r)
		}
	})

	root.SetArgs([]string{
		"html", "publish-and-notify",
		"--slug", "report_1",
		"--html", "<html><body>done</body></html>",
		"--title", "Quarterly report",
		"--mount-type", "group",
		"--group-no", "group-9",
		"--request-id", "req-123",
		"--channel-id", "user-7",
		"--channel-type", "1",
	})
	if err := root.Execute(); err != nil {
		t.Fatalf("execute: %v", err)
	}

	if publishBody["slug"] != "report_1" || publishBody["mount_type"] != "group" || publishBody["group_no"] != "group-9" {
		t.Fatalf("publish body = %#v", publishBody)
	}
	meta, _ := publishBody["meta"].(map[string]any)
	if meta["title"] != "Quarterly report" {
		t.Fatalf("publish meta = %#v", meta)
	}

	if sendBody["channel_id"] != "user-7" || sendBody["channel_type"] != float64(1) {
		t.Fatalf("send body = %#v", sendBody)
	}
	payload, _ := sendBody["payload"].(map[string]any)
	if payload["type"] != float64(17) || payload["profile"] != "octo/v1" || payload["card_version"] != "1.5" {
		t.Fatalf("payload envelope = %#v", payload)
	}
	result, _ := payload["octo_result"].(map[string]any)
	if result["schema"] != "html.publish.result" || result["version"] != float64(1) || result["request_id"] != "req-123" {
		t.Fatalf("octo_result identity = %#v", result)
	}
	if result["registered"] != true || result["doc_id"] != "doc-123" || result["doc_version"] != float64(3) {
		t.Fatalf("octo_result data = %#v", result)
	}
	card, _ := payload["card"].(map[string]any)
	if card["type"] != "AdaptiveCard" || card["version"] != "1.5" {
		t.Fatalf("card = %#v", card)
	}
}

func TestHTMLPublishAndNotify_UnregisteredDoesNotSend(t *testing.T) {
	sendCalls := 0
	root, tf, _ := rootWithService(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/v1/bot/sendMessage" {
			sendCalls++
		}
		_, _ = w.Write([]byte(`{"slug":"report","version":1,"doc_id":"doc-1","share_url":"https://docs.example.test/d/doc-1","registered":false,"status":"registration_failed"}`))
	})

	root.SetArgs(validPublishAndNotifyArgs())
	if err := root.Execute(); err == nil {
		t.Fatal("expected registered=false error")
	}
	if sendCalls != 0 {
		t.Fatalf("message send calls = %d, want 0", sendCalls)
	}
	if !strings.Contains(tf.ErrOut.String(), "HTML_NOT_REGISTERED") {
		t.Fatalf("stderr = %s", tf.ErrOut.String())
	}
}

func TestHTMLPublishAndNotify_PublishFailureDoesNotSend(t *testing.T) {
	publishCalls := 0
	sendCalls := 0
	root, _, _ := rootWithService(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/docs" {
			publishCalls++
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(`{"error":{"code":"VALIDATION_ERROR","message":"bad html"}}`))
			return
		}
		sendCalls++
	})

	root.SetArgs(validPublishAndNotifyArgs())
	if err := root.Execute(); err == nil {
		t.Fatal("expected publish error")
	}
	if publishCalls != 1 || sendCalls != 0 {
		t.Fatalf("publish calls = %d, send calls = %d", publishCalls, sendCalls)
	}
}

func TestHTMLPublishAndNotify_DoesNotRetryPublish(t *testing.T) {
	publishCalls := 0
	sendCalls := 0
	root, _, _ := rootWithService(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/docs" {
			publishCalls++
			w.WriteHeader(http.StatusServiceUnavailable)
			_, _ = w.Write([]byte(`{"error":{"code":"UPSTREAM_UNAVAILABLE","message":"registration unavailable"}}`))
			return
		}
		sendCalls++
	})

	root.SetArgs(validPublishAndNotifyArgs())
	if err := root.Execute(); err == nil {
		t.Fatal("expected publish error")
	}
	if publishCalls != 1 || sendCalls != 0 {
		t.Fatalf("publish calls = %d, send calls = %d", publishCalls, sendCalls)
	}
}

func TestHTMLPublishAndNotify_DoesNotRetryTransientSendFailure(t *testing.T) {
	sendCalls := 0
	root, tf, _ := rootWithService(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/v1/docs" {
			_, _ = w.Write([]byte(`{"slug":"report","version":1,"doc_id":"doc-1","share_url":"https://docs.example.test/d/doc-1","registered":true,"status":"published"}`))
			return
		}
		sendCalls++
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = w.Write([]byte(`{"error":{"code":"UPSTREAM_UNAVAILABLE","message":"try later"}}`))
	})

	root.SetArgs(validPublishAndNotifyArgs())
	if err := root.Execute(); err == nil {
		t.Fatal("expected send error")
	}
	if sendCalls != 1 {
		t.Fatalf("message send calls = %d, want 1", sendCalls)
	}
	assertUnknownDeliveryError(t, tf.ErrOut.String())
}

func TestHTMLPublishAndNotify_DoesNotRetryWhenSendResponseIsLost(t *testing.T) {
	sendCalls := 0
	root, tf, _ := rootWithService(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/v1/docs" {
			_, _ = w.Write([]byte(`{"slug":"report","version":1,"doc_id":"doc-1","share_url":"https://docs.example.test/d/doc-1","registered":true,"status":"published"}`))
			return
		}
		sendCalls++
		conn, _, err := w.(http.Hijacker).Hijack()
		if err != nil {
			t.Errorf("hijack response: %v", err)
			return
		}
		_ = conn.Close()
	})

	root.SetArgs(validPublishAndNotifyArgs())
	if err := root.Execute(); err == nil {
		t.Fatal("expected lost response error")
	}
	if sendCalls != 1 {
		t.Fatalf("message send calls = %d, want 1", sendCalls)
	}
	assertUnknownDeliveryError(t, tf.ErrOut.String())
}

func TestHTMLPublishAndNotify_InvalidSendResponseIsUnknownDelivery(t *testing.T) {
	root, tf, _ := rootWithService(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/v1/docs" {
			_, _ = w.Write([]byte(`{"slug":"report","version":1,"doc_id":"doc-1","share_url":"https://docs.example.test/d/doc-1","registered":true,"status":"published"}`))
			return
		}
		_, _ = w.Write([]byte(`{"message_id":"42","message_seq":7}`))
	})

	root.SetArgs(validPublishAndNotifyArgs())
	if err := root.Execute(); err == nil {
		t.Fatal("expected invalid message response error")
	}
	assertUnknownDeliveryError(t, tf.ErrOut.String())
}

func TestHTMLPublishAndNotify_InvalidPublishResponseDoesNotSend(t *testing.T) {
	sendCalls := 0
	root, tf, _ := rootWithService(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/v1/bot/sendMessage" {
			sendCalls++
		}
		_, _ = w.Write([]byte(`{"slug":"report","version":1,"doc_id":"doc-1","share_url":"javascript:alert(1)","registered":true,"status":"published"}`))
	})

	root.SetArgs(validPublishAndNotifyArgs())
	if err := root.Execute(); err == nil {
		t.Fatal("expected invalid share_url error")
	}
	if sendCalls != 0 {
		t.Fatalf("message send calls = %d, want 0", sendCalls)
	}
	if !strings.Contains(tf.ErrOut.String(), "invalid share_url") {
		t.Fatalf("stderr = %s", tf.ErrOut.String())
	}
}

func TestHTMLPublishAndNotify_ValidatesArgumentsBeforeRequests(t *testing.T) {
	requests := 0
	root, tf, _ := rootWithService(t, func(w http.ResponseWriter, r *http.Request) {
		requests++
	})
	args := validPublishAndNotifyArgs()
	args[len(args)-1] = "9"
	root.SetArgs(args)

	if err := root.Execute(); err == nil {
		t.Fatal("expected channel type validation error")
	}
	if requests != 0 {
		t.Fatalf("requests = %d, want 0", requests)
	}
	if !strings.Contains(tf.ErrOut.String(), "--channel-type must be 1, 2, or 5") {
		t.Fatalf("stderr = %s", tf.ErrOut.String())
	}
}

func TestHTMLPublishAndNotify_AppBotRejectsNonDMBeforePublish(t *testing.T) {
	requests := 0
	root, tf, _ := rootWithService(t, func(w http.ResponseWriter, r *http.Request) {
		requests++
	})
	args := validPublishAndNotifyArgs()
	args[len(args)-1] = "2"
	root.SetArgs(args)

	if err := root.Execute(); err == nil {
		t.Fatal("expected App Bot DM-only validation error")
	}
	if requests != 0 {
		t.Fatalf("requests = %d, want 0", requests)
	}
	if !strings.Contains(tf.ErrOut.String(), "App Bot publish-and-notify destinations must use --channel-type 1") {
		t.Fatalf("stderr = %s", tf.ErrOut.String())
	}
}

func assertUnknownDeliveryError(t *testing.T, stderr string) {
	t.Helper()
	for _, want := range []string{
		"DELIVERY_OUTCOME_UNKNOWN",
		"delivery outcome unknown, DO NOT rerun publish-and-notify",
		"不得重新发布",
		"Do not rerun publish-and-notify or republish the HTML",
	} {
		if !strings.Contains(stderr, want) {
			t.Fatalf("stderr missing %q: %s", want, stderr)
		}
	}
}

func validPublishAndNotifyArgs() []string {
	return []string{
		"html", "publish-and-notify",
		"--slug", "report",
		"--html", "<html>ok</html>",
		"--title", "Report",
		"--mount-type", "space",
		"--request-id", "req-1",
		"--channel-id", "user-1",
		"--channel-type", "1",
	}
}
