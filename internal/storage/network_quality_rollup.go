package storage

import (
	"404-probe/internal/protocol"
	"bytes"
	"context"
	"database/sql"
	"encoding/binary"
	"errors"
	"fmt"
	"math"
	"sort"
	"time"
)

const QualityRawRetention = 8 * time.Hour
const QualityReceiptRetention = 48 * time.Hour
const QualityHistoryRetention = 30 * 24 * time.Hour
const QualityHistogramVersion = 1
const QualityHistogramGridMS = .25
const QualityPercentileMaxErrorMS = .125

var qualityResolutions = []int64{60000, 300000, 3600000}
var qualityBucketRetentions = []time.Duration{24 * time.Hour, 7 * 24 * time.Hour, QualityHistoryRetention}

// RTT sum and jitter sum are original Float64 values, never histogram estimates.
type qualityBucketData struct {
	Stats                                                   QualityStatsV2
	RTTSum, JitterSum                                       float64
	Partial, Gaps, IntervalMin, IntervalMax                 int
	Histogram                                               map[uint16]uint32
	FirstID, FirstTime, FirstSeq, LastID, LastTime, LastSeq int64
	PreviousID                                              int64
	IncomingJitter                                          float64
	IncomingPair                                            bool
	ConfigMin, ConfigMax, SlotMin, SlotMax                  int64
}

