// Package rollout implements flagstead's deterministic percent-rollout
// bucketing. A (salt, key) pair always lands in the same of 10,000
// buckets (basis-point resolution), computed with FNV-1a 64 — no state,
// no randomness, no coordination between server instances. Because the
// bucket is fixed and only the threshold moves, raising a rollout
// percentage never kicks out a key that was already enabled ("sticky"
// rollouts), and rollbacks disable exactly the most recently added keys.
package rollout

import "hash/fnv"

// Buckets is the resolution of the rollout space: 10,000 buckets give
// exact basis points, so rollout percentages with two decimals (e.g.
// 0.25) map onto whole buckets.
const Buckets = 10000

// Bucket returns the deterministic bucket in [0, Buckets) for a key under
// a salt. The salt (by default the flag name) decorrelates flags: the
// same user is early-adopter for some flags and late for others.
func Bucket(salt, key string) int {
	h := fnv.New64a()
	h.Write([]byte(salt))
	h.Write([]byte{0}) // unambiguous separator: salt "ab"+key "c" != "a"+"bc"
	h.Write([]byte(key))
	return int(h.Sum64() % Buckets)
}

// InRollout reports whether key is inside the first percent% of the
// bucket space under salt. percent is clamped conceptually to [0, 100];
// 0 always excludes and 100 always includes, regardless of hashing.
func InRollout(salt, key string, percent float64) bool {
	if percent <= 0 {
		return false
	}
	if percent >= 100 {
		return true
	}
	return float64(Bucket(salt, key)) < percent*(Buckets/100)
}
