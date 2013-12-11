// Copyright 2010 The Go Authors. All rights reserved.
// Copyright 2011 ThePiachu. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

// Package bitelliptic implements several Koblitz elliptic curves over prime
// fields.
//
// This package operates, internally, on Jacobian coordinates. For a given
// (x, y) position on the curve, the Jacobian coordinates are (x1, y1, z1)
// where x = x1/z1² and y = y1/z1³. The greatest speedups come when the whole
// calculation can be performed within the transform (as in ScalarMult and
// ScalarBaseMult). But even for Add and Double, it's faster to apply and
// reverse the transform than to operate in affine coordinates.
package kelliptic

import (
	"crypto/elliptic"
	"errors"
	"math/big"
	"sync"
)

// A Curve represents a Koblitz Curve with a=0.
// See http://www.hyperellipticurve.org/EFD/g1p/auto-shortw.html
type Curve struct {
	P       *big.Int // the order of the underlying field
	N       *big.Int // the order of the base point
	B       *big.Int // the constant of the Curve equation
	Gx, Gy  *big.Int // (x,y) of the base point
	BitSize int      // the size of the underlying field
}

func (curve *Curve) Params() *elliptic.CurveParams {
	return &elliptic.CurveParams{
		P:       curve.P,
		N:       curve.N,
		B:       curve.B,
		Gx:      curve.Gx,
		Gy:      curve.Gy,
		BitSize: curve.BitSize,
	}
}

// IsOnCurve returns true if the given (x,y) lies on the curve.
func (curve *Curve) IsOnCurve(x, y *big.Int) bool {
	// y² = x³ + b
	y2 := new(big.Int).Mul(y, y) //y²
	y2.Mod(y2, curve.P)          //y²%P

	x3 := new(big.Int).Mul(x, x) //x²
	x3.Mul(x3, x)                //x³

	x3.Add(x3, curve.B) //x³+B
	x3.Mod(x3, curve.P) //(x³+B)%P

	return x3.Cmp(y2) == 0
}

// affineFromJacobian reverses the Jacobian transform. See the comment at the
// top of the file.
//
// TODO(x): double check if the function is okay
func (curve *Curve) affineFromJacobian(x, y, z *big.Int) (xOut, yOut *big.Int) {
	zinv := new(big.Int).ModInverse(z, curve.P)
	zinvsq := new(big.Int).Mul(zinv, zinv)

	xOut = new(big.Int).Mul(x, zinvsq)
	xOut.Mod(xOut, curve.P)
	zinvsq.Mul(zinvsq, zinv)
	yOut = new(big.Int).Mul(y, zinvsq)
	yOut.Mod(yOut, curve.P)
	return
}

// Add returns the sum of (x1,y1) and (x2,y2)
func (curve *Curve) Add(x1, y1, x2, y2 *big.Int) (*big.Int, *big.Int) {
	z := new(big.Int).SetInt64(1)
	return curve.affineFromJacobian(curve.addJacobian(x1, y1, z, x2, y2, z))
}

