package agent

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
)

func TestAntigravityStreamJSONUsage(t *testing.T) {
	for _, status := range []string{"SUCCESS", "ERROR"} {
		t.Run(status, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "agy")
			// Official headless envelope: output includes thinking, input includes
			// cache reads. Step usage must not be summed with terminal usage.
			writeTestExecutable(t, path, []byte(`#!/bin/sh
printf '%s\n' '{"event":"init","init":{"model":"gemini-3.8-flash-high"}}'
printf '%s\n' '{"event":"step_update","step_update":{"step_type":"agent_response","text_delta":"Hello\n","usage":{"input_tokens":10415}}}'
printf '%s\n' '{"event":"step_update","step_update":{"text_delta":"world"}}'
printf '%s\n' '{"event":"result","result":{"conversation_id":"conversation-1","status":"`+status+`","error":"test error","response":"Hello\nworld","usage":{"input_tokens":10415,"output_tokens":657,"thinking_tokens":616,"cache_read_tokens":8113,"total_tokens":11072}}}'
`))
			b := &antigravityBackend{cfg: Config{ExecutablePath: path, Logger: quietAntigravityLogger()}}
			session, err := b.Execute(context.Background(), "ignored", ExecOptions{})
			if err != nil {
				t.Fatal(err)
			}
			var streamed strings.Builder
			for msg := range session.Messages {
				if msg.Type == MessageText {
					streamed.WriteString(msg.Content)
				}
			}
			result := <-session.Result
			if result.Output != "Hello\nworld" || streamed.String() != result.Output {
				t.Fatalf("output=%q stream=%q", result.Output, streamed.String())
			}
			if result.SessionID != "conversation-1" {
				t.Fatal(result.SessionID)
			}
			want := TokenUsage{InputTokens: 2302, OutputTokens: 657, CacheReadTokens: 8113}
			if got := result.Usage["gemini-3.8-flash-high"]; got != want {
				t.Fatalf("usage=%+v want=%+v", got, want)
			}
			if status == "ERROR" && (result.Status != "failed" || result.Error != "test error") {
				t.Fatalf("result=%+v", result)
			}
			if status == "SUCCESS" && result.Status != "completed" {
				t.Fatalf("result=%+v", result)
			}
		})
	}
}

func TestAntigravityUsageMissingAndZero(t *testing.T) {
	if got := (antigravityUsage{}).tokenUsage(); got != (TokenUsage{}) {
		t.Fatal(got)
	}
	if got := (antigravityUsage{InputTokens: 5, CacheReadTokens: 10}).tokenUsage(); got.InputTokens != 0 || got.CacheReadTokens != 5 {
		t.Fatal(got)
	}
}
