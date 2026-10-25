package storage

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os/exec"
	"path/filepath"
	"testing"
)

// Both branches consume the same judged minute timeline: the Server's right
// calculation and the real card blocks passed to the production JS view.
func TestQualityTCPRightMatchesFrontendFromSameMinutes(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("Node.js is required to execute the production tcpRightView")
	}
	type fixture struct {
		Name  string           `json:"name"`
		Lane  QualityCardLane  `json:"lane"`
		Right QualityCardRight `json:"right"`
	}
	var fixtures []fixture
	const startMinute = int64(6)
	const windowStart = startMinute * qualityMinuteMS
	for _, cutoff := range []int64{64, 65, 66} { // partial final block (1/2 minutes), and a full block
		for _, samples := range []struct{ judged, failed int }{
			{0, 0}, {1, 1}, {2, 1}, {3, 1}, {9, 1}, {10, 3}, {10, 0}, {100, 100}, {101, 1},
		} {
			name := fmt.Sprintf("cutoff=%d/judged=%d/failed=%d", cutoff, samples.judged, samples.failed)
			frame := qualityLaneFrame{SlotRevision: 2, IntervalStart: windowStart, HasInterval: true, ThresholdMS: 500, TCP: true}
			bucket := func(judged, failed int) qualityBucketData {
				b := qualityNewBucket()
				b.ConfigMin, b.ConfigMax, b.SlotMin, b.SlotMax = 1, 1, 2, 2
				b.IntervalMin, b.IntervalMax = i30, i30
				b.Stats.Count, b.Stats.ConnectAttempts = judged, judged
				b.Stats.ConnectSuccesses, b.Stats.ValidRTTs = judged-failed, judged-failed
				if judged > failed {
					b.Histogram[200] = uint32(judged - failed) // 50 ms, below the slow threshold
				}
				return b
			}
			tl := &qualityTimeline{First: 0}
			for m := int64(0); m < 70; m++ {
				var b *qualityBucketData
				put := func(judged, failed int) { value := bucket(judged, failed); b = &value }
				switch m {
				case startMinute:
					put(samples.judged/2, samples.failed/2)
				case cutoff - 1:
					put(samples.judged-samples.judged/2, samples.failed-samples.failed/2)
				case startMinute - 1, cutoff: // before the window / at the unjudged cutoff
					put(99, 99)
				case 9: // known gap
					put(99, 99)
				case 12: // mixed configuration versions
					put(99, 99)
					b.ConfigMax++
				case 15: // obsolete slot revision
					put(99, 99)
					b.SlotMin, b.SlotMax = 1, 1
				}
				tl.Mins = append(tl.Mins, qualityJudgeMinute(frame, m*qualityMinuteMS, b, m == 9))
			}
			evalEnd := cutoff * qualityMinuteMS
			right := qualityTCPRight(tl, windowStart, evalEnd)
			if right.Denominator != samples.judged || right.Numerator != samples.failed {
				t.Fatalf("%s: excluded minutes entered the Server window: %+v", name, right)
			}
			lane := QualityCardLane{TCP: QualityCardTCPLane{Status: "active"}}
			for i := 0; i < QualityCardBlocks; i++ {
				lane.TCP.Blocks = append(lane.TCP.Blocks, tl.classifyBlock(windowStart+int64(i)*QualityBlockMS, evalEnd, i30))
			}
			fixtures = append(fixtures, fixture{Name: name, Lane: lane, Right: right})
		}
	}
	input, err := json.Marshal(fixtures)
	if err != nil {
		t.Fatal(err)
	}
	core, err := filepath.Abs(filepath.Join("..", "..", "web", "static", "network-quality-core.js"))
	if err != nil {
		t.Fatal(err)
	}
	const check = `
const assert=require('node:assert/strict'),fs=require('node:fs');
const {tcpRightView}=require(process.argv[1]);
const fixtures=JSON.parse(fs.readFileSync(0,'utf8')),classes=new Set();
for(const {name,lane,right} of fixtures){
 const view=tcpRightView(lane),blocks=lane.tcp.blocks;
 const judged=blocks.reduce((n,b)=>n+b.judged,0),failed=blocks.reduce((n,b)=>n+b.failed,0);
 assert.equal(judged,right.denominator,name+' denominator');
 assert.equal(failed,right.numerator,name+' numerator');
 const percent=judged?failed/judged*100:null;
 assert.equal(percent===null,right.percent==null,name+' percentage availability');
 // Go multiplies before dividing; JS divides first. Allow only floating-point
 // roundoff here; the actual displayed percentage below must match exactly.
 if(percent!==null)assert.ok(Math.abs(percent-right.percent)<=Number.EPSILON*Math.max(1,Math.abs(right.percent)),name+' unrounded percentage');
 classes.add(right.samples);
 if(right.samples==='none'){
  assert.equal(view.text,'—',name+' samples below minimum');
  assert.match(view.title,/样本太少/,name);
 }else{
  const value=Number.isInteger(right.percent)?String(right.percent):right.percent.toFixed(1);
  assert.equal(view.text,'失败 '+value+'%',name+' displayed percentage');
  assert.equal(view.title.includes('样本较少'),right.samples==='few',name+' few/ok sample judgement');
 }
}
assert.deepEqual([...classes].sort(),['few','none','ok']);
process.stdout.write('Verified '+fixtures.length+' shared-minute fixtures (none/few/ok).');
`
	cmd := exec.Command(node, "-e", check, core)
	cmd.Stdin = bytes.NewReader(input)
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("production tcpRightView disagrees with qualityTCPRight: %v\n%s", err, output)
	}
	t.Log(string(output))
}
