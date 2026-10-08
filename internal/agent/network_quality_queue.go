package agent

import (
	"404-probe/internal/protocol"
	"encoding/json"
	"errors"
	"math"
	"strconv"
	"time"
)

const qualityQueueRows = 6000
const qualityQueueBytes = 2 << 20
const qualityQueueAge = 6 * time.Hour

type qualityRow struct {
	seq   string
	raw   []byte
	added time.Time
	wall  int64
}
type qualityGapRow struct {
	id  uint64
	gap protocol.QualityGap
}
type qualityQueue struct {
	rows                       []qualityRow
	bytes                      int
	sequence                   int64
	gaps                       []qualityGapRow
	gapID                      uint64
	overflowStart, overflowEnd int64
}
type qualityUpload struct {
	batch protocol.QualityBatch
	body  []byte
	gaps  []uint64
}

func (q *qualityQueue) clear() {
	q.rows = nil
	q.gaps = nil
	q.bytes = 0
	q.overflowStart = 0
	q.overflowEnd = 0
	// Sequence never resets during the same session, even after authorization loss.
}
func (q *qualityQueue) addGap(start, end int64, reason string, dropped *int) {
	if start <= 0 || end < start {
		return
	}
	if len(q.gaps) >= 8 {
		if q.overflowStart == 0 || start < q.overflowStart {
			q.overflowStart = start
		}
		if end > q.overflowEnd {
			q.overflowEnd = end
		}
		return // No mutable in-flight gap; collapsed overflow has unknown count.
	}
	q.gapID++
	q.gaps = append(q.gaps, qualityGapRow{q.gapID, protocol.QualityGap{Start: start, End: end, Reason: reason, Dropped: dropped}})
}
func (q *qualityQueue) flushOverflow() {
	if len(q.gaps) < 8 && q.overflowStart > 0 {
		start, end := q.overflowStart, q.overflowEnd
		q.overflowStart = 0
		q.overflowEnd = 0
		q.addGap(start, end, "unknown_gap", nil)
	}
}
func (q *qualityQueue) dropFirst() {
	row := q.rows[0]
	one := 1
	q.addGap(row.wall, row.wall, "queue_drop", &one)
	q.bytes -= len(row.raw)
	copy(q.rows, q.rows[1:])
	q.rows[len(q.rows)-1] = qualityRow{}
	q.rows = q.rows[:len(q.rows)-1]
}
func (q *qualityQueue) expire(now time.Time) {
	for len(q.rows) > 0 && now.Sub(q.rows[0].added) >= qualityQueueAge {
		q.dropFirst()
	}
	// Expired gap metadata is not retryable forever either.
	kept := q.gaps[:0]
	for _, g := range q.gaps {
		if g.gap.End >= now.Add(-qualityQueueAge).UnixMilli() {
			kept = append(kept, g)
		}
	}
	q.gaps = kept
	if q.overflowEnd < now.Add(-qualityQueueAge).UnixMilli() {
		q.overflowStart = 0
		q.overflowEnd = 0
	}
	q.flushOverflow()
}
func (q *qualityQueue) append(sample protocol.QualitySample, now time.Time) error {
	if q.sequence == math.MaxInt64 {
		return errors.New("quality sequence exhausted")
	}
	q.sequence++
	sample.Sequence = strconv.FormatInt(q.sequence, 10)
	raw, err := json.Marshal(sample)
	if err != nil {
		return err
	}
	if len(raw) > qualityQueueBytes {
		return errors.New("quality row exceeds queue")
	}
	q.expire(now)
	for len(q.rows) > 0 && (len(q.rows) >= qualityQueueRows || q.bytes+len(raw) > qualityQueueBytes) {
		q.dropFirst()
	}
	if q.rows == nil {
		q.rows = make([]qualityRow, 0, qualityQueueRows)
	}
	q.rows = append(q.rows, qualityRow{sample.Sequence, raw, now, sample.StartedAt})
	q.bytes += len(raw)
	return nil
}
func (q *qualityQueue) pack(epoch, session string) (qualityUpload, error) {
	u := qualityUpload{batch: protocol.QualityBatch{Version: 1, Epoch: epoch, SessionID: session, Samples: []protocol.QualitySample{}}}
	for _, g := range q.gaps {
		u.batch.Gaps = append(u.batch.Gaps, g.gap)
		u.gaps = append(u.gaps, g.id)
	}
	for _, r := range q.rows {
		if len(u.batch.Samples) == protocol.QualityBatchLimit {
			break
		}
		var sample protocol.QualitySample
		if err := json.Unmarshal(r.raw, &sample); err != nil {
			return u, err
		}
		u.batch.Samples = append(u.batch.Samples, sample)
		body, err := json.Marshal(u.batch)
		if err != nil {
			return u, err
		}
		if len(body) > protocol.QualityBodyLimit {
			u.batch.Samples = u.batch.Samples[:len(u.batch.Samples)-1]
			break
		}
	}
	var err error
	u.body, err = json.Marshal(u.batch)
	if len(u.body) > protocol.QualityBodyLimit {
		return u, errors.New("quality batch exceeds body budget")
	}
	return u, err
}

