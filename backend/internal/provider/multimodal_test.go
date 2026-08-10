package provider

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestOpenAIMessagesMapsImageToDataURL(t *testing.T) {
	messages := openAIMessages([]Message{{Role: "user", Content: "描述图片", Parts: []ContentPart{{Type: "image", MediaType: "image/png", Data: "YWJj"}}}})
	payload, err := json.Marshal(messages)
	if err != nil {
		t.Fatal(err)
	}
	text := string(payload)
	if !strings.Contains(text, `"type":"image_url"`) || !strings.Contains(text, `data:image/png;base64,YWJj`) {
		t.Fatalf("unexpected payload: %s", text)
	}
}

func TestAnthropicMessageMapsImageToBase64Source(t *testing.T) {
	payload, err := json.Marshal(anthropicMessage(Message{Role: "user", Parts: []ContentPart{{Type: "image", MediaType: "image/jpeg", Data: "YWJj"}}}))
	if err != nil {
		t.Fatal(err)
	}
	text := string(payload)
	if !strings.Contains(text, `"type":"base64"`) || !strings.Contains(text, `"media_type":"image/jpeg"`) {
		t.Fatalf("unexpected payload: %s", text)
	}
}