// addJacobian takes two points in Jacobian coordinates, (x1, y1, z1) and
// (x2, y2, z2) and returns their sum, also in Jacobian form.
func (curve *Curve) addJacobian(x1, y1, z1, x2, y2, z2 *big.Int) (*big.Int, *big.Int, *big.Int) {
	// See http://hyperellipticurve.org/EFD/g1p/auto-shortw-jacobian-0.html#addition-add-2007-bl
	z1z1 := new(big.Int).Mul(z1, z1)
	z1z1.Mod(z1z1, curve.P)
	z2z2 := new(big.Int).Mul(z2, z2)
	z2z2.Mod(z2z2, curve.P)

	u1 := new(big.Int).Mul(x1, z2z2)
	u1.Mod(u1, curve.P)
	u2 := new(big.Int).Mul(x2, z1z1)
	u2.Mod(u2, curve.P)
	h := new(big.Int).Sub(u2, u1)
	if h.Sign() == -1 {
		h.Add(h, curve.P)
	}
	i := new(big.Int).Lsh(h, 1)
	i.Mul(i, i)
	j := new(big.Int).Mul(h, i)

	s1 := new(big.Int).Mul(y1, z2)
	s1.Mul(s1, z2z2)
	s1.Mod(s1, curve.P)
	s2 := new(big.Int).Mul(y2, z1)
	s2.Mul(s2, z1z1)
	s2.Mod(s2, curve.P)
	r := new(big.Int).Sub(s2, s1)
	if r.Sign() == -1 {
		r.Add(r, curve.P)
	}
	r.Lsh(r, 1)
	v := new(big.Int).Mul(u1, i)

	x3 := new(big.Int).Set(r)
	x3.Mul(x3, x3)
	x3.Sub(x3, j)
	x3.Sub(x3, v)
	x3.Sub(x3, v)
	x3.Mod(x3, curve.P)

	y3 := new(big.Int).Set(r)
	v.Sub(v, x3)
	y3.Mul(y3, v)
	s1.Mul(s1, j)
	s1.Lsh(s1, 1)
	y3.Sub(y3, s1)
	y3.Mod(y3, curve.P)

	z3 := new(big.Int).Add(z1, z2)
	z3.Mul(z3, z3)
	z3.Sub(z3, z1z1)
	if z3.Sign() == -1 {
		z3.Add(z3, curve.P)
	}
	z3.Sub(z3, z2z2)
	if z3.Sign() == -1 {
		z3.Add(z3, curve.P)
	}
	z3.Mul(z3, h)
	z3.Mod(z3, curve.P)

	return x3, y3, z3
}

// Double returns 2*(x,y)
func (curve *Curve) Double(x1, y1 *big.Int) (*big.Int, *big.Int) {
	z1 := new(big.Int).SetInt64(1)
	return curve.affineFromJacobian(curve.doubleJacobian(x1, y1, z1))
}

// doubleJacobian takes a point in Jacobian coordinates, (x, y, z), and
// returns its double, also in Jacobian form.
//
// See http://hyperellipticurve.org/EFD/g1p/auto-shortw-jacobian-0.html#doubling-dbl-2009-l
func (curve *Curve) doubleJacobian(x, y, z *big.Int) (*big.Int, *big.Int, *big.Int) {
	a := new(big.Int).Mul(x, x) //X1²
	b := new(big.Int).Mul(y, y) //Y1²
	c := new(big.Int).Mul(b, b) //B²

	d := new(big.Int).Add(x, b) //X1+B
	d.Mul(d, d)                 //(X1+B)²
	d.Sub(d, a)                 //(X1+B)²-A
	d.Sub(d, c)                 //(X1+B)²-A-C
	d.Mul(d, big.NewInt(2))     //2*((X1+B)²-A-C)

	e := new(big.Int).Mul(big.NewInt(3), a) //3*A
	f := new(big.Int).Mul(e, e)             //E²

	x3 := new(big.Int).Mul(big.NewInt(2), d) //2*D
	x3.Sub(f, x3)                            //F-2*D
	x3.Mod(x3, curve.P)

	y3 := new(big.Int).Sub(d, x3)                  //D-X3
	y3.Mul(e, y3)                                  //E*(D-X3)
	y3.Sub(y3, new(big.Int).Mul(big.NewInt(8), c)) //E*(D-X3)-8*C
	y3.Mod(y3, curve.P)

	z3 := new(big.Int).Mul(y, z) //Y1*Z1
	z3.Mul(big.NewInt(2), z3)    //3*Y1*Z1
	z3.Mod(z3, curve.P)

	return x3, y3, z3
}

