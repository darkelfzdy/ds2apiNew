package completionruntime

import (
	"context"
	"errors"
	"testing"

	"ds2api/internal/auth"
	dsclient "ds2api/internal/deepseek/client"
	"ds2api/internal/promptcompat"
)

func TestFireSegmentPayloadsNormalChainsParents(t *testing.T) {
	ds := &fakeDeepSeekCaller{}
	stdReq := promptcompat.StandardRequest{ResolvedModel: "deepseek-v4-pro"}
	segs := []string{"seg1", "seg2", "seg3"}
	pow, payload, outErr := fireSegmentPayloads(context.Background(), ds, &auth.RequestAuth{}, stdReq, "session-1", segs, 3)
	if outErr != nil {
		t.Fatalf("unexpected error: %#v", outErr)
	}
	if pow != "pow" {
		t.Fatalf("pow mismatch: %q", pow)
	}
	if got := payload["prompt"]; got != "seg3" {
		t.Fatalf("final prompt mismatch: %#v", got)
	}
	if got := payload["parent_message_id"]; got != 102 {
		t.Fatalf("final parent mismatch: %#v (want 102, last committed segment id)", got)
	}
	if len(ds.payloads) != 2 {
		t.Fatalf("expected two fire-and-stop payloads, got %d", len(ds.payloads))
	}
	if ds.payloads[0]["parent_message_id"] != nil {
		t.Fatalf("first segment should have nil parent, got %#v", ds.payloads[0]["parent_message_id"])
	}
	if got := ds.payloads[1]["parent_message_id"]; got != 101 {
		t.Fatalf("second segment parent mismatch: %#v (want 101)", got)
	}
}

func TestFireSegmentPayloadsFallsBackToSingleMessageOnError(t *testing.T) {
	ds := &fakeDeepSeekCaller{fireAndStopErrs: []error{errors.New("upstream boom")}}
	stdReq := promptcompat.StandardRequest{ResolvedModel: "deepseek-v4-pro"}
	segs := []string{"<User>:AAA", "<Assistant>:"}
	pow, payload, outErr := fireSegmentPayloads(context.Background(), ds, &auth.RequestAuth{}, stdReq, "session-1", segs, 3)
	if outErr != nil {
		t.Fatalf("expected fallback instead of error, got %#v", outErr)
	}
	if pow != "pow" {
		t.Fatalf("pow mismatch: %q", pow)
	}
	// 第一段失败：合并全部段还原原文，parent 为 nil（根消息），不依赖断裂的链。
	if got := payload["prompt"]; got != "<User>:AAA<Assistant>:" {
		t.Fatalf("fallback prompt mismatch: %#v", got)
	}
	if got := payload["parent_message_id"]; got != nil {
		t.Fatalf("fallback after first-segment failure should carry nil parent, got %#v", got)
	}
}

func TestFireSegmentPayloadsFallsBackOnCommitUnconfirmed(t *testing.T) {
	ds := &fakeDeepSeekCaller{fireAndStopErrs: []error{nil, dsclient.ErrSegmentCommitUnconfirmed}}
	stdReq := promptcompat.StandardRequest{ResolvedModel: "deepseek-v4-pro"}
	segs := []string{"seg1", "seg2", "seg3"}
	pow, payload, outErr := fireSegmentPayloads(context.Background(), ds, &auth.RequestAuth{}, stdReq, "session-1", segs, 3)
	if outErr != nil {
		t.Fatalf("expected fallback instead of error, got %#v", outErr)
	}
	if pow != "pow" {
		t.Fatalf("pow mismatch: %q", pow)
	}
	// 第一段成功（id 101），第二段提交未确认：回退合并剩余段，parent 用已提交的 101。
	if got := payload["prompt"]; got != "seg2seg3" {
		t.Fatalf("fallback prompt mismatch: %#v", got)
	}
	if got := payload["parent_message_id"]; got != 101 {
		t.Fatalf("fallback parent mismatch: %#v (want 101, last confirmed segment id)", got)
	}
}
