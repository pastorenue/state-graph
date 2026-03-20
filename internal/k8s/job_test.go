package k8s

import (
	"strings"
	"testing"
)

func TestJobName_BasicKebab(t *testing.T) {
	got := JobName("550e8400e29b41d4", "ValidateOrder")
	want := "kflow-550e8400e29b41d4-validate-order"
	if got != want {
		t.Fatalf("jobName = %q, want %q", got, want)
	}
}

func TestJobName_TruncatesExecID(t *testing.T) {
	got := JobName("550e8400e29b41d4a4b2", "ValidateOrder")
	// execID truncated to 16 chars
	want := "kflow-550e8400e29b41d4-validate-order"
	if got != want {
		t.Fatalf("jobName = %q, want %q", got, want)
	}
}

func TestJobName_MaxLength(t *testing.T) {
	longState := strings.Repeat("A", 100)
	got := JobName("550e8400e29b41d4", longState)
	if len(got) > 63 {
		t.Fatalf("jobName length = %d > 63: %q", len(got), got)
	}
}

func TestJobName_NoTrailingHyphen(t *testing.T) {
	// Ensure truncation doesn't leave trailing hyphens
	for i := 0; i < 10; i++ {
		state := strings.Repeat("LongStateName", i+1)
		got := JobName("abcdef0123456789", state)
		if strings.HasSuffix(got, "-") {
			t.Fatalf("jobName has trailing hyphen: %q", got)
		}
	}
}

func TestJobName_SpecialChars(t *testing.T) {
	got := JobName("abcdef0123456789", "Charge_Payment.2")
	if strings.Contains(got, "_") || strings.Contains(got, ".") {
		t.Fatalf("jobName contains invalid chars: %q", got)
	}
}

func TestJobName_ChargePayment(t *testing.T) {
	got := JobName("550e8400e29b41d4", "ChargePayment")
	want := "kflow-550e8400e29b41d4-charge-payment"
	if got != want {
		t.Fatalf("jobName = %q, want %q", got, want)
	}
}

func TestJobName_UUIDExecID(t *testing.T) {
	// UUID with hyphens — hyphens stripped then truncated to 16
	got := JobName("550e8400-e29b-41d4-a4b2-446655440000", "DoSomething")
	if len(got) > 63 {
		t.Fatalf("jobName too long: %d chars: %q", len(got), got)
	}
	if !strings.HasPrefix(got, "kflow-") {
		t.Fatalf("missing kflow- prefix: %q", got)
	}
}
