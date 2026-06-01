package registry

import "testing"

func TestPasswordHashAndVerifyBcrypt(t *testing.T) {
	hash, err := HashPasswordForStorage("correct horse")
	if err != nil {
		t.Fatalf("HashPasswordForStorage: %v", err)
	}
	if !IsBcryptHash(hash) {
		t.Fatalf("hash = %q, want bcrypt prefix", hash)
	}
	ok, needsRehash := VerifyPassword(hash, "correct horse")
	if !ok || needsRehash {
		t.Fatalf("VerifyPassword bcrypt ok=%v needsRehash=%v, want ok without rehash", ok, needsRehash)
	}
	ok, _ = VerifyPassword(hash, "wrong")
	if ok {
		t.Fatalf("VerifyPassword accepted wrong password")
	}
}

func TestVerifyPasswordAcceptsLegacySHA256AndRequestsRehash(t *testing.T) {
	hash := LegacySHA256Hash("old password")
	if !IsLegacySHA256Hash(hash) {
		t.Fatalf("legacy hash not recognized")
	}
	ok, needsRehash := VerifyPassword(hash, "old password")
	if !ok || !needsRehash {
		t.Fatalf("VerifyPassword legacy ok=%v needsRehash=%v, want ok with rehash", ok, needsRehash)
	}
	ok, _ = VerifyPassword(hash, "wrong")
	if ok {
		t.Fatalf("VerifyPassword accepted wrong legacy password")
	}
}

func TestGenerateSessionTokenUsesExpectedEntropyShape(t *testing.T) {
	a, err := generateSessionToken()
	if err != nil {
		t.Fatalf("generateSessionToken: %v", err)
	}
	b, err := generateSessionToken()
	if err != nil {
		t.Fatalf("generateSessionToken second: %v", err)
	}
	if len(a) != sessionTokenBytes*2 {
		t.Fatalf("token length = %d, want %d", len(a), sessionTokenBytes*2)
	}
	if a == b {
		t.Fatalf("two generated session tokens matched")
	}
	if sessionDigest(a) == sessionDigest(b) {
		t.Fatalf("two generated session token digests matched")
	}
}
