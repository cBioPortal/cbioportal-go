package databin

import (
	"bytes"
	"errors"
	"fmt"
	"math"
	"math/big"
	"math/bits"
	"strconv"
	"strings"
)

// Decimal is an arbitrary-precision decimal number with the value semantics
// of java.math.BigDecimal: an unscaled integer and a scale, so that the value
// is unscaled × 10^-scale. Two Decimals can be numerically equal while having
// different scales (2 vs 2.0); String renders the scale exactly the way
// BigDecimal.toString does, which is also how Jackson serializes BigDecimal.
//
// The zero value is the number 0 with scale 0. Decimals are immutable.
type Decimal struct {
	u     *big.Int
	scale int32
}

var (
	bigZero = big.NewInt(0)
	bigOne  = big.NewInt(1)
	bigTen  = big.NewInt(10)
)

func (d Decimal) unscaled() *big.Int {
	if d.u == nil {
		return bigZero
	}
	return d.u
}

func newDecimal(u *big.Int, scale int32) Decimal {
	return Decimal{u: u, scale: scale}
}

func decimalFromInt(v int64) Decimal {
	return newDecimal(big.NewInt(v), 0)
}

var pow10Cache [64]*big.Int

func init() {
	p := big.NewInt(1)
	for i := range pow10Cache {
		pow10Cache[i] = new(big.Int).Set(p)
		p.Mul(p, bigTen)
	}
}

func pow10(n int64) *big.Int {
	if n < 0 {
		panic("databin: negative power of ten")
	}
	if n < int64(len(pow10Cache)) {
		return pow10Cache[n]
	}
	return new(big.Int).Exp(bigTen, big.NewInt(n), nil)
}

// ParseDecimal parses s with the grammar of new BigDecimal(String): an
// optional sign, digits with an optional decimal point, and an optional
// exponent. The scale of the result is the number of fraction digits minus
// the exponent, so "40.0" has scale 1 and "1E+3" has scale -3.
func ParseDecimal(s string) (Decimal, error) {
	d, ok := parseJavaBigDecimal(s)
	if !ok {
		return Decimal{}, fmt.Errorf("databin: invalid decimal %q", s)
	}
	return d, nil
}

// MustParseDecimal is ParseDecimal that panics on invalid input.
func MustParseDecimal(s string) Decimal {
	d, err := ParseDecimal(s)
	if err != nil {
		panic(err)
	}
	return d
}

func parseJavaBigDecimal(s string) (Decimal, bool) {
	i := 0
	n := len(s)
	if n == 0 {
		return Decimal{}, false
	}
	neg := false
	if s[0] == '+' || s[0] == '-' {
		neg = s[0] == '-'
		i++
	}
	var digits strings.Builder
	fracDigits := 0
	seenDot := false
	nDigits := 0
	for ; i < n; i++ {
		c := s[i]
		if c >= '0' && c <= '9' {
			digits.WriteByte(c)
			nDigits++
			if seenDot {
				fracDigits++
			}
			continue
		}
		if c == '.' {
			if seenDot {
				return Decimal{}, false
			}
			seenDot = true
			continue
		}
		break
	}
	if nDigits == 0 {
		return Decimal{}, false
	}
	var exp int64
	if i < n {
		if s[i] != 'e' && s[i] != 'E' {
			return Decimal{}, false
		}
		i++
		if i >= n {
			return Decimal{}, false
		}
		expStr := s[i:]
		if expStr[0] == '+' {
			expStr = expStr[1:]
			if expStr == "" || expStr[0] == '-' || expStr[0] == '+' {
				return Decimal{}, false
			}
		}
		for j := 0; j < len(expStr); j++ {
			if j == 0 && expStr[j] == '-' {
				continue
			}
			if expStr[j] < '0' || expStr[j] > '9' {
				return Decimal{}, false
			}
		}
		e, err := strconv.ParseInt(expStr, 10, 32)
		if err != nil {
			return Decimal{}, false
		}
		exp = e
	}
	scale := int64(fracDigits) - exp
	if scale > math.MaxInt32 || scale < math.MinInt32 {
		return Decimal{}, false
	}
	u, ok := new(big.Int).SetString(digits.String(), 10)
	if !ok {
		return Decimal{}, false
	}
	if neg {
		u.Neg(u)
	}
	return newDecimal(u, int32(scale)), true
}

// DecimalFromFloat64 mirrors BigDecimal.valueOf(double): the decimal is built
// from the shortest decimal representation of f as rendered by
// Double.toString, so 10.0 becomes "10.0" (scale 1) and 1e8 becomes "1.0E+8".
func DecimalFromFloat64(f float64) Decimal {
	if math.IsNaN(f) || math.IsInf(f, 0) {
		panic(fmt.Sprintf("databin: cannot convert %v to a decimal", f))
	}
	d, ok := parseJavaBigDecimal(javaDoubleToString(f))
	if !ok {
		panic("databin: internal float formatting error")
	}
	return d
}

