package main

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/mmqz/cpa-multi-plugins/plugins/trae/upstream"
)

// resetUsageNoteTestState clears the in-memory ledger and throttle maps so
// tests start from a fresh process state.
func resetUsageNoteTestState(t *testing.T) {
	t.Helper()
	usageLedgerState.Lock()
	usageLedgerState.ledger = nil
	usageLedgerState.loaded = false
	usageLedgerState.dirty = false
	usageLedgerState.lastFlush = time.Time{}
	usageLedgerState.lastWrite = nil
	usageLedgerState.Unlock()
	usageDataDirOnce.Lock()
	usageDataDirOnce.dir = ""
	usageDataDirOnce.done = true // keep tests off the real filesystem
	usageDataDirOnce.Unlock()
}

func TestUsageLedgerObserveAndBucketsTrae(t *testing.T) {
	resetUsageNoteTestState(t)
	defer resetUsageNoteTestState(t)

	base := time.Date(2026, 9, 27, 12, 30, 0, 0, time.UTC)
	usageLedgerObserve("idx-1", "id-1", 1000, false, base)
	usageLedgerObserve("idx-1", "id-1", 500, true, base.Add(10*time.Minute))
	usageLedgerObserve("idx-1", "id-1", 1500, false, base.Add(20*time.Minute))

	usageLedgerState.Lock()
	a := usageLedgerState.ledger.Accounts["idx-1"]
	usageLedgerState.Unlock()
	if a == nil || a.Total.Requests != 3 || a.Total.Success != 2 || a.Total.Failed != 1 || a.Total.Tokens != 3000 {
		t.Fatalf("totals wrong: %+v", a)
	}
	if a.AuthID != "id-1" {
		t.Fatalf("auth id not recorded: %q", a.AuthID)
	}
	if a.Days["2026-09-27"] == nil || a.Hours["2026-09-27T12"] == nil {
		t.Fatalf("buckets missing: %+v", a)
	}
}

func TestUsageNoteSegmentFallsBackWithoutWindow(t *testing.T) {
	resetUsageNoteTestState(t)
	defer resetUsageNoteTestState(t)

	now := time.Date(2026, 9, 27, 12, 30, 0, 0, time.UTC)
	usageLedgerObserve("idx-1", "", 1000, false, now.Add(-30*time.Minute))
	usageLedgerObserve("idx-1", "", 2000, true, now.Add(-10*time.Minute))

	seg := usageNoteSegmentFor("idx-1", now)
	if !strings.HasPrefix(seg, usageSegmentMarker) {
		t.Fatalf("segment marker missing: %q", seg)
	}
	if !strings.Contains(seg, "该账号暂未解析出额度窗口") {
		t.Fatalf("no-window fallback missing (trae exposes no window bounds): %q", seg)
	}
	if !strings.Contains(seg, "累计 请求2 · 成功率50%") {
		t.Fatalf("all-time stats missing: %q", seg)
	}
	if got := usageNoteSegmentFor("idx-unknown", now); got != "" {
		t.Fatalf("unknown credential must render empty, got %q", got)
	}
}

func TestUsageNoteWritePreservesHandWrittenBase(t *testing.T) {
	resetUsageNoteTestState(t)
	defer resetUsageNoteTestState(t)

	// trae has no other note writer; a user's hand-written note must survive
	// as the base ahead of the usage segment.
	baseDoc := []byte(`{"type":"trae","provider":"trae","auth":{"accessToken":"tok","variant":"cn"},"account":{"uid":"u1"},"note":"张三的账号"}`)
	restoreSeams := stubPersistSeams(t, map[string][]byte{"a1": baseDoc})
	defer restoreSeams()

	usageLedgerObserve("a1", "", 1234, false, time.Now())
	usageNoteWrite("a1")
	if n := len(saveCalls()); n != 1 {
		t.Fatalf("first write should save once, got %d", n)
	}
	var saved map[string]json.RawMessage
	parts := strings.SplitN(saveCalls()[0], "=", 2)
	if err := json.Unmarshal([]byte(parts[1]), &saved); err != nil {
		t.Fatalf("saved doc unreadable: %v", err)
	}
	var note string
	if err := json.Unmarshal(saved["note"], &note); err != nil {
		t.Fatalf("note unreadable: %v", err)
	}
	if !strings.HasPrefix(note, "张三的账号 · ") || !strings.Contains(note, usageSegmentMarker) {
		t.Fatalf("hand-written base lost: %q", note)
	}
	// The saved doc must keep the model_cache snapshot (save-funnel guard).
	if _, ok := saved["model_cache"]; ok {
		t.Fatalf("usage writer must not invent model_cache (funnel's job)")
	}

	// Unchanged second write → change-guard skips the save.
	usageNoteWrite("a1")
	if n := len(saveCalls()); n != 1 {
		t.Fatalf("unchanged write must not save (watcher churn), got %d", n)
	}
}

func TestUsageQuotaStampFromSummary(t *testing.T) {
	resetUsageNoteTestState(t)
	defer resetUsageNoteTestState(t)

	now := time.Date(2026, 9, 27, 12, 0, 0, 0, time.UTC)
	usageLedgerObserve("idx-1", "", 1000, false, now)
	usageQuotaStampFromSummary("idx-1", upstream.UsageSummary{
		PlanType:    "Pro",
		Remain:      42,
		RemainKnown: true,
		Used:        58,
		Total:       100,
		CreditsPool: upstream.CreditsPoolInfo{Remain: 1234, Known: true},
	})

	usageLedgerState.Lock()
	a := usageLedgerState.ledger.Accounts["idx-1"]
	usageLedgerState.Unlock()
	if a.Quota == nil {
		t.Fatal("quota not stamped")
	}
	if a.Quota.Remain != 1234 {
		t.Fatalf("credits pool remain must win: %+v", a.Quota)
	}
	if a.Quota.Plan != "Pro" {
		t.Fatalf("plan not recorded: %+v", a.Quota)
	}
	if a.Quota.WindowStart != "" || a.Quota.WindowEnd != "" {
		t.Fatalf("trae exposes no window bounds: %+v", a.Quota)
	}
}
