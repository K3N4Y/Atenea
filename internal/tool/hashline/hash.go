package hashline

import (
	"fmt"
	"strings"
)

func normalizeForHash(text string) string {
	rows := strings.Split(text, "\n")
	for i := range rows {
		rows[i] = strings.TrimRight(rows[i], " \t\r")
	}
	return strings.Join(rows, "\n")
}

// ComputeFileHash is low16(xxHash32(seed=0)) after per-line trailing whitespace normalization.
func ComputeFileHash(text string) string {
	return fmt.Sprintf("%04X", xxh32([]byte(normalizeForHash(text)), 0)&0xffff)
}
func xxh32(b []byte, seed uint32) uint32 {
	const p1 uint32 = 2654435761
	const p2 uint32 = 2246822519
	const p3 uint32 = 3266489917
	const p4 uint32 = 668265263
	const p5 uint32 = 374761393
	rot := func(x uint32, n uint) uint32 { return x<<n | x>>(32-n) }
	rd := func(i int) uint32 { return uint32(b[i]) | uint32(b[i+1])<<8 | uint32(b[i+2])<<16 | uint32(b[i+3])<<24 }
	i := 0
	var h uint32
	if len(b) >= 16 {
		v1 := seed + p1 + p2
		v2 := seed + p2
		v3 := seed
		v4 := seed - p1
		round := func(v, x uint32) uint32 { v += x * p2; v = rot(v, 13); return v * p1 }
		for i <= len(b)-16 {
			v1 = round(v1, rd(i))
			v2 = round(v2, rd(i+4))
			v3 = round(v3, rd(i+8))
			v4 = round(v4, rd(i+12))
			i += 16
		}
		h = rot(v1, 1) + rot(v2, 7) + rot(v3, 12) + rot(v4, 18)
	} else {
		h = seed + p5
	}
	h += uint32(len(b))
	for i <= len(b)-4 {
		h += rd(i) * p3
		h = rot(h, 17) * p4
		i += 4
	}
	for i < len(b) {
		h += uint32(b[i]) * p5
		h = rot(h, 11) * p1
		i++
	}
	h ^= h >> 15
	h *= p2
	h ^= h >> 13
	h *= p3
	h ^= h >> 16
	return h
}
