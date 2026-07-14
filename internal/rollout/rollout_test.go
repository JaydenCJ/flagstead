// Tests for the bucketing math. Stickiness and distribution are the two
// properties users bet production traffic on, so both are asserted over
// thousands of deterministic synthetic keys (FNV-1a has no randomness —
// these tests can never flake).
package rollout

import (
	"fmt"
	"testing"
)

func TestBucketIsDeterministic(t *testing.T) {
	a := Bucket("checkout", "user-42")
	b := Bucket("checkout", "user-42")
	if a != b {
		t.Fatalf("same inputs, different buckets: %d vs %d", a, b)
	}
}

func TestBucketStaysInRange(t *testing.T) {
	for i := 0; i < 5000; i++ {
		got := Bucket("salt", fmt.Sprintf("key-%d", i))
		if got < 0 || got >= Buckets {
			t.Fatalf("bucket %d out of [0,%d)", got, Buckets)
		}
	}
}

func TestBucketVariesBySalt(t *testing.T) {
	// Different flags must decorrelate: the same key should land in
	// different buckets for at least some salts.
	same := 0
	for i := 0; i < 100; i++ {
		key := fmt.Sprintf("user-%d", i)
		if Bucket("flag-a", key) == Bucket("flag-b", key) {
			same++
		}
	}
	if same > 5 {
		t.Fatalf("salts barely decorrelate: %d/100 collisions", same)
	}
}

func TestBucketSaltKeyBoundaryIsUnambiguous(t *testing.T) {
	// Without a separator, ("ab","c") and ("a","bc") would hash alike.
	if Bucket("ab", "c") == Bucket("a", "bc") {
		t.Fatal("salt/key concatenation is ambiguous")
	}
}

func TestInRolloutExtremesAreAbsolute(t *testing.T) {
	for i := 0; i < 1000; i++ {
		key := fmt.Sprintf("k%d", i)
		if InRollout("s", key, 0) {
			t.Fatal("0% rollout enabled a key")
		}
		if !InRollout("s", key, 100) {
			t.Fatal("100% rollout excluded a key")
		}
	}
}

func TestInRolloutIsSticky(t *testing.T) {
	// The contract: raising the percentage NEVER kicks out a key that was
	// already in. Verified across every step of a staged rollout.
	steps := []float64{1, 5, 10, 25, 50, 75, 99}
	for i := 0; i < 2000; i++ {
		key := fmt.Sprintf("user-%d", i)
		wasIn := false
		for _, pct := range steps {
			in := InRollout("checkout", key, pct)
			if wasIn && !in {
				t.Fatalf("key %s fell out when raising rollout to %v%%", key, pct)
			}
			wasIn = in
		}
	}
}

func TestInRolloutShareIsRoughlyProportional(t *testing.T) {
	const n = 10000
	for _, pct := range []float64{10, 25, 50} {
		hits := 0
		for i := 0; i < n; i++ {
			if InRollout("dist-check", fmt.Sprintf("user-%d", i), pct) {
				hits++
			}
		}
		got := float64(hits) / n * 100
		if got < pct-2 || got > pct+2 {
			t.Fatalf("%v%% rollout enabled %.2f%% of keys (outside ±2)", pct, got)
		}
	}
}

func TestInRolloutSupportsBasisPointPrecision(t *testing.T) {
	// 0.25% of 10,000 buckets = 25 buckets; a fractional percentage must
	// gate on a strictly smaller population than 1%.
	quarter, one := 0, 0
	for i := 0; i < 10000; i++ {
		key := fmt.Sprintf("u%d", i)
		if InRollout("fine", key, 0.25) {
			quarter++
		}
		if InRollout("fine", key, 1) {
			one++
		}
	}
	if quarter == 0 || quarter >= one {
		t.Fatalf("0.25%% enabled %d keys, 1%% enabled %d — precision broken", quarter, one)
	}
}
