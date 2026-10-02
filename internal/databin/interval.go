package databin

// bound is one side of an interval; an unset bound is unbounded.
type bound struct {
	set    bool
	closed bool
	v      Decimal
}

// interval mirrors the subset of Guava Range<BigDecimal> that the binning
// code uses: contains, encloses and endpoint access. Constructing an invalid
// interval (lower above upper, or open(a, a)) raises the same error Guava
// does.
type interval struct {
	lo, hi bound
}

func newInterval(lo, hi bound) *interval {
	if lo.set && hi.set {
		c := lo.v.Cmp(hi.v)
		// Guava compares cuts: closed lower a is below open lower a, and
		// open upper a is below closed upper a.
		if c > 0 || (c == 0 && !lo.closed && !hi.closed) {
			panic(javaError("IllegalArgumentException: Invalid range: " + describe(lo, hi)))
		}
	}
	return &interval{lo: lo, hi: hi}
}

func describe(lo, hi bound) string {
	s := "(-∞"
	if lo.set {
		if lo.closed {
			s = "[" + lo.v.String()
		} else {
			s = "(" + lo.v.String()
		}
	}
	s += ".."
	if hi.set {
		if hi.closed {
			s += hi.v.String() + "]"
		} else {
			s += hi.v.String() + ")"
		}
	} else {
		s += "+∞)"
	}
	return s
}

func closedRange(a, b Decimal) *interval {
	return newInterval(bound{set: true, closed: true, v: a}, bound{set: true, closed: true, v: b})
}

func closedOpenRange(a, b Decimal) *interval {
	return newInterval(bound{set: true, closed: true, v: a}, bound{set: true, closed: false, v: b})
}

func (r *interval) contains(x Decimal) bool {
	if r.lo.set {
		c := x.Cmp(r.lo.v)
		if c < 0 || (c == 0 && !r.lo.closed) {
			return false
		}
	}
	if r.hi.set {
		c := x.Cmp(r.hi.v)
		if c > 0 || (c == 0 && !r.hi.closed) {
			return false
		}
	}
	return true
}

func (r *interval) encloses(o *interval) bool {
	if r.lo.set {
		if !o.lo.set {
			return false
		}
		c := r.lo.v.Cmp(o.lo.v)
		if c > 0 || (c == 0 && !r.lo.closed && o.lo.closed) {
			return false
		}
	}
	if r.hi.set {
		if !o.hi.set {
			return false
		}
		c := r.hi.v.Cmp(o.hi.v)
		if c < 0 || (c == 0 && !r.hi.closed && o.hi.closed) {
			return false
		}
	}
	return true
}

// isEmpty is true for closedOpen(a, a) and openClosed(a, a).
func (r *interval) isEmpty() bool {
	return r.lo.set && r.hi.set && r.lo.v.Cmp(r.hi.v) == 0 && r.lo.closed != r.hi.closed
}

func (r *interval) hasLowerBound() bool { return r.lo.set }
func (r *interval) hasUpperBound() bool { return r.hi.set }
func (r *interval) lowerEndpoint() Decimal {
	if !r.lo.set {
		panic(javaError("IllegalStateException: range unbounded on this side"))
	}
	return r.lo.v
}
func (r *interval) upperEndpoint() Decimal {
	if !r.hi.set {
		panic(javaError("IllegalStateException: range unbounded on this side"))
	}
	return r.hi.v
}

// key identifies an interval under Guava Range equals/hashCode semantics
// combined with BigDecimal hashing (scale-sensitive endpoints).
func (r *interval) key() string {
	s := "-inf"
	if r.lo.set {
		if r.lo.closed {
			s = "[" + r.lo.v.keyString()
		} else {
			s = "(" + r.lo.v.keyString()
		}
	}
	s += ","
	if r.hi.set {
		if r.hi.closed {
			s += r.hi.v.keyString() + "]"
		} else {
			s += r.hi.v.keyString() + ")"
		}
	} else {
		s += "+inf"
	}
	return s
}

// calcRangeBounds mirrors DataBinHelper.calcRange(start, startInclusive, end,
// endInclusive); it returns nil when both endpoints are absent.
func calcRangeBounds(start *Decimal, startInclusive bool, end *Decimal, endInclusive bool) *interval {
	if start == nil && end == nil {
		return nil
	}
	var lo, hi bound
	if start != nil {
		lo = bound{set: true, closed: startInclusive, v: *start}
	}
	if end != nil {
		hi = bound{set: true, closed: endInclusive, v: *end}
	}
	return newInterval(lo, hi)
}
