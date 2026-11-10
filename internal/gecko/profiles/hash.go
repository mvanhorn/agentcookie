// Copyright (c) 2011 Google, Inc.
//
// Permission is hereby granted, free of charge, to any person obtaining a copy
// of this software and associated documentation files (the "Software"), to deal
// in the Software without restriction, including without limitation the rights
// to use, copy, modify, merge, publish, distribute, sublicense, and/or sell
// copies of the Software, and to permit persons to whom the Software is
// furnished to do so, subject to the following conditions:
//
// The above copyright notice and this permission notice shall be included in
// all copies or substantial portions of the Software.
//
// THE SOFTWARE IS PROVIDED "AS IS", WITHOUT WARRANTY OF ANY KIND, EXPRESS OR
// IMPLIED, INCLUDING BUT NOT LIMITED TO THE WARRANTIES OF MERCHANTABILITY,
// FITNESS FOR A PARTICULAR PURPOSE AND NONINFRINGEMENT. IN NO EVENT SHALL THE
// AUTHORS OR COPYRIGHT HOLDERS BE LIABLE FOR ANY CLAIM, DAMAGES OR OTHER
// LIABILITY, WHETHER IN AN ACTION OF CONTRACT, TORT OR OTHERWISE, ARISING FROM,
// OUT OF OR IN CONNECTION WITH THE SOFTWARE OR THE USE OR OTHER DEALINGS IN
// THE SOFTWARE.
//
// Mozilla retains CityHash Version 1; later CityHash versions are incompatible.
// Reference: other-licenses/nsis/Contrib/CityHash/cityhash in mozilla/gecko-dev.
package profiles

import (
	"encoding/binary"
	"fmt"
	"math/bits"
	"unicode/utf16"
)

const (
	k0 uint64 = 0xc3a5c85c97cb3127
	k1 uint64 = 0xb492b66fbe98f273
	k2 uint64 = 0x9ae16a3b2f90404f
	k3 uint64 = 0xc949d7c7509e6557
)

func fetch(s []byte) uint64         { return binary.LittleEndian.Uint64(s) }
func rotate(v uint64, n int) uint64 { return bits.RotateLeft64(v, -n) }
func mix(v uint64) uint64           { return v ^ (v >> 47) }
func hash16(u, v uint64) uint64 {
	const m uint64 = 0x9ddfea08eb382d69
	a := (u ^ v) * m
	a ^= a >> 47
	b := (v ^ a) * m
	b ^= b >> 47
	return b * m
}
func weak(s []byte, a, b uint64) (uint64, uint64) {
	w, x, y, z := fetch(s), fetch(s[8:]), fetch(s[16:]), fetch(s[24:])
	a += w
	b = rotate(b+a+z, 21)
	c := a
	a += x + y
	b += rotate(a, 44)
	return a + z, b + c
}

// CityHash64 implements Mozilla's original CityHash Version 1, not the modern variant.
func CityHash64(s []byte) uint64 {
	n := len(s)
	l := uint64(n)
	if n <= 16 {
		if n > 8 {
			a, b := fetch(s), fetch(s[n-8:])
			return hash16(a, rotate(b+l, n)) ^ b
		}
		if n >= 4 {
			a := uint64(binary.LittleEndian.Uint32(s))
			return hash16(l+(a<<3), uint64(binary.LittleEndian.Uint32(s[n-4:])))
		}
		if n > 0 {
			a, b, c := uint64(s[0]), uint64(s[n>>1]), uint64(s[n-1])
			return mix((a+(b<<8))*k2^(l+(c<<2))*k3) * k2
		}
		return k2
	}
	if n <= 32 {
		a, b, c, d := fetch(s)*k1, fetch(s[8:]), fetch(s[n-8:])*k2, fetch(s[n-16:])*k0
		return hash16(rotate(a-b, 43)+rotate(c, 30)+d, a+rotate(b^k3, 20)-c+l)
	}
	if n <= 64 {
		z := fetch(s[24:])
		a := fetch(s) + (l+fetch(s[n-16:]))*k0
		b, c := rotate(a+z, 52), rotate(a, 37)
		a += fetch(s[8:])
		c += rotate(a, 7)
		a += fetch(s[16:])
		vf, vs := a+z, b+rotate(a, 31)+c
		a = fetch(s[16:]) + fetch(s[n-32:])
		z = fetch(s[n-8:])
		b, c = rotate(a+z, 52), rotate(a, 37)
		a += fetch(s[n-24:])
		c += rotate(a, 7)
		a += fetch(s[n-16:])
		wf, ws := a+z, b+rotate(a, 31)+c
		r := mix((vf+ws)*k2 + (wf+vs)*k0)
		return mix(r*k0+vs) * k2
	}
	x, y, z := fetch(s), fetch(s[n-16:])^k1, fetch(s[n-56:])^k0
	v1, v2 := weak(s[n-64:], l, y)
	w1, w2 := weak(s[n-32:], l*k1, k0)
	z += mix(v2) * k1
	x = rotate(z+x, 39) * k1
	y = rotate(y, 33) * k1
	for remaining := (n - 1) &^ 63; remaining > 0; remaining -= 64 {
		x = rotate(x+y+v1+fetch(s[16:]), 37) * k1
		y = rotate(y+v2+fetch(s[48:]), 42) * k1
		x ^= w2
		y ^= v1
		z = rotate(z^w1, 33)
		v1, v2 = weak(s, v2*k1, x+w1)
		w1, w2 = weak(s[32:], z+w2, y)
		z, x = x, z
		s = s[64:]
	}
	return hash16(hash16(v1, w1)+mix(y)*k1+z, hash16(v2, w2)+x)
}

// InstallHash hashes UTF-16LE code units, without a terminating NUL, as Mozilla
// does on supported macOS and Linux architectures. Hex is uppercase, unpadded.
func InstallHash(installDir string) string {
	units := utf16.Encode([]rune(installDir))
	data := make([]byte, 2*len(units))
	for i, u := range units {
		binary.LittleEndian.PutUint16(data[2*i:], u)
	}
	return fmt.Sprintf("%X", CityHash64(data))
}
