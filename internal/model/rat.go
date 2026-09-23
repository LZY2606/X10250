package model

import (
	"database/sql/driver"
	"errors"
	"fmt"
	"strings"
)

// Rat is an exact rational number, rendered canonically as "n/d" (or "n").
// It exists so original edge offsets can survive normalization without
// floating point drift and can be used as stable map keys.
type Rat struct {
	num int64
	den int64 // always positive; den==0 with num==0 means zero
}

// NewRat constructs a reduced rational.
func NewRat(n, d int64) (Rat, error) {
	if d == 0 {
		if n == 0 {
			return Rat{}, nil
		}
		return Rat{}, errors.New("rational with zero denominator")
	}
	if d < 0 {
		n, d = -n, -d
	}
	g := bgcd(abs64(n), d)
	return Rat{num: n / g, den: d / g}, nil
}

// MustRat panics on a zero denominator; convenient for tests/builders.
func MustRat(n, d int64) Rat {
	r, err := NewRat(n, d)
	if err != nil {
		panic(err)
	}
	return r
}

// RatInt returns n as a rational.
func RatInt(n int64) Rat { return Rat{num: n, den: 1} }

func bgcd(a, b int64) int64 {
	for b != 0 {
		a, b = b, a%b
	}
	if a == 0 {
		return 1
	}
	return a
}

func abs64(x int64) int64 {
	if x < 0 {
		return -x
	}
	return x
}

// ParseRat parses the canonical "n/d" form.
func ParseRat(s string) (Rat, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return Rat{}, errors.New("empty rational")
	}
	if i := strings.IndexByte(s, '/'); i >= 0 {
		var n, d int64
		if _, err := fmt.Sscanf(s[:i], "%d", &n); err != nil {
			return Rat{}, err
		}
		if _, err := fmt.Sscanf(s[i+1:], "%d", &d); err != nil {
			return Rat{}, err
		}
		return NewRat(n, d)
	}
	var n int64
	if _, err := fmt.Sscanf(s, "%d", &n); err != nil {
		return Rat{}, err
	}
	return Rat{num: n, den: 1}, nil
}

// Num returns the numerator.
func (r Rat) Num() int64 { return r.num }

// Den returns the (positive) denominator.
func (r Rat) Den() int64 {
	if r.den == 0 {
		return 1
	}
	return r.den
}

// IsZero reports whether the value is zero.
func (r Rat) IsZero() bool { return r.num == 0 }

func (r Rat) String() string {
	if r.den <= 1 {
		return fmt.Sprintf("%d", r.num)
	}
	return fmt.Sprintf("%d/%d", r.num, r.den)
}

// Float returns a float approximation for rendering only.
func (r Rat) Float() float64 {
	return float64(r.Num()) / float64(r.Den())
}

// Add returns r+o.
func (r Rat) Add(o Rat) Rat {
	return MustRat(r.Num()*o.Den()+o.Num()*r.Den(), r.Den()*o.Den())
}

// Sub returns r-o.
func (r Rat) Sub(o Rat) Rat {
	return MustRat(r.Num()*o.Den()-o.Num()*r.Den(), r.Den()*o.Den())
}

// Cmp compares two rationals.
func (r Rat) Cmp(o Rat) int {
	l := r.Num() * o.Den()
	right := o.Num() * r.Den()
	switch {
	case l < right:
		return -1
	case l > right:
		return 1
	default:
		return 0
	}
}

// MarshalText implements encoding.TextMarshaler.
func (r Rat) MarshalText() ([]byte, error) { return []byte(r.String()), nil }

// UnmarshalText implements encoding.TextUnmarshaler.
func (r *Rat) UnmarshalText(b []byte) error {
	v, err := ParseRat(string(b))
	if err != nil {
		return err
	}
	*r = v
	return nil
}

// Value implements driver.Valuer.
func (r Rat) Value() (driver.Value, error) { return r.String(), nil }

// Scan implements sql.Scanner.
func (r *Rat) Scan(src any) error {
	switch v := src.(type) {
	case string:
		x, err := ParseRat(v)
		if err != nil {
			return err
		}
		*r = x
	case []byte:
		x, err := ParseRat(string(v))
		if err != nil {
			return err
		}
		*r = x
	case int64:
		*r = RatInt(v)
	default:
		return fmt.Errorf("cannot scan %T into Rat", src)
	}
	return nil
}

// Mul returns r*o.
func (r Rat) Mul(o Rat) Rat {
	return MustRat(r.Num()*o.Num(), r.Den()*o.Den())
}
