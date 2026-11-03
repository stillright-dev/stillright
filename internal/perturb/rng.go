package perturb

import "math/rand/v2"

// rng draws from PCG, whose output is fixed by its specification. Integers in
// a range are derived here rather than with math/rand's helper methods so a
// published seed keeps producing the same sequences across Go releases.
type rng struct {
	src *rand.PCG
}

func newRNG(seed, stream uint64) *rng {
	return &rng{src: rand.NewPCG(seed, stream)}
}

// intn returns a uniform integer in [0, n) by rejection sampling. n must be > 0.
func (r *rng) intn(n int) int {
	un := uint64(n)
	// Reject the top 2^64 mod n values so every residue is equally likely.
	excess := (-un) % un
	for {
		if v := r.src.Uint64(); v <= ^uint64(0)-excess {
			return int(v % un)
		}
	}
}