// javaDoubleToString renders f the way java.lang.Double.toString does
// (shortest round-tripping digits, plain notation for 1e-3 <= |f| < 1e7 and
// computerized scientific notation otherwise, always with a fraction digit).
func javaDoubleToString(f float64) string {
	if f == 0 {
		if math.Signbit(f) {
			return "-0.0"
		}
		return "0.0"
	}
	sign := ""
	if f < 0 {
		sign = "-"
		f = -f
	}
	e := strconv.FormatFloat(f, 'e', -1, 64)
	if !strings.Contains(e, ".") {
		// When the shortest form has one digit, Java chooses the closest
		// decimal of one or two digits (this only differs for subnormals,
		// e.g. 4.9E-324 rather than 5.0E-324).
		if two := strconv.FormatFloat(f, 'e', 1, 64); mustParseFloat(two) == f && two[2] != '0' {
			e = two
		}
	}
	mant, expStr, _ := strings.Cut(e, "e")
	exp, _ := strconv.Atoi(expStr)
	digits := strings.Replace(mant, ".", "", 1)
	if f >= 1e-3 && f < 1e7 {
		if exp >= 0 {
			for len(digits) < exp+1 {
				digits += "0"
			}
			intPart := digits[:exp+1]
			frac := digits[exp+1:]
			if frac == "" {
				frac = "0"
			}
			return sign + intPart + "." + frac
		}
		return sign + "0." + strings.Repeat("0", -exp-1) + digits
	}
	frac := digits[1:]
	if frac == "" {
		frac = "0"
	}
	return sign + digits[:1] + "." + frac + "E" + strconv.Itoa(exp)
}

func mustParseFloat(s string) float64 {
	f, err := strconv.ParseFloat(s, 64)
	if err != nil {
		panic(err)
	}
	return f
}

// String renders d exactly like BigDecimal.toString.
func (d Decimal) String() string {
	u := d.unscaled()
	if d.scale == 0 {
		return u.String()
	}
	coeff := new(big.Int).Abs(u).String()
	neg := u.Sign() < 0
	var buf strings.Builder
	if neg {
		buf.WriteByte('-')
	}
	adjusted := -int64(d.scale) + int64(len(coeff)-1)
	if d.scale > 0 && adjusted >= -6 {
		pad := int(d.scale) - len(coeff)
		if pad >= 0 {
			buf.WriteString("0.")
			buf.WriteString(strings.Repeat("0", pad))
			buf.WriteString(coeff)
		} else {
			buf.WriteString(coeff[:-pad])
			buf.WriteByte('.')
			buf.WriteString(coeff[-pad:])
		}
		return buf.String()
	}
	buf.WriteString(coeff[:1])
	if len(coeff) > 1 {
		buf.WriteByte('.')
		buf.WriteString(coeff[1:])
	}
	buf.WriteByte('E')
	if adjusted >= 0 {
		buf.WriteByte('+')
	}
	buf.WriteString(strconv.FormatInt(adjusted, 10))
	return buf.String()
}

// Float64 returns the nearest float64 (BigDecimal.doubleValue).
func (d Decimal) Float64() float64 {
	f, _ := strconv.ParseFloat(d.String(), 64)
	return f
}

func (d Decimal) float32() float32 {
	f, _ := strconv.ParseFloat(d.String(), 32)
	return float32(f)
}

// MarshalJSON writes the BigDecimal.toString form as a JSON number.
func (d Decimal) MarshalJSON() ([]byte, error) {
	return []byte(d.String()), nil
}

// UnmarshalJSON accepts a JSON number (or a quoted number) and keeps its
// textual scale, as Jackson does when it deserializes a BigDecimal.
func (d *Decimal) UnmarshalJSON(b []byte) error {
	b = bytes.TrimSpace(b)
	if len(b) >= 2 && b[0] == '"' && b[len(b)-1] == '"' {
		b = b[1 : len(b)-1]
	}
	v, err := ParseDecimal(string(b))
	if err != nil {
		return err
	}
	*d = v
	return nil
}

// Cmp compares numerically, ignoring scale (BigDecimal.compareTo).
func (d Decimal) Cmp(o Decimal) int {
	a, b := d.unscaled(), o.unscaled()
	sa, sb := a.Sign(), b.Sign()
	if sa != sb {
		if sa < sb {
			return -1
		}
		return 1
	}
	if sa == 0 {
		return 0
	}
	if d.scale == o.scale {
		return a.Cmp(b)
	}
	if a.IsInt64() && b.IsInt64() {
		if r, ok := cmpInt64Scaled(a.Int64(), d.scale, b.Int64(), o.scale); ok {
			return r
		}
	}
	x, y := alignScales(d, o)
	return x.Cmp(y)
}