// ScalarMult returns k*(Bx,By) where k is a number in big-endian form.
//
// TODO(x): double check if it is okay
func (curve *Curve) ScalarMult(Bx, By *big.Int, k []byte) (*big.Int, *big.Int) {
	// We have a slight problem in that the identity of the group (the
	// point at infinity) cannot be represented in (x, y) form on a finite
	// machine. Thus the standard add/double algorithm has to be tweaked
	// slightly: our initial state is not the identity, but x, and we
	// ignore the first true bit in |k|.  If we don't find any true bits in
	// |k|, then we return nil, nil, because we cannot return the identity
	// element.

	Bz := new(big.Int).SetInt64(1)
	x := Bx
	y := By
	z := Bz

	seenFirstTrue := false
	for _, byte := range k {
		for bitNum := 0; bitNum < 8; bitNum++ {
			if seenFirstTrue {
				x, y, z = curve.doubleJacobian(x, y, z)
			}
			if byte&0x80 == 0x80 {
				if !seenFirstTrue {
					seenFirstTrue = true
				} else {
					x, y, z = curve.addJacobian(Bx, By, Bz, x, y, z)
				}
			}
			byte <<= 1
		}
	}

	if !seenFirstTrue {
		return nil, nil
	}

	return curve.affineFromJacobian(x, y, z)
}

// ScalarBaseMult returns k*G, where G is the base point of the group and k is
// an integer in big-endian form.
func (curve *Curve) ScalarBaseMult(k []byte) (*big.Int, *big.Int) {
	return curve.ScalarMult(curve.Gx, curve.Gy, k)
}

//curve parameters taken from:
//http://www.secg.org/collateral/sec2_final.pdf

var initonce sync.Once
var secp160k1 *Curve
var secp192k1 *Curve
var secp224k1 *Curve
var secp256k1 *Curve

func initAll() {
	initS160()
	initS192()
	initS224()
	initS256()
}

func initS160() {
	// See SEC 2 section 2.4.1
	secp160k1 = new(Curve)
	secp160k1.P, _ = new(big.Int).SetString("FFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFEFFFFAC73", 16)
	secp160k1.N, _ = new(big.Int).SetString("0100000000000000000001B8FA16DFAB9ACA16B6B3", 16)
	secp160k1.B, _ = new(big.Int).SetString("0000000000000000000000000000000000000007", 16)
	secp160k1.Gx, _ = new(big.Int).SetString("3B4C382CE37AA192A4019E763036F4F5DD4D7EBB", 16)
	secp160k1.Gy, _ = new(big.Int).SetString("938CF935318FDCED6BC28286531733C3F03C4FEE", 16)
	secp160k1.BitSize = 160
}

func initS192() {
	// See SEC 2 section 2.5.1
	secp192k1 = new(Curve)
	secp192k1.P, _ = new(big.Int).SetString("FFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFEFFFFEE37", 16)
	secp192k1.N, _ = new(big.Int).SetString("FFFFFFFFFFFFFFFFFFFFFFFE26F2FC170F69466A74DEFD8D", 16)
	secp192k1.B, _ = new(big.Int).SetString("000000000000000000000000000000000000000000000003", 16)
	secp192k1.Gx, _ = new(big.Int).SetString("DB4FF10EC057E9AE26B07D0280B7F4341DA5D1B1EAE06C7D", 16)
	secp192k1.Gy, _ = new(big.Int).SetString("9B2F2F6D9C5628A7844163D015BE86344082AA88D95E2F9D", 16)
	secp192k1.BitSize = 192
}

func initS224() {
	// See SEC 2 section 2.6.1
	secp224k1 = new(Curve)
	secp224k1.P, _ = new(big.Int).SetString("FFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFEFFFFE56D", 16)
	secp224k1.N, _ = new(big.Int).SetString("010000000000000000000000000001DCE8D2EC6184CAF0A971769FB1F7", 16)
	secp224k1.B, _ = new(big.Int).SetString("00000000000000000000000000000000000000000000000000000005", 16)
	secp224k1.Gx, _ = new(big.Int).SetString("A1455B334DF099DF30FC28A169A467E9E47075A90F7E650EB6B7A45C", 16)
	secp224k1.Gy, _ = new(big.Int).SetString("7E089FED7FBA344282CAFBD6F7E319F7C0B0BD59E2CA4BDB556D61A5", 16)
	secp224k1.BitSize = 224
}

