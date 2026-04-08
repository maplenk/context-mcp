package parser

const maxUint32 = ^uint32(0)

func clampUint32(v int) uint32 {
	if v <= 0 {
		return 0
	}
	if uint64(v) > uint64(maxUint32) {
		return maxUint32
	}
	// #nosec G115 -- v is range-checked and clamped before conversion.
	return uint32(v)
}

func addClampedUint32(base uint32, delta int) uint32 {
	if delta <= 0 {
		return base
	}

	clamped := clampUint32(delta)
	if clamped > maxUint32-base {
		return maxUint32
	}
	return base + clamped
}
