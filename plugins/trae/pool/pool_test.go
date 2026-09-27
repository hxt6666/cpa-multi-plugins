package pool

import (
	"testing"
	"time"

	"github.com/mmqz/cpa-multi-plugins/plugins/trae/auth"
)

// TestUntilNextMidnightBounds pins the two wire edges of the resume window:
// the duration is always in (0, 24h] and lands on local midnight.
func TestUntilNextMidnightBounds(t *testing.T) {
	d := UntilNextMidnight()
	if d <= 0 || d > 24*time.Hour {
		t.Fatalf("UntilNextMidnight() = %v, want (0, 24h]", d)
	}
	resume := time.Now().Add(d)
	if resume.Hour() != 0 || resume.Minute() != 0 || resume.Second() > 1 {
		t.Fatalf("resume instant %s is not local midnight", resume)
	}
}

// TestCooldownUntilMidnightSemantics: a CoolPlan cooldown with the midnight
// duration makes the account unhealthy immediately and healthy again after
// the window (simulated by waiting past `until` via a shorter second entry —
// here we verify status fields, the expiry walk is covered by healthy()).
func TestCooldownUntilMidnightSemantics(t *testing.T) {
	p := New("")
	p.Add(&auth.Auth{UID: "u1"})

	d := UntilNextMidnight()
	p.Cooldown("u1", CoolPlan, d, "credits exhausted (0) — resumes at local midnight")

	st, ok := p.Status("u1")
	if !ok {
		t.Fatal("status missing")
	}
	if !st.Cooling {
		t.Fatal("account should be cooling right after Cooldown")
	}
	if st.Disabled {
		t.Fatal("Cooldown must not disable — only disable does")
	}
	if st.Until.IsZero() || !st.Until.After(time.Now()) {
		t.Fatalf("until %v should be in the future", st.Until)
	}
	if st.Until.Hour() != 0 || st.Until.Minute() != 0 {
		t.Fatalf("until %s should be local midnight", st.Until)
	}
	// Pick must skip the cooling account.
	if got := p.Pick(); got != nil {
		t.Fatalf("Pick() returned a cooling account: %s", got.UID)
	}
}

// TestExhaustThenRefillUnfreezes: the full user story — credits hit 0
// (cooldown to midnight), upstream refills early (check-in scan sees >0),
// ReenableIfCredits lifts the window before midnight.
func TestExhaustThenRefillUnfreezes(t *testing.T) {
	p := New("")
	p.Add(&auth.Auth{UID: "u1"})

	p.Cooldown("u1", CoolPlan, UntilNextMidnight(), "credits exhausted (0) — resumes at local midnight")
	if got := p.Pick(); got != nil {
		t.Fatal("exhausted account must not be picked")
	}

	// Refill arrives (sign-in scan / manual /credits with remain > 0).
	p.ReenableIfCredits("u1", 42)
	st, _ := p.Status("u1")
	if st.Cooling {
		t.Fatal("refill should lift the cooling window")
	}
	if got := p.Pick(); got == nil || got.UID != "u1" {
		t.Fatal("refilled account should be picked again")
	}
}

// TestCooldownKeepsDisabled: Cooldown must not resurrect a disabled account
// (session-dead is a human decision), and ReenableIfCredits must not either.
func TestCooldownKeepsDisabled(t *testing.T) {
	p := New("")
	p.Add(&auth.Auth{UID: "u1"})
	p.Disable("u1", "session dead (401)")

	p.Cooldown("u1", CoolPlan, UntilNextMidnight(), "credits exhausted (0) — resumes at local midnight")
	p.ReenableIfCredits("u1", 99)
	if got := p.Pick(); got != nil {
		t.Fatal("disabled account must stay unpickable")
	}
}