func initS256() {
	// See SEC 2 section 2.7.1
	secp256k1 = new(Curve)
	secp256k1.P, _ = new(big.Int).SetString("FFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFEFFFFFC2F", 16)
	secp256k1.N, _ = new(big.Int).SetString("FFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFEBAAEDCE6AF48A03BBFD25E8CD0364141", 16)
	secp256k1.B, _ = new(big.Int).SetString("0000000000000000000000000000000000000000000000000000000000000007", 16)
	secp256k1.Gx, _ = new(big.Int).SetString("79BE667EF9DCBBAC55A06295CE870B07029BFCDB2DCE28D959F2815B16F81798", 16)
	secp256k1.Gy, _ = new(big.Int).SetString("483ADA7726A3C4655DA4FBFC0E1108A8FD17B448A68554199C47D08FFB10D4B8", 16)
	secp256k1.BitSize = 256
}

// S160 returns a Curve which implements secp160k1 (see SEC 2 section 2.4.1)
func S160() *Curve {
	initonce.Do(initAll)
	return secp160k1
}

// S192 returns a Curve which implements secp192k1 (see SEC 2 section 2.5.1)
func S192() *Curve {
	initonce.Do(initAll)
	return secp192k1
}

// S224 returns a Curve which implements secp224k1 (see SEC 2 section 2.6.1)
func S224() *Curve {
	initonce.Do(initAll)
	return secp224k1
}

// S256 returns a Curve which implements secp256k1 (see SEC 2 section 2.7.1)
func S256() *Curve {
	initonce.Do(initAll)
	return secp256k1
}

// Point Compression Routines. These could use a lot of testing.
func (curve *Curve) CompressPoint(X, Y *big.Int) (cp []byte) {
	by := new(big.Int).And(Y, big.NewInt(1)).Int64()
	bx := X.Bytes()
	cp = make([]byte, len(bx)+1)
	if by == 1 {
		cp[0] = byte(3)
	} else {
		cp[0] = byte(2)
	}
	copy(cp[1:], bx)

	return
}

func (curve *Curve) DecompressPoint(cp []byte) (X, Y *big.Int, err error) {
	var c int64

	switch cp[0] { // c = 2 most signiﬁcant bits of S
	case byte(0x03):
		c = 1
		break
	case byte(0x02):
		c = 0
		break
	case byte(0x04): // This is an uncompressed point. Use base Unmarshal.
		X, Y = elliptic.Unmarshal(curve, cp)
		return
	default:
		return nil, nil, errors.New("Not a compressed point. (Invalid Header)")
	}

	byteLen := (curve.Params().BitSize + 7) >> 3
	if len(cp) != 1+byteLen {
		return nil, nil, errors.New("Not a compressed point. (Require 1 + key size)")
	}

	X = new(big.Int).SetBytes(cp[1:])
	Y = new(big.Int)

	Y.Mod(Y.Mul(X, X), curve.P) // solve for y in y**2 = x**3 + x*a + b (mod p)
	Y.Mod(Y.Mul(Y, X), curve.P) // assume a = 0
	Y.Mod(Y.Add(Y, curve.B), curve.P)

	Y = curve.Sqrt(Y)

	if Y.Cmp(big.NewInt(0)) == 0 {
		return nil, nil, errors.New("Not a compressed point. (Not on curve)")
	}

	if c != new(big.Int).And(Y, big.NewInt(1)).Int64() {
		Y.Sub(curve.P, Y)
	}

	return
}

