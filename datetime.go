package etf

import (
	"fmt"
	"math"
	"time"
)

// DateTime represents an Erlang calendar:datetime() tuple:
// {{year(), month(), day()}, {hour(), minute(), second()}}.
//
// Validation enforces Erlang calendar:valid_date/3 and calendar:time() bounds:
//   - Year >= 0
//   - Month in 1..12
//   - Day in 1..daysInMonth(Year, Month) (Gregorian leap-year aware)
//   - Hour in 0..23
//   - Minute in 0..59
//   - Second in 0..59
type DateTime struct {
	Year   int
	Month  int
	Day    int
	Hour   int
	Minute int
	Second int
}

var (
	_ Marshaler   = DateTime{}
	_ Unmarshaler = (*DateTime)(nil)
	_ Marshaler   = Tuple(nil)
	_ Unmarshaler = (*Tuple)(nil)
)

// DateTimeFromTime constructs a DateTime from the date and clock components of t
// in t's location (call t.UTC() first for Erlang universal_time()).
func DateTimeFromTime(t time.Time) DateTime {
	y, m, d := t.Date()
	h, min, s := t.Clock()
	return DateTime{
		Year:   y,
		Month:  int(m),
		Day:    d,
		Hour:   h,
		Minute: min,
		Second: s,
	}
}

// Time validates dt and converts it to a time.Time in loc (defaults to time.UTC if loc is nil).
func (dt DateTime) Time(loc *time.Location) (time.Time, error) {
	if err := dt.Validate(); err != nil {
		return time.Time{}, err
	}
	if loc == nil {
		loc = time.UTC
	}
	return time.Date(dt.Year, time.Month(dt.Month), dt.Day, dt.Hour, dt.Minute, dt.Second, 0, loc), nil
}

// Validate checks that all entries of dt form a valid Erlang calendar:datetime().
func (dt DateTime) Validate() error {
	if dt.Year < 0 {
		return fmt.Errorf("%w: year %d must be >= 0", ErrInvalidDateTime, dt.Year)
	}
	if dt.Month < 1 || dt.Month > 12 {
		return fmt.Errorf("%w: month %d out of range 1..12", ErrInvalidDateTime, dt.Month)
	}
	maxDay := daysInMonth(dt.Year, dt.Month)
	if dt.Day < 1 || dt.Day > maxDay {
		return fmt.Errorf("%w: day %d out of range 1..%d for %04d-%02d", ErrInvalidDateTime, dt.Day, maxDay, dt.Year, dt.Month)
	}
	if dt.Hour < 0 || dt.Hour > 23 {
		return fmt.Errorf("%w: hour %d out of range 0..23", ErrInvalidDateTime, dt.Hour)
	}
	if dt.Minute < 0 || dt.Minute > 59 {
		return fmt.Errorf("%w: minute %d out of range 0..59", ErrInvalidDateTime, dt.Minute)
	}
	if dt.Second < 0 || dt.Second > 59 {
		return fmt.Errorf("%w: second %d out of range 0..59", ErrInvalidDateTime, dt.Second)
	}
	return nil
}

// String formats dt as "YYYY-MM-DD HH:MM:SS".
func (dt DateTime) String() string {
	return fmt.Sprintf("%04d-%02d-%02d %02d:%02d:%02d", dt.Year, dt.Month, dt.Day, dt.Hour, dt.Minute, dt.Second)
}

// MarshalETF validates dt and encodes it as {{Year, Month, Day}, {Hour, Minute, Second}}.
func (dt DateTime) MarshalETF() ([]byte, error) {
	if err := dt.Validate(); err != nil {
		return nil, err
	}
	var w wireWriter
	w.writeByte(VersionMagic)
	// Outer 2-tuple: {Date, Time}
	w.buf = append(w.buf, TagSmallTuple, 2)
	// Date 3-tuple: {Year, Month, Day}
	w.buf = append(w.buf, TagSmallTuple, 3)
	w.writeInt64(int64(dt.Year))
	w.writeInt64(int64(dt.Month))
	w.writeInt64(int64(dt.Day))
	// Time 3-tuple: {Hour, Minute, Second}
	w.buf = append(w.buf, TagSmallTuple, 3)
	w.writeInt64(int64(dt.Hour))
	w.writeInt64(int64(dt.Minute))
	w.writeInt64(int64(dt.Second))
	return w.bytes(), nil
}

// UnmarshalETF decodes and validates an Erlang datetime tuple {{Y, M, D}, {H, Min, S}} into *dt.
func (dt *DateTime) UnmarshalETF(data []byte) error {
	if dt == nil {
		return ErrInvalidTarget
	}
	payload, err := unwrapETFPayload(data, defaultMaxUncompressedSize)
	if err != nil {
		return err
	}
	r := newWireReader(payload)
	parsed, err := readDateTimeWire(r)
	if err != nil {
		return err
	}
	if r.remaining() != 0 {
		return ErrTrailingBytes
	}
	*dt = parsed
	return nil
}

func isLeapYear(year int) bool {
	return year%4 == 0 && (year%100 != 0 || year%400 == 0)
}

func daysInMonth(year, month int) int {
	switch month {
	case 1, 3, 5, 7, 8, 10, 12:
		return 31
	case 4, 6, 9, 11:
		return 30
	case 2:
		if isLeapYear(year) {
			return 29
		}
		return 28
	default:
		return 0
	}
}