func qualityAckActions(acks []protocol.QualityAck, seqs []string) (map[int]string, error) {
	actions := map[int]string{}
	for _, ack := range acks {
		i, err := strconv.Atoi(ack[0])
		if err != nil || i < 0 || i >= len(seqs) || strconv.Itoa(i) != ack[0] || ack[1] != seqs[i] {
			return nil, errors.New("quality ACK identity mismatch")
		}
		if _, exists := actions[i]; exists {
			return nil, errors.New("duplicate quality ACK index")
		}
		switch ack[2] {
		case "committed", "duplicate", "invalid", "conflict", "too_old", "clock_skew", "unauthorized", "quota", "storage_pressure":
		default:
			return nil, errors.New("unknown quality ACK status")
		}
		actions[i] = ack[2]
	}
	return actions, nil
}
func qualityAckTerminal(status string) bool { return status != "quota" && status != "storage_pressure" }
func (q *qualityQueue) acknowledge(u qualityUpload, result protocol.QualityBatchResponse, record func(string)) (bool, error) {
	seqs := make([]string, len(u.batch.Samples))
	for i, s := range u.batch.Samples {
		seqs[i] = s.Sequence
	}
	samples, err := qualityAckActions(result.Results, seqs)
	if err != nil {
		return false, err
	}
	gaps, err := qualityAckActions(result.Gaps, make([]string, len(u.gaps)))
	if err != nil {
		return false, err
	}
	remove := map[string]bool{}
	gapRemove := map[uint64]bool{}
	progress := false
	for i, status := range samples {
		if qualityAckTerminal(status) {
			remove[seqs[i]] = true
			progress = true
			if status != "committed" && status != "duplicate" {
				record(status)
			}
		}
	}
	for i, status := range gaps {
		if qualityAckTerminal(status) {
			gapRemove[u.gaps[i]] = true
			progress = true
			if status != "committed" && status != "duplicate" {
				record(status)
			}
		}
	}
	kept := q.rows[:0]
	for _, r := range q.rows {
		if remove[r.seq] {
			q.bytes -= len(r.raw)
		} else {
			kept = append(kept, r)
		}
	}
	for i := len(kept); i < len(q.rows); i++ {
		q.rows[i] = qualityRow{}
	}
	q.rows = kept
	keepGaps := q.gaps[:0]
	for _, g := range q.gaps {
		if !gapRemove[g.id] {
			keepGaps = append(keepGaps, g)
		}
	}
	q.gaps = keepGaps
	q.flushOverflow()
	complete := progress && len(samples) == len(seqs) && len(gaps) == len(u.gaps)
	for _, status := range samples {
		if !qualityAckTerminal(status) {
			complete = false
		}
	}
	for _, status := range gaps {
		if !qualityAckTerminal(status) {
			complete = false
		}
	}
	return complete, nil
}