// Sqrt returns the module square root.
//
// Modulo Square root involves deep magic. Uses the Shanks-Tonelli algorithem:
//    http://en.wikipedia.org/wiki/Shanks-Tonelli_algorithm
// Translated from a python implementation found here:
//    http://eli.thegreenplace.net/2009/03/07/computing-modular-square-roots-in-python/
func (curve *Curve) Sqrt(a *big.Int) *big.Int {
	ZERO := big.NewInt(0)
	ONE := big.NewInt(1)
	TWO := big.NewInt(2)
	THREE := big.NewInt(3)
	FOUR := big.NewInt(4)

	p := curve.P
	c := new(big.Int)

	// Simple Cases
	//

	if legendre_symbol(a, p) != 1 {
		return ZERO
	} else if a.Cmp(ZERO) == 0 {
		return ZERO
	} else if p.Cmp(TWO) == 0 {
		return p
	} else if c.Mod(p, FOUR).Cmp(THREE) == 0 {
		c.Add(p, ONE)
		c.Div(c, FOUR)
		c.Exp(a, c, p)
		return c
	}

	// Partition p-1 to s * 2^e for an odd s (i.e.
	// reduce all the powers of 2 from p-1)
	//
	s := new(big.Int)
	s.Sub(p, ONE)

	e := new(big.Int)
	e.Set(ZERO)
	for c.Mod(s, TWO).Cmp(ZERO) == 0 {
		s.Div(s, TWO)
		e.Add(e, ONE)
	}

	// Find some 'n' with a legendre symbol n|p = -1.
	// Shouldn't take long.
	//
	n := new(big.Int)
	n.Set(TWO)
	for legendre_symbol(n, p) != -1 {
		n.Add(n, ONE)
	}

	/*
	   Here be dragons!

	   Read the paper "Square roots from 1; 24, 51,
	   10 to Dan Shanks" by Ezra Brown for more
	   information
	*/

	//  x is a guess of the square root that gets better
	//  with each iteration.
	x := new(big.Int)
	x.Add(s, ONE)
	x.Div(x, TWO)
	x.Exp(a, x, p)

	// b is the "fudge factor" - by how much we're off
	// with the guess. The invariant x^2 = ab (mod p)
	// is maintained throughout the loop.
	b := new(big.Int)
	b.Exp(a, s, p)

	// g is used for successive powers of n to update both a and b
	g := new(big.Int)
	g.Exp(n, s, p)

	// r is the exponent - decreases with each update
	r := new(big.Int)
	r.Set(e)

	t := new(big.Int)
	m := new(big.Int)
	gs := new(big.Int)

	for {
		t.Set(b)
		m.Set(ZERO)

		for ; m.Cmp(r) < 0; m.Add(m, ONE) {
			if t.Cmp(ONE) == 0 {
				break
			}
			t.Exp(t, TWO, p)
		}

		if m.Cmp(ZERO) == 0 {
			return x
		}

		gs.Sub(r, m)
		gs.Sub(gs, ONE)
		gs.Exp(TWO, gs, nil)
		gs.Exp(g, gs, p)

		g.Mod(g.Mul(gs, gs), p)
		x.Mod(x.Mul(x, gs), p)
		b.Mod(b.Mul(b, g), p)
		r.Set(m)
	}

	//return ZERO // This will never get reached.
}

func legendre_symbol(a, p *big.Int) int {
	ZERO := big.NewInt(0)
	ONE := big.NewInt(1)
	TWO := big.NewInt(2)

	ls := new(big.Int).Mod(a, p)

	if ls.Cmp(ZERO) == 0 {
		return 0 // 0 if a ≡ 0 (mod p)
	}

	ps := new(big.Int).Sub(p, ONE)

	ls.Div(ps, TWO)
	ls.Exp(a, ls, p)

	if c := ls.Cmp(ps); c == 0 {
		return -1 // -1 if a is a quadratic non-residue modulo p
	}

	return 1 // 1 if a is a quadratic residue modulo p and a ≢ 0 (mod p)
}
