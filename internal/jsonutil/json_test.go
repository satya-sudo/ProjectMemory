package jsonutil

import (
	"strings"
	"testing"
)

func TestStrictJSON(t *testing.T) {
	type input struct {
		Credential string `json:"credential"`
	}
	for _, body := range []string{
		`{"credential":"a","credential":"b"}`,
		`{"credential":"a","Credential":"b"}`,
		`{"Credential":"a"}`,
		`{"credential":null}`,
		`{"credential":"a","unknown":true}`,
		`{"credential":"a"} {}`,
		`{"credential":3}`,
		`{"credential":"unterminated}`,
		"{\"credential\":\"\xff\"}",
		strings.Repeat("[", 70) + "0" + strings.Repeat("]", 70),
	} {
		var dst input
		if Decode([]byte(body), &dst) == nil {
			t.Fatalf("accepted invalid JSON: %q", body)
		}
	}
	var dst input
	if err := Decode([]byte(`{"credential":"valid"}`), &dst); err != nil || dst.Credential != "valid" {
		t.Fatal("valid JSON rejected")
	}
	var nested map[string]any
	if err := Decode([]byte(`{"nested":{"key":1,"key":2}}`), &nested); err == nil {
		t.Fatal("nested duplicate keys accepted")
	}
}
