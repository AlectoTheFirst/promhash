package main

import (
	"context"
	"errors"
	"testing"
	"time"
)

// fakeRetractor is a test double for appRetractor. It records CloseAppValidity
// calls and returns a configured error for specific phashes.
type fakeRetractor struct {
	open       []string
	listErr    error
	closeErr   map[string]error // keyed by phash; nil entry or missing = no error
	closeCalls []closeCall
}

type closeCall struct {
	phash string
	at    time.Time
}

func (f *fakeRetractor) ListOpenDeclaredApps(_ context.Context) ([]string, error) {
	return f.open, f.listErr
}

func (f *fakeRetractor) CloseAppValidity(_ context.Context, appPHash string, at time.Time) error {
	f.closeCalls = append(f.closeCalls, closeCall{phash: appPHash, at: at})
	if f.closeErr != nil {
		return f.closeErr[appPHash]
	}
	return nil
}

// ---------------------------------------------------------------------------
// registerApp tests
// ---------------------------------------------------------------------------

func TestRegisterApp_FirstDeclarationAccepted(t *testing.T) {
	seen := map[string]string{}
	prev, dup := registerApp(seen, "payments", "declared/payments.yaml")
	if dup {
		t.Fatalf("first declaration flagged as duplicate (prev=%q)", prev)
	}
}

func TestRegisterApp_DuplicateAcrossFilesDetected(t *testing.T) {
	// Two files declaring the same app would silently last-write-win in the
	// graph; the batch must fail instead and name both files.
	seen := map[string]string{}
	_, _ = registerApp(seen, "payments", "declared/payments.yaml")
	prev, dup := registerApp(seen, "payments", "declared/payments-copy.yaml")
	if !dup {
		t.Fatal("second declaration of the same app not flagged as duplicate")
	}
	if prev != "declared/payments.yaml" {
		t.Fatalf("prev file = %q, want declared/payments.yaml", prev)
	}
}

func TestRegisterApp_IdentityIsCaseAndSpaceInsensitive(t *testing.T) {
	// App identity is the phash (ToLower+TrimSpace), so "Payments " and
	// "payments" are the SAME app and must collide.
	seen := map[string]string{}
	_, _ = registerApp(seen, "payments", "a.yaml")
	_, dup := registerApp(seen, " Payments", "b.yaml")
	if !dup {
		t.Fatal("case/whitespace variant of the same app not flagged as duplicate")
	}
}

// ---------------------------------------------------------------------------
// shouldReconcile tests
// ---------------------------------------------------------------------------

func TestShouldReconcile_HappyPath(t *testing.T) {
	if !shouldReconcile(false, false, 3, false) {
		t.Error("want true for happy path (validateOnly=false, failed=false, fileCount=3, allowEmpty=false)")
	}
}

func TestShouldReconcile_ValidateOnly(t *testing.T) {
	if shouldReconcile(true, false, 3, false) {
		t.Error("want false when validateOnly=true")
	}
}

func TestShouldReconcile_Failed(t *testing.T) {
	if shouldReconcile(false, true, 3, false) {
		t.Error("want false when failed=true")
	}
}

func TestShouldReconcile_EmptyDir_NoAllowEmpty(t *testing.T) {
	if shouldReconcile(false, false, 0, false) {
		t.Error("want false when fileCount=0 and allowEmpty=false")
	}
}

func TestShouldReconcile_EmptyDir_AllowEmpty(t *testing.T) {
	if !shouldReconcile(false, false, 0, true) {
		t.Error("want true when fileCount=0 but allowEmpty=true")
	}
}

func TestShouldReconcile_NonEmptyDir_AllowEmptyIgnored(t *testing.T) {
	// allowEmpty=false but fileCount>0 — should still reconcile
	if !shouldReconcile(false, false, 5, false) {
		t.Error("want true when fileCount>0 regardless of allowEmpty")
	}
}

// ---------------------------------------------------------------------------
// reconcileRetractions tests
// ---------------------------------------------------------------------------