func qualityNewBucket() qualityBucketData {
	return qualityBucketData{Stats: QualityStatsV2{Categories: map[string]int{}}, Histogram: map[uint16]uint32{}}
}
func qualityEncodeBucket(b qualityBucketData) ([]byte, error) {
	var out bytes.Buffer
	out.WriteByte(QualityHistogramVersion)
	put := func(x int) error {
		if x < 0 {
			return errors.New("negative quality aggregate")
		}
		var buf [10]byte
		n := binary.PutUvarint(buf[:], uint64(x))
		out.Write(buf[:n])
		return nil
	}
	s := b.Stats
	for _, x := range []int{s.Count, s.ValidRTTs, s.ProbeAttempts, s.ProbeSuccesses, s.ConnectAttempts, s.ConnectSuccesses, s.NotExecuted, s.Sent, s.Received, s.JitterPairs, b.Partial, b.Gaps, b.IntervalMin, b.IntervalMax} {
		if err := put(x); err != nil {
			return nil, err
		}
	}
	for _, x := range []float64{b.RTTSum, b.JitterSum} {
		if math.IsNaN(x) || math.IsInf(x, 0) {
			return nil, errors.New("invalid quality sum")
		}
		var buf [8]byte
		binary.LittleEndian.PutUint64(buf[:], math.Float64bits(x))
		out.Write(buf[:])
	}
	keys := make([]string, 0, len(s.Categories))
	for k := range s.Categories {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	if err := put(len(keys)); err != nil {
		return nil, err
	}
	for _, k := range keys {
		if len(k) > 64 {
			return nil, errors.New("invalid quality category")
		}
		_ = put(len(k))
		out.WriteString(k)
		if err := put(s.Categories[k]); err != nil {
			return nil, err
		}
	}
	bins := make([]int, 0, len(b.Histogram))
	for k := range b.Histogram {
		bins = append(bins, int(k))
	}
	sort.Ints(bins)
	_ = put(len(bins))
	for _, k := range bins {
		var buf [6]byte
		binary.LittleEndian.PutUint16(buf[:2], uint16(k))
		binary.LittleEndian.PutUint32(buf[2:], b.Histogram[uint16(k)])
		out.Write(buf[:])
	}
	for _, x := range []int64{b.FirstID, b.FirstTime, b.FirstSeq, b.LastID, b.LastTime, b.LastSeq, b.PreviousID, b.ConfigMin, b.ConfigMax, b.SlotMin, b.SlotMax} {
		if x < 0 {
			return nil, errors.New("invalid quality boundary")
		}
		var buf [10]byte
		n := binary.PutUvarint(buf[:], uint64(x))
		out.Write(buf[:n])
	}
	var incoming [8]byte
	binary.LittleEndian.PutUint64(incoming[:], math.Float64bits(b.IncomingJitter))
	out.Write(incoming[:])
	if b.IncomingPair {
		out.WriteByte(1)
	} else {
		out.WriteByte(0)
	}
	return out.Bytes(), nil
}
func qualityDecodeBucket(wire []byte) (qualityBucketData, error) {
	b := qualityNewBucket()
	r := bytes.NewReader(wire)
	v, err := r.ReadByte()
	if err != nil || v != QualityHistogramVersion {
		return b, errors.New("unsupported quality histogram encoding")
	}
	get := func() (int, error) {
		v, e := binary.ReadUvarint(r)
		if v > math.MaxInt32 {
			return 0, errors.New("quality count exceeds bound")
		}
		return int(v), e
	}
	s := &b.Stats
	for _, p := range []*int{&s.Count, &s.ValidRTTs, &s.ProbeAttempts, &s.ProbeSuccesses, &s.ConnectAttempts, &s.ConnectSuccesses, &s.NotExecuted, &s.Sent, &s.Received, &s.JitterPairs, &b.Partial, &b.Gaps, &b.IntervalMin, &b.IntervalMax} {
		*p, err = get()
		if err != nil {
			return b, err
		}
	}
	for _, p := range []*float64{&b.RTTSum, &b.JitterSum} {
		var raw uint64
		if err = binary.Read(r, binary.LittleEndian, &raw); err != nil {
			return b, err
		}
		*p = math.Float64frombits(raw)
		if math.IsNaN(*p) || math.IsInf(*p, 0) {
			return b, errors.New("invalid quality sum")
		}
	}
	n, err := get()
	if err != nil || n > 64 {
		return b, errors.New("invalid quality categories")
	}
	for i := 0; i < n; i++ {
		size, e := get()
		if e != nil || size > 64 || size > r.Len() {
			return b, errors.New("invalid quality category")
		}
		raw := make([]byte, size)
		_, _ = r.Read(raw)
		count, e := get()
		if e != nil {
			return b, e
		}
		s.Categories[string(raw)] = count
	}
	n, err = get()
	if err != nil || n > 24001 {
		return b, errors.New("invalid quality histogram size")
	}
	total := uint64(0)
	for i := 0; i < n; i++ {
		var k uint16
		var count uint32
		if err = binary.Read(r, binary.LittleEndian, &k); err != nil {
			return b, err
		}
		if err = binary.Read(r, binary.LittleEndian, &count); err != nil {
			return b, err
		}
		if k > 24000 || count == 0 || b.Histogram[k] != 0 {
			return b, errors.New("invalid quality histogram bin")
		}
		b.Histogram[k] = count
		total += uint64(count)
	}
	for _, p := range []*int64{&b.FirstID, &b.FirstTime, &b.FirstSeq, &b.LastID, &b.LastTime, &b.LastSeq, &b.PreviousID, &b.ConfigMin, &b.ConfigMax, &b.SlotMin, &b.SlotMax} {
		v, e := binary.ReadUvarint(r)
		if e != nil || v > math.MaxInt64 {
			return b, errors.New("invalid quality boundary")
		}
		*p = int64(v)
	}
	var incoming uint64
	if err = binary.Read(r, binary.LittleEndian, &incoming); err != nil {
		return b, err
	}
	b.IncomingJitter = math.Float64frombits(incoming)
	flag, e := r.ReadByte()
	if e != nil || flag > 1 || b.IncomingJitter < 0 || math.IsNaN(b.IncomingJitter) || math.IsInf(b.IncomingJitter, 0) {
		return b, errors.New("invalid quality incoming edge")
	}
	b.IncomingPair = flag == 1
	if r.Len() != 0 || total != uint64(s.ValidRTTs) {
		return b, errors.New("quality histogram count mismatch")
	}
	return b, nil
}
func qualityMergeBucket(a *qualityBucketData, b qualityBucketData) {
	if b.IncomingPair && a.LastID != 0 && a.LastID == b.PreviousID && a.LastTime < b.FirstTime {
		a.JitterSum += b.IncomingJitter
		a.Stats.JitterPairs++
	}
	before := func(t, seq, id, otherT, otherSeq, otherID int64) bool {
		return t < otherT || t == otherT && (seq < otherSeq || seq == otherSeq && id < otherID)
	}
	if b.FirstID > 0 && (a.FirstID == 0 || before(b.FirstTime, b.FirstSeq, b.FirstID, a.FirstTime, a.FirstSeq, a.FirstID)) {
		a.FirstID = b.FirstID
		a.FirstTime = b.FirstTime
		a.FirstSeq = b.FirstSeq
		a.PreviousID = b.PreviousID
		a.IncomingJitter = b.IncomingJitter
		a.IncomingPair = b.IncomingPair
	}
	if b.LastID > 0 && (a.LastID == 0 || before(a.LastTime, a.LastSeq, a.LastID, b.LastTime, b.LastSeq, b.LastID)) {
		a.LastID = b.LastID
		a.LastTime = b.LastTime
		a.LastSeq = b.LastSeq
	}
	if b.ConfigMin > 0 && (a.ConfigMin == 0 || b.ConfigMin < a.ConfigMin) {
		a.ConfigMin = b.ConfigMin
	}
	a.ConfigMax = max(a.ConfigMax, b.ConfigMax)
	if b.SlotMin > 0 && (a.SlotMin == 0 || b.SlotMin < a.SlotMin) {
		a.SlotMin = b.SlotMin
	}
	a.SlotMax = max(a.SlotMax, b.SlotMax)
	a.Stats.Count += b.Stats.Count
	a.Stats.ValidRTTs += b.Stats.ValidRTTs
	a.Stats.ProbeAttempts += b.Stats.ProbeAttempts
	a.Stats.ProbeSuccesses += b.Stats.ProbeSuccesses
	a.Stats.ConnectAttempts += b.Stats.ConnectAttempts
	a.Stats.ConnectSuccesses += b.Stats.ConnectSuccesses
	a.Stats.NotExecuted += b.Stats.NotExecuted
	a.Stats.Sent += b.Stats.Sent
	a.Stats.Received += b.Stats.Received
	a.Stats.JitterPairs += b.Stats.JitterPairs
	a.RTTSum += b.RTTSum
	a.JitterSum += b.JitterSum
	a.Partial += b.Partial
	a.Gaps = max(a.Gaps, b.Gaps)
	if b.IntervalMin > 0 && (a.IntervalMin == 0 || b.IntervalMin < a.IntervalMin) {
		a.IntervalMin = b.IntervalMin
	}
	a.IntervalMax = max(a.IntervalMax, b.IntervalMax)
	for k, v := range b.Stats.Categories {
		a.Stats.Categories[k] += v
	}
	for k, v := range b.Histogram {
		a.Histogram[k] += v
	}
}
func qualityBucketStats(b qualityBucketData) QualityStatsV2 {
	s := b.Stats
	s.Completeness = "complete"
	s.ExecutionCoverage = "unverified"
	if s.Count == 0 {
		s.Completeness = "no_data"
	} else if b.Partial > 0 || b.Gaps > 0 {
		s.Completeness = "partial"
	}
	if b.Gaps > 0 {
		s.ExecutionCoverage = "known_gap"
	}
	if s.ValidRTTs > 0 {
		q := func(p float64) float64 { return qualityHistogramQuantile(b.Histogram, s.ValidRTTs, p) }
		a, c := q(.5), q(.95)
		d := c - a
		s.P50 = &a
		s.P95 = &c
		s.Spread = &d
		mean := b.RTTSum / float64(s.ValidRTTs)
		s.Mean = &mean
	}
	if s.JitterPairs > 0 {
		x := b.JitterSum / float64(s.JitterPairs)
		s.Jitter = &x
	}
	if s.ProbeAttempts > 0 {
		x := 100 * float64(s.ProbeSuccesses) / float64(s.ProbeAttempts)
		s.ProbeSuccessPercent = &x
	}
	if s.ConnectAttempts > 0 {
		x := 100 * float64(s.ConnectSuccesses) / float64(s.ConnectAttempts)
		s.ConnectSuccessPercent = &x
	}
	if s.Sent > 0 {
		x := 100 * float64(s.Sent-s.Received) / float64(s.Sent)
		s.PacketLossPercent = &x
	}
	return s
}

func qualityContribution(sample protocol.QualitySample, proto string) (qualityBucketData, error) {
	b := qualityNewBucket()
	b.Stats = qualityStatsV2([]qualityHistoryRow{{sample: sample}}, proto, nil, sample.StartedAt, sample.StartedAt+1)
	if b.Stats.Completeness == "partial" {
		b.Partial = 1
	}
	if b.Stats.Jitter != nil {
		b.JitterSum = *b.Stats.Jitter * float64(b.Stats.JitterPairs)
	}
	values := []float64{}
	d, err := protocol.DecodeQualityDetails(sample.Details)
	if err != nil {
		return b, err
	}
	b.IntervalMin = 30000
	if d != nil && d.IntervalMS > 0 {
		b.IntervalMin = d.IntervalMS
	}
	b.IntervalMax = b.IntervalMin
	if proto == "icmp" && d != nil {
		for _, p := range d.Echoes {
			if p.Status == "reply" && p.RTTMS != nil {
				values = append(values, *p.RTTMS)
			}
		}
	} else if sample.Outcome == "success" && sample.Attempt() && sample.LatencyMS != nil {
		values = append(values, *sample.LatencyMS)
	}
	for _, v := range values {
		// This is also a migration guard. No clamping or silently omitted valid value.
		if v < 0 || v > 6000 || math.IsNaN(v) || math.IsInf(v, 0) {
			return b, fmt.Errorf("RTT %g outside histogram v1 range [0,6000]", v)
		}
		b.RTTSum += v
		b.Histogram[uint16(math.Round(v/QualityHistogramGridMS))]++
	}
	return b, nil
}

func qualityBucketUpdateTx(ctx context.Context, tx *sql.Tx, series, res, t int64, delta qualityBucketData) error {
	t = t / res * res
	b := qualityNewBucket()
	var wire []byte
	err := tx.QueryRowContext(ctx, `SELECT data FROM quality_buckets WHERE series_id=? AND resolution=? AND bucket_start=?`, series, res, t).Scan(&wire)
	if err == nil {
		b, err = qualityDecodeBucket(wire)
	} else if errors.Is(err, sql.ErrNoRows) {
		err = nil
	}
	if err != nil {
		return err
	}
	qualityMergeBucket(&b, delta)
	if b.Stats.JitterPairs == 0 {
		b.JitterSum = 0
	}
	wire, err = qualityEncodeBucket(b)
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO quality_buckets VALUES(?,?,?,?) ON CONFLICT(series_id,resolution,bucket_start) DO UPDATE SET data=excluded.data`, series, res, t, wire)
	return err
}

type qualityFact struct {
	ID, RawID, Series, Session, Seq, Time, Config, Slot int64
	Target, IP                                          string
	Eligible                                            bool
	RTT                                                 sql.NullFloat64
	Interval                                            int64
}

const qualityFactColumns = `id,raw_id,target_id,series_id,session_key,seq,sample_time,config_revision,slot_revision,resolved_ip,eligible,rtt,interval_ms`

func qualityReadFact(row interface{ Scan(...any) error }) (*qualityFact, error) {
	f := &qualityFact{}
	err := row.Scan(&f.ID, &f.RawID, &f.Target, &f.Series, &f.Session, &f.Seq, &f.Time, &f.Config, &f.Slot, &f.IP, &f.Eligible, &f.RTT, &f.Interval)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	return f, err
}
func qualityFactPair(a, b *qualityFact) (float64, bool) {
	if a == nil || b == nil || !a.Eligible || !b.Eligible || !a.RTT.Valid || !b.RTT.Valid || a.Target != b.Target || a.Series != b.Series || a.Session != b.Session || a.Config != b.Config || a.Slot != b.Slot || a.IP != b.IP || b.Time <= a.Time || b.Time-a.Time > b.Interval*3/2 {
		return 0, false
	}
	return math.Abs(a.RTT.Float64 - b.RTT.Float64), true
}
func qualityEdgeTx(ctx context.Context, tx *sql.Tx, a, b *qualityFact, sign int, ignoreGap bool) error {
	diff, ok := qualityFactPair(a, b)
	if !ok {
		return nil
	}
	if !ignoreGap {
		var gap bool
		if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM quality_gaps g JOIN quality_sessions s ON s.agent_id=g.agent_id WHERE s.id=? AND g.start_at<=? AND g.end_at>=? AND g.reason NOT LIKE 'storage_pruned:%')`, b.Session, b.Time, a.Time).Scan(&gap); err != nil {
			return err
		}
		if gap {
			return nil
		}
	}
	for _, res := range qualityResolutions {
		if a.Time/res != b.Time/res {
			if err := qualityIncomingEdgeTx(ctx, tx, b.Series, res, b.Time, a, b, diff, sign); err != nil {
				return err
			}
			continue
		}
		d := qualityNewBucket()
		d.JitterSum = float64(sign) * diff
		d.Stats.JitterPairs = sign
		if err := qualityBucketUpdateTx(ctx, tx, b.Series, res, b.Time, d); err != nil {
			return err
		}
	}
	return nil
}

