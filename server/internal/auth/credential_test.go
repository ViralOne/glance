package auth

import (
	"strings"
	"testing"
)

func TestHashAndVerify(t *testing.T) {
	const pw = "correct-horse-battery"
	enc, err := HashPassword(pw)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(enc, pw) {
		t.Fatal("the encoding must not contain the password")
	}
	if !strings.HasPrefix(enc, pbkdf2Scheme+"$") {
		t.Fatalf("encoding: %q", enc)
	}
	if !VerifyPassword(enc, pw) {
		t.Error("the right password should verify")
	}
	for _, wrong := range []string{"", "correct-horse-batter", "Correct-Horse-Battery", pw + " "} {
		if VerifyPassword(enc, wrong) {
			t.Errorf("%q should not verify", wrong)
		}
	}
	// A fresh salt each time, so two identical passwords do not share a hash.
	other, err := HashPassword(pw)
	if err != nil {
		t.Fatal(err)
	}
	if other == enc {
		t.Error("two hashes of the same password must differ; the salt is not random")
	}
	if !VerifyPassword(other, pw) {
		t.Error("the second encoding should also verify")
	}
}

func TestShortPasswordRefused(t *testing.T) {
	if _, err := HashPassword("short"); err == nil {
		t.Error("a password under the minimum should be refused at hashing time")
	}
	if _, err := HashPassword(strings.Repeat("a", MinPasswordLength)); err != nil {
		t.Errorf("a password at the minimum should be accepted: %v", err)
	}
}

// TestMalformedEncodingFailsClosed matters more than it looks: a corrupted or
// truncated row must lock the account, never open it.
func TestMalformedEncodingFailsClosed(t *testing.T) {
	for _, bad := range []string{
		"",
		"not-a-hash",
		"pbkdf2-sha256$",
		"pbkdf2-sha256$210000$$",
		"pbkdf2-sha256$0$c2FsdA$a2V5",
		"pbkdf2-sha256$abc$c2FsdA$a2V5",
		"sha256$210000$c2FsdA$a2V5", // a scheme we do not implement
		// The old plain-SHA256 form, which must no longer be accepted.
		Hash("correct-horse-battery"),
	} {
		if VerifyPassword(bad, "correct-horse-battery") {
			t.Errorf("%q must not verify any password", bad)
		}
		if VerifyPassword(bad, "") {
			t.Errorf("%q must not verify an empty password", bad)
		}
	}
}

func TestNeedsRehash(t *testing.T) {
	enc, err := HashPassword("correct-horse-battery")
	if err != nil {
		t.Fatal(err)
	}
	if NeedsRehash(enc) {
		t.Error("a freshly written hash should not need rehashing")
	}
	// A weaker stored hash should be flagged for replacement.
	if !NeedsRehash("pbkdf2-sha256$1000$c2FsdA$a2V5") {
		t.Error("a low iteration count should be flagged")
	}
	if !NeedsRehash("garbage") {
		t.Error("an unreadable encoding should be flagged")
	}
}

func TestGeneratePassword(t *testing.T) {
	seen := map[string]bool{}
	for i := 0; i < 50; i++ {
		p := GeneratePassword()
		if len(p) < MinPasswordLength {
			t.Fatalf("generated password is too short: %q", p)
		}
		if seen[p] {
			t.Fatalf("generated the same password twice: %q", p)
		}
		seen[p] = true
		if _, err := HashPassword(p); err != nil {
			t.Fatalf("a generated password should be acceptable: %v", err)
		}
	}
}