func readDateTimeWire(r *wireReader) (DateTime, error) {
	tag, err := r.readByte()
	if err != nil {
		return DateTime{}, err
	}
	if tagTable[tag] != tagTuple {
		return DateTime{}, fmt.Errorf("%w: expected tuple for datetime, got tag %d", ErrTypeMismatch, tag)
	}
	arity, err := r.readTupleHeader(tag)
	if err != nil {
		return DateTime{}, err
	}
	if arity != 2 {
		return DateTime{}, fmt.Errorf("%w: expected 2-tuple {{Y,M,D},{H,Min,S}}, got arity %d", ErrInvalidDateTime, arity)
	}

	y, m, d, err := readDateTimeTriple(r, "date")
	if err != nil {
		return DateTime{}, err
	}
	h, min, s, err := readDateTimeTriple(r, "time")
	if err != nil {
		return DateTime{}, err
	}

	dt := DateTime{
		Year:   y,
		Month:  m,
		Day:    d,
		Hour:   h,
		Minute: min,
		Second: s,
	}
	if err := dt.Validate(); err != nil {
		return DateTime{}, err
	}
	return dt, nil
}

func readDateTimeTriple(r *wireReader, part string) (int, int, int, error) {
	tag, err := r.readByte()
	if err != nil {
		return 0, 0, 0, err
	}
	if tagTable[tag] != tagTuple {
		return 0, 0, 0, fmt.Errorf("%w: %s part must be a 3-tuple, got tag %d", ErrInvalidDateTime, part, tag)
	}
	arity, err := r.readTupleHeader(tag)
	if err != nil {
		return 0, 0, 0, err
	}
	if arity != 3 {
		return 0, 0, 0, fmt.Errorf("%w: %s part must be a 3-tuple, got arity %d", ErrInvalidDateTime, part, arity)
	}
	var vals [3]int
	for i := 0; i < 3; i++ {
		i64, bi, elemTag, err := r.readAnyInteger()
		if err != nil {
			if err == ErrUnexpectedEOF || err == ErrInvalidBignumSign {
				return 0, 0, 0, err
			}
			return 0, 0, 0, fmt.Errorf("%w: %s element %d must be an integer, got tag %d", ErrInvalidDateTime, part, i, elemTag)
		}
		if bi != nil || i64 < math.MinInt || i64 > math.MaxInt {
			return 0, 0, 0, fmt.Errorf("%w: %s element %d integer out of range", ErrInvalidDateTime, part, i)
		}
		vals[i] = int(i64)
	}
	return vals[0], vals[1], vals[2], nil
}

func dateTimeFromTuple(t Tuple) (DateTime, error) {
	if len(t) != 2 {
		return DateTime{}, fmt.Errorf("%w: expected 2-tuple {{Y,M,D},{H,Min,S}}, got arity %d", ErrInvalidDateTime, len(t))
	}
	dateT, ok := t[0].(Tuple)
	if !ok || len(dateT) != 3 {
		return DateTime{}, fmt.Errorf("%w: date part must be a 3-tuple", ErrInvalidDateTime)
	}
	timeT, ok := t[1].(Tuple)
	if !ok || len(timeT) != 3 {
		return DateTime{}, fmt.Errorf("%w: time part must be a 3-tuple", ErrInvalidDateTime)
	}

	var dateVals, timeVals [3]int
	for i := 0; i < 3; i++ {
		v, ok := tupleIntElement(dateT[i])
		if !ok {
			return DateTime{}, fmt.Errorf("%w: date element %d must be an integer", ErrInvalidDateTime, i)
		}
		dateVals[i] = v
	}
	for i := 0; i < 3; i++ {
		v, ok := tupleIntElement(timeT[i])
		if !ok {
			return DateTime{}, fmt.Errorf("%w: time element %d must be an integer", ErrInvalidDateTime, i)
		}
		timeVals[i] = v
	}

	dt := DateTime{
		Year:   dateVals[0],
		Month:  dateVals[1],
		Day:    dateVals[2],
		Hour:   timeVals[0],
		Minute: timeVals[1],
		Second: timeVals[2],
	}
	if err := dt.Validate(); err != nil {
		return DateTime{}, err
	}
	return dt, nil
}

func tupleIntElement(v any) (int, bool) {
	i64, _, _, kind := classifyInt(v)
	if kind != intKindInt64 || i64 < math.MinInt || i64 > math.MaxInt {
		return 0, false
	}
	return int(i64), true
}

func equalDateTimeTuple(dt DateTime, t Tuple) bool {
	if len(t) != 2 {
		return false
	}
	d, ok := t[0].(Tuple)
	if !ok || len(d) != 3 {
		return false
	}
	tm, ok := t[1].(Tuple)
	if !ok || len(tm) != 3 {
		return false
	}
	return Equal(int64(dt.Year), d[0]) &&
		Equal(int64(dt.Month), d[1]) &&
		Equal(int64(dt.Day), d[2]) &&
		Equal(int64(dt.Hour), tm[0]) &&
		Equal(int64(dt.Minute), tm[1]) &&
		Equal(int64(dt.Second), tm[2])
}

func hashDateTime(dt DateTime) uint64 {
	hd := hashByte(fnvOffset64, hashTagTuple)
	hd = hashUint64(hd, hashInt64Val(int64(dt.Year)))
	hd = hashUint64(hd, hashInt64Val(int64(dt.Month)))
	hd = hashUint64(hd, hashInt64Val(int64(dt.Day)))
	hd = hashUint64(hd, 3)

	ht := hashByte(fnvOffset64, hashTagTuple)
	ht = hashUint64(ht, hashInt64Val(int64(dt.Hour)))
	ht = hashUint64(ht, hashInt64Val(int64(dt.Minute)))
	ht = hashUint64(ht, hashInt64Val(int64(dt.Second)))
	ht = hashUint64(ht, 3)

	h := hashByte(fnvOffset64, hashTagTuple)
	h = hashUint64(h, hd)
	h = hashUint64(h, ht)
	return hashUint64(h, 2)
}