func qualityIncomingEdgeTx(ctx context.Context, tx *sql.Tx, series, res, t int64, a, b *qualityFact, diff float64, sign int) error {
	var wire []byte
	if err := tx.QueryRowContext(ctx, `SELECT data FROM quality_buckets WHERE series_id=? AND resolution=? AND bucket_start=?`, series, res, t/res*res).Scan(&wire); err != nil {
		return err
	}
	d, err := qualityDecodeBucket(wire)
	if err != nil {
		return err
	}
	if d.FirstID != b.ID {
		return nil
	}
	if sign > 0 {
		d.PreviousID = a.ID
		d.IncomingJitter = diff
		d.IncomingPair = true
	} else if d.PreviousID == a.ID {
		d.PreviousID = 0
		d.IncomingJitter = 0
		d.IncomingPair = false
	}
	wire, err = qualityEncodeBucket(d)
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `UPDATE quality_buckets SET data=? WHERE series_id=? AND resolution=? AND bucket_start=?`, wire, series, res, t/res*res)
	return err
}
func qualityRollupTx(ctx context.Context, tx *sql.Tx, id string, epoch int64, session string, rawID int64, sample protocol.QualitySample, target protocol.QualityTarget, digest []byte, length int, now time.Time) error {
	sem := protocol.QualityV2Semantics
	if sample.Staged() {
		sem = protocol.QualityStagedSemantics
	}
	if _, err := tx.ExecContext(ctx, `INSERT OR IGNORE INTO quality_series(agent_id,target_id,semantics) VALUES(?,?,?)`, id, target.ID, sem); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `INSERT OR IGNORE INTO quality_sessions(agent_id,epoch,session_id) VALUES(?,?,?)`, id, epoch, session); err != nil {
		return err
	}
	var series, sid int64
	if err := tx.QueryRowContext(ctx, `SELECT id FROM quality_series WHERE target_id=? AND semantics=?`, target.ID, sem).Scan(&series); err != nil {
		return err
	}
	if err := tx.QueryRowContext(ctx, `SELECT id FROM quality_sessions WHERE agent_id=? AND epoch=? AND session_id=?`, id, epoch, session).Scan(&sid); err != nil {
		return err
	}
	seq, _ := protocol.QualityInteger(sample.Sequence)
	cfg, _ := protocol.QualityInteger(sample.ConfigRevision)
	slot, _ := protocol.QualityInteger(sample.SlotRevision)
	// Receipt insertion is the single inclusion gate, shared by upload and backfill.
	result, err := tx.ExecContext(ctx, `INSERT OR IGNORE INTO quality_receipts VALUES(?,?,?,?,?,?,?,?)`, sid, seq, series, sample.StartedAt, cfg, slot, digest, length)
	if err != nil {
		return err
	}
	n, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		var oldHash []byte
		var oldLength int
		if err = tx.QueryRowContext(ctx, `SELECT content_hash,canonical_length FROM quality_receipts WHERE session_key=? AND seq=?`, sid, seq).Scan(&oldHash, &oldLength); err != nil {
			return err
		}
		if !bytes.Equal(oldHash, digest) || oldLength != length {
			return errors.New("quality backfill receipt conflict")
		}
		return nil
	}
	contribution, err := qualityContribution(sample, target.Protocol)
	if err != nil {
		return err
	}
	f := &qualityFact{RawID: rawID, Target: target.ID, Series: series, Session: sid, Seq: seq, Time: sample.StartedAt, Config: cfg, Slot: slot, IP: sample.ResolvedIP, Interval: int64(contribution.IntervalMax)}
	f.Eligible = target.Protocol == "tcp" && sample.Outcome == "success" && sample.Attempt() && sample.LatencyMS != nil
	if f.Eligible {
		f.RTT = sql.NullFloat64{Float64: *sample.LatencyMS, Valid: true}
	}
	prev, err := qualityReadFact(tx.QueryRowContext(ctx, `SELECT `+qualityFactColumns+` FROM quality_facts WHERE target_id=? AND (sample_time,seq)<=(?,?) ORDER BY sample_time DESC,seq DESC,id DESC LIMIT 1`, f.Target, f.Time, f.Seq))
	if err != nil {
		return err
	}
	next, err := qualityReadFact(tx.QueryRowContext(ctx, `SELECT `+qualityFactColumns+` FROM quality_facts WHERE target_id=? AND (sample_time,seq)>(?,?) ORDER BY sample_time,seq,id LIMIT 1`, f.Target, f.Time, f.Seq))
	if err != nil {
		return err
	}
	if err = qualityEdgeTx(ctx, tx, prev, next, -1, false); err != nil {
		return err
	}
	factResult, err := tx.ExecContext(ctx, `INSERT INTO quality_facts(raw_id,target_id,series_id,session_key,seq,sample_time,config_revision,slot_revision,resolved_ip,eligible,rtt,interval_ms) VALUES(?,?,?,?,?,?,?,?,?,?,?,?)`, f.RawID, f.Target, f.Series, f.Session, f.Seq, f.Time, f.Config, f.Slot, f.IP, f.Eligible, f.RTT, f.Interval)
	if err != nil {
		return err
	}
	f.ID, err = factResult.LastInsertId()
	if err != nil {
		return err
	}
	contribution.FirstID = f.ID
	contribution.FirstTime = f.Time
	contribution.FirstSeq = f.Seq
	contribution.LastID = f.ID
	contribution.LastTime = f.Time
	contribution.LastSeq = f.Seq
	contribution.ConfigMin = f.Config
	contribution.ConfigMax = f.Config
	contribution.SlotMin = f.Slot
	contribution.SlotMax = f.Slot
	for _, res := range qualityResolutions {
		var gap bool
		start := sample.StartedAt / res * res
		if err = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM quality_gaps WHERE agent_id=? AND start_at<? AND (end_at>? OR (start_at=end_at AND start_at>=?)) AND reason NOT LIKE 'storage_pruned:%')`, id, start+res, start, start).Scan(&gap); err != nil {
			return err
		}
		contribution.Gaps = boolInt(gap)
		if err = qualityBucketUpdateTx(ctx, tx, series, res, sample.StartedAt, contribution); err != nil {
			return err
		}
	}
	if err = qualityEdgeTx(ctx, tx, prev, f, 1, false); err != nil {
		return err
	}
	return qualityEdgeTx(ctx, tx, f, next, 1, false)
}

// qualityHistogramQuantile reads a percentile from a minute histogram: n is the number of valid RTTs, bins are
// grid indexes (QualityHistogramGridMS), and neighbouring ranks are interpolated as (n-1)*p.
func qualityHistogramQuantile(hist map[uint16]uint32, n int, p float64) float64 {
	bins := make([]int, 0, len(hist))
	for k := range hist {
		bins = append(bins, int(k))
	}
	sort.Ints(bins)
	at := func(rank int) float64 {
		for _, k := range bins {
			c := int(hist[uint16(k)])
			if rank < c {
				return float64(k) * QualityHistogramGridMS
			}
			rank -= c
		}
		return 0
	}
	x := float64(n-1) * p
	i := int(x)
	return at(i) + (at(min(i+1, n-1))-at(i))*(x-float64(i))
}

// qualityFiveMinuteP50 merges the minute histograms of the window ending at the current minute and returns their
// median. Only minutes with valid RTTs that sit in the same config and slot revision as the current one take part.
// It is null when the current minute has no valid RTT, is mixed, or the merge holds fewer than three RTTs, so gaps
// stay gaps. window holds the current minute last.
func qualityFiveMinuteP50(window []qualityBucketData) *float64 {
	cur := window[len(window)-1]
	if cur.Stats.ValidRTTs == 0 || cur.ConfigMin != cur.ConfigMax || cur.SlotMin != cur.SlotMax {
		return nil
	}
	merged, n := map[uint16]uint32{}, 0
	for _, b := range window {
		if b.Stats.ValidRTTs == 0 || b.ConfigMin != cur.ConfigMin || b.ConfigMax != cur.ConfigMin || b.SlotMin != cur.SlotMin || b.SlotMax != cur.SlotMin {
			continue
		}
		for k, v := range b.Histogram {
			merged[k] += v
		}
		n += b.Stats.ValidRTTs
	}
	if n < 3 {
		return nil
	}
	v := qualityHistogramQuantile(merged, n, .5)
	return &v
}