func TestReconcileRetractions_ClosesAbsentApp(t *testing.T) {
	// present={payments}, open={payments, ledger} → ledger should be closed
	paymentsPHash := "app:payments-phash"
	ledgerPHash := "app:ledger-phash"
	at := time.Date(2026, 6, 3, 12, 0, 0, 0, time.UTC)

	fake := &fakeRetractor{
		open: []string{paymentsPHash, ledgerPHash},
	}
	present := map[string]bool{paymentsPHash: true}

	closed, err := reconcileRetractions(context.Background(), fake, present, at)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(closed) != 1 || closed[0] != ledgerPHash {
		t.Errorf("want closed=[%s], got %v", ledgerPHash, closed)
	}
	// Verify payments was NOT closed
	for _, c := range fake.closeCalls {
		if c.phash == paymentsPHash {
			t.Errorf("payments should not have been closed, but CloseAppValidity was called with %s", c.phash)
		}
	}
	// Verify ledger was closed with the correct timestamp
	if len(fake.closeCalls) != 1 {
		t.Fatalf("want 1 CloseAppValidity call, got %d", len(fake.closeCalls))
	}
	if fake.closeCalls[0].phash != ledgerPHash {
		t.Errorf("want CloseAppValidity called with %s, got %s", ledgerPHash, fake.closeCalls[0].phash)
	}
	if !fake.closeCalls[0].at.Equal(at) {
		t.Errorf("want CloseAppValidity at=%v, got %v", at, fake.closeCalls[0].at)
	}
}

func TestReconcileRetractions_OpenSubsetOfPresent_NothingClosed(t *testing.T) {
	// present={payments, ledger}, open={payments} → nothing closed
	paymentsPHash := "app:payments-phash"
	ledgerPHash := "app:ledger-phash"
	at := time.Date(2026, 6, 3, 12, 0, 0, 0, time.UTC)

	fake := &fakeRetractor{
		open: []string{paymentsPHash},
	}
	present := map[string]bool{paymentsPHash: true, ledgerPHash: true}

	closed, err := reconcileRetractions(context.Background(), fake, present, at)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(closed) != 0 {
		t.Errorf("want no closed apps, got %v", closed)
	}
	if len(fake.closeCalls) != 0 {
		t.Errorf("want no CloseAppValidity calls, got %d", len(fake.closeCalls))
	}
}

func TestReconcileRetractions_ListError_Propagated(t *testing.T) {
	at := time.Now().UTC()
	listErr := errors.New("neo4j unavailable")
	fake := &fakeRetractor{listErr: listErr}
	present := map[string]bool{}

	_, err := reconcileRetractions(context.Background(), fake, present, at)
	if !errors.Is(err, listErr) {
		t.Errorf("want ListOpenDeclaredApps error propagated, got %v", err)
	}
}

func TestReconcileRetractions_CloseError_FailFast(t *testing.T) {
	// If CloseAppValidity returns an error for one app, reconcileRetractions
	// should stop immediately (fail-fast) and surface the error.
	paymentsPHash := "app:payments-phash"
	ledgerPHash := "app:ledger-phash"
	ordersPHash := "app:orders-phash"
	at := time.Now().UTC()

	closeErr := errors.New("transaction failed")
	fake := &fakeRetractor{
		open: []string{paymentsPHash, ledgerPHash, ordersPHash},
		closeErr: map[string]error{
			paymentsPHash: closeErr,
		},
	}
	present := map[string]bool{} // all are absent → all should be closed

	_, err := reconcileRetractions(context.Background(), fake, present, at)
	if err == nil {
		t.Fatal("want error when CloseAppValidity fails, got nil")
	}
	if !errors.Is(err, closeErr) {
		t.Errorf("want close error wrapped in returned error, got %v", err)
	}
	// Fail-fast: only the first close call was attempted (the one that failed)
	if len(fake.closeCalls) != 1 {
		t.Errorf("want 1 CloseAppValidity call (fail-fast), got %d", len(fake.closeCalls))
	}
}

func TestReconcileRetractions_CorrectTimestampPassedThrough(t *testing.T) {
	// Verify the exact `at` time is forwarded to CloseAppValidity
	ledgerPHash := "app:ledger-phash"
	at := time.Date(2025, 12, 31, 23, 59, 59, 0, time.UTC)

	fake := &fakeRetractor{
		open: []string{ledgerPHash},
	}
	present := map[string]bool{} // ledger absent

	if _, err := reconcileRetractions(context.Background(), fake, present, at); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(fake.closeCalls) != 1 {
		t.Fatalf("want 1 close call, got %d", len(fake.closeCalls))
	}
	if !fake.closeCalls[0].at.Equal(at) {
		t.Errorf("want at=%v forwarded unchanged, got %v", at, fake.closeCalls[0].at)
	}
}