func cmpInt64Scaled(a int64, sa int32, b int64, sb int32) (int, bool) {
	diff := int64(sa) - int64(sb)
	if diff < 0 {
		v, ok := mulPow10Int64(a, -diff)
		if !ok {
			return 0, false
		}
		return cmpInt64(v, b), true
	}
	v, ok := mulPow10Int64(b, diff)
	if !ok {
		return 0, false
	}
	return cmpInt64(a, v), true
}

func cmpInt64(a, b int64) int {
	switch {
	case a < b:
		return -1
	case a > b:
		return 1
	}
	return 0
}

func mulPow10Int64(v int64, n int64) (int64, bool) {
	if n > 18 {
		return 0, false
	}
	p := uint64(1)
	for i := int64(0); i < n; i++ {
		p *= 10
	}
	neg := v < 0
	av := uint64(v)
	if neg {
		av = uint64(-v)
		if v == math.MinInt64 {
			return 0, false
		}
	}
	hi, lo := bits.Mul64(av, p)
	if hi != 0 || lo > math.MaxInt64 {
		return 0, false
	}
	if neg {
		return -int64(lo), true
	}
	return int64(lo), true
}

// alignScales returns both unscaled values brought to the larger scale.
func alignScales(d, o Decimal) (*big.Int, *big.Int) {
	a, b := d.unscaled(), o.unscaled()
	switch {
	case d.scale > o.scale:
		b = new(big.Int).Mul(b, pow10(int64(d.scale)-int64(o.scale)))
	case o.scale > d.scale:
		a = new(big.Int).Mul(a, pow10(int64(o.scale)-int64(d.scale)))
	}
	return a, b
}

// identical is BigDecimal.equals: same value and same scale.
func (d Decimal) identical(o Decimal) bool {
	return d.scale == o.scale && d.unscaled().Cmp(o.unscaled()) == 0
}

// decKey identifies a Decimal under BigDecimal.equals/hashCode semantics
// (value and scale).
type decKey struct {
	scale int32
	small int64
	big   string
}

func (d Decimal) key() decKey {
	u := d.unscaled()
	if u.IsInt64() {
		return decKey{scale: d.scale, small: u.Int64()}
	}
	return decKey{scale: d.scale, big: u.String()}
}

// keyString is key rendered as a string.
func (d Decimal) keyString() string {
	return d.unscaled().String() + "e" + strconv.Itoa(int(-d.scale))
}

func (d Decimal) sign() int { return d.unscaled().Sign() }

func (d Decimal) add(o Decimal) Decimal {
	a, b := alignScales(d, o)
	return newDecimal(new(big.Int).Add(a, b), max(d.scale, o.scale))
}

func (d Decimal) sub(o Decimal) Decimal {
	a, b := alignScales(d, o)
	return newDecimal(new(big.Int).Sub(a, b), max(d.scale, o.scale))
}

func (d Decimal) mul(o Decimal) Decimal {
	return newDecimal(new(big.Int).Mul(d.unscaled(), o.unscaled()), d.scale+o.scale)
}

func (d Decimal) abs() Decimal {
	if d.sign() >= 0 {
		return d
	}
	return newDecimal(new(big.Int).Neg(d.unscaled()), d.scale)
}

// maxOf is BigDecimal.max: d when d >= o, otherwise o.
func (d Decimal) maxOf(o Decimal) Decimal {
	if d.Cmp(o) >= 0 {
		return d
	}
	return o
}

// minOf is BigDecimal.min: d when d <= o, otherwise o.
func (d Decimal) minOf(o Decimal) Decimal {
	if d.Cmp(o) <= 0 {
		return d
	}
	return o
}

// precision is the number of digits of the unscaled value (1 for zero).
func (d Decimal) precision() int {
	u := d.unscaled()
	if u.Sign() == 0 {
		return 1
	}
	return len(new(big.Int).Abs(u).String())
}

// stripTrailingZeros mirrors BigDecimal.stripTrailingZeros (zero becomes 0
// with scale 0).
func (d Decimal) stripTrailingZeros() Decimal {
	return d.stripZerosToScale(math.MinInt32)
}

// stripZerosToScale removes trailing zeros while the scale is above
// preferred.
func (d Decimal) stripZerosToScale(preferred int32) Decimal {
	u := d.unscaled()
	if u.Sign() == 0 {
		if preferred == math.MinInt32 {
			return newDecimal(big.NewInt(0), 0)
		}
		return d
	}
	u = new(big.Int).Set(u)
	scale := d.scale
	q, r := new(big.Int), new(big.Int)
	for scale > preferred {
		q.QuoRem(u, bigTen, r)
		if r.Sign() != 0 {
			break
		}
		u.Set(q)
		scale--
	}
	return newDecimal(u, scale)
}

type roundingMode int

const (
	roundHalfUp roundingMode = iota
	roundCeiling
	roundFloor
	roundDown
)

// setScale mirrors BigDecimal.setScale(newScale, mode).
func (d Decimal) setScale(newScale int32, mode roundingMode) Decimal {
	u := d.unscaled()
	if newScale >= d.scale {
		return newDecimal(new(big.Int).Mul(u, pow10(int64(newScale)-int64(d.scale))), newScale)
	}
	div := pow10(int64(d.scale) - int64(newScale))
	q, r := new(big.Int).QuoRem(u, div, new(big.Int))
	if r.Sign() != 0 {
		inc := false
		switch mode {
		case roundHalfUp:
			twice := new(big.Int).Abs(r)
			twice.Lsh(twice, 1)
			inc = twice.Cmp(div) >= 0
		case roundCeiling:
			inc = r.Sign() > 0
		case roundFloor:
			inc = r.Sign() < 0
		}
		if inc {
			if r.Sign() > 0 {
				q.Add(q, bigOne)
			} else {
				q.Sub(q, bigOne)
			}
		}
	}
	return newDecimal(q, newScale)
}

// truncInt returns the integral part, truncated toward zero.
func (d Decimal) truncInt() *big.Int {
	if d.scale <= 0 {
		return new(big.Int).Mul(d.unscaled(), pow10(-int64(d.scale)))
	}
	return new(big.Int).Quo(d.unscaled(), pow10(int64(d.scale)))
}

// longValue mirrors BigDecimal.longValue (truncation, low-order 64 bits).
func (d Decimal) longValue() int64 {
	t := d.truncInt()
	if t.IsInt64() {
		return t.Int64()
	}
	m := new(big.Int).And(t, new(big.Int).SetUint64(math.MaxUint64))
	return int64(m.Uint64())
}

// intValue mirrors BigDecimal.intValue (truncation, low-order 32 bits).
func (d Decimal) intValue() int {
	return int(int32(d.longValue()))
}

var errNonTerminating = errors.New("databin: non-terminating decimal expansion; no exact representable decimal result")

// divExact mirrors BigDecimal.divide(BigDecimal): the exact quotient with the
// preferred scale d.scale - o.scale, or the smallest larger scale that
// represents it exactly.
func (d Decimal) divExact(o Decimal) Decimal {
	if o.sign() == 0 {
		panic(javaError("ArithmeticException: Division by zero"))
	}
	preferred := int64(d.scale) - int64(o.scale)
	num := new(big.Int).Set(d.unscaled())
	den := new(big.Int).Set(o.unscaled())
	if den.Sign() < 0 {
		num.Neg(num)
		den.Neg(den)
	}
	if num.Sign() == 0 {
		return newDecimal(big.NewInt(0), clampScale(preferred))
	}
	g := new(big.Int).GCD(nil, nil, new(big.Int).Abs(num), den)
	num.Quo(num, g)
	den.Quo(den, g)
	twos, fives := 0, 0
	r := new(big.Int)
	two, five := big.NewInt(2), big.NewInt(5)
	rest := new(big.Int).Set(den)
	for {
		q, m := new(big.Int).QuoRem(rest, two, r)
		if m.Sign() != 0 {
			break
		}
		rest = q
		twos++
	}
	for {
		q, m := new(big.Int).QuoRem(rest, five, r)
		if m.Sign() != 0 {
			break
		}
		rest = q
		fives++
	}
	if rest.Cmp(bigOne) != 0 {
		panic(javaError("ArithmeticException: " + errNonTerminating.Error()))
	}
	k := max(twos, fives)
	factor := new(big.Int).Quo(pow10(int64(k)), den)
	u := new(big.Int).Mul(num, factor)
	res := newDecimal(u, clampScale(preferred+int64(k)))
	return res.stripZerosToScale(clampScale(preferred))
}

func clampScale(s int64) int32 {
	if s > math.MaxInt32 {
		return math.MaxInt32
	}
	if s < math.MinInt32 {
		return math.MinInt32
	}
	return int32(s)
}

// rem mirrors BigDecimal.remainder: d - d.divideToIntegralValue(o) * o.
func (d Decimal) rem(o Decimal) Decimal {
	if o.sign() == 0 {
		panic(javaError("ArithmeticException: Division by zero"))
	}
	preferred := clampScale(int64(d.scale) - int64(o.scale))
	a, b := alignScales(d, o)
	q := new(big.Int).Quo(a, b)
	var qd Decimal
	if q.Sign() == 0 {
		qd = newDecimal(q, preferred)
	} else {
		qd = newDecimal(q, 0).stripZerosToScale(preferred)
		if qd.scale < preferred {
			qd = qd.setScale(preferred, roundDown)
		}
	}
	return d.sub(qd.mul(o))
}
