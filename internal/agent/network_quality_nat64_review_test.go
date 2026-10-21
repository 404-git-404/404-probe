package agent

import (
	"context"
	"fmt"
	"math/rand"
	"net/netip"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"404-probe/internal/protocol"
)

// A ninth candidate must not push an existing prefix out of the check (R07-C review M1).
func TestNAT64NinthCandidateNeverRemovesAnExistingRefusal(t *testing.T) {
	useRealQualityPolicy(t)
	setNAT64(t)
	var answers []netip.Addr
	var last netip.Prefix
	for i := 1; i <= 8; i++ {
		last = netip.MustParsePrefix(fmt.Sprintf("2606:4700:%x:1234:5678:9abc::/96", i))
		answers = append(answers, nat64Compose(last, nat64WellKnown[0]))
	}
	target := nat64Compose(last, netip.MustParseAddr("10.1.2.3"))
	resolver := &scriptedNAT64{answers: answers}
	if err := nat64Refresh(context.Background(), resolver); err != nil {
		t.Fatal(err)
	}
	if qualityAddressSafe(target) {
		t.Fatal("the eight-prefix fixture did not refuse")
	}
	extra := netip.MustParsePrefix("2606:4700:99:1234:5678:9abc::/96")
	for name, list := range map[string][]netip.Addr{
		"new candidate first":     append([]netip.Addr{nat64Compose(extra, nat64WellKnown[0])}, answers...),
		"new candidate last":      append(append([]netip.Addr{}, answers...), nat64Compose(extra, nat64WellKnown[0])),
		"reordered, target ninth": append(append(append([]netip.Addr{}, answers[:7]...), nat64Compose(extra, nat64WellKnown[0])), answers[7]),
		"duplicates":              append(append([]netip.Addr{}, answers...), answers...),
	} {
		resolver.answers = list
		if err := nat64Refresh(context.Background(), resolver); err != nil {
			t.Fatal(err)
		}
		if qualityAddressSafe(target) {
			t.Errorf("%s: the address that was refused is allowed", name)
		}
	}
}

// Stopping and resuming the Agent (device disabled, then enabled) must bring the discovery back,
// every time, with exactly one loop alive (review M2).
func TestNAT64DiscoveryRestartsAfterEveryStopAndResume(t *testing.T) {
	setNAT64(t)
	q := newQualityControl()
	var loops, calls atomic.Int32
	var mu sync.Mutex
	live := 0
	maxLive := 0
	resolver := countingNAT64{calls: &calls}
	q.nat64Resolver = resolver
	q.nat64Wait = func(time.Duration) <-chan time.Time {
		loops.Add(1)
		mu.Lock()
		live++
		maxLive = max(maxLive, live)
		mu.Unlock()
		return make(chan time.Time) // sleeps until its context ends
	}
	r := &Runner{quality: q}
	config := userConfig(userTarget("telecom", "ipv4", "tcp", "example.com", 443))
	for cycle := 1; cycle <= 3; cycle++ {
		ctx, cancel := context.WithCancel(context.Background())
		r.startNAT64Discovery(ctx, &config)
		r.startNAT64Discovery(ctx, &config) // a second call in the same worker must not start another loop
		waitForAgentCondition(t, fmt.Sprintf("query of cycle %d", cycle), func() bool { return calls.Load() == int32(cycle) })
		cancel()
		time.Sleep(20 * time.Millisecond)
		mu.Lock()
		live = 0
		mu.Unlock()
	}
	if loops.Load() != 3 || maxLive > 3 {
		t.Fatalf("loops=%d", loops.Load())
	}
}

type countingNAT64 struct{ calls *atomic.Int32 }

func (c countingNAT64) LookupNetIP(context.Context, string, string) ([]netip.Addr, error) {
	c.calls.Add(1)
	return nil, nil
}

var _ = protocol.QualityTargetsHeader

// blockingNAT64 holds the first discovery query until it is released.
type blockingNAT64 struct {
	release chan struct{}
	calls   *atomic.Int32
}

func (b blockingNAT64) LookupNetIP(ctx context.Context, _, _ string) ([]netip.Addr, error) {
	b.calls.Add(1)
	select {
	case <-b.release:
		return nil, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

// No user target is probed before the first discovery has completed (maintainer decision on R07-C).
func TestUserTargetsWaitForTheFirstNAT64Discovery(t *testing.T) {
	setNAT64(t)
	server := newQuietServer()
	defer server.Close()
	r := n2bRunner(t, server.URL, "id", "token")
	r.setQualityUserTargets(true)
	release := make(chan struct{})
	var asked, probes atomic.Int32
	r.quality.nat64Resolver = blockingNAT64{release: release, calls: &asked}
	var advance atomic.Int64
	r.quality.now = func() time.Time { return time.Now().Add(time.Duration(advance.Load())) }
	r.quality.random = func() float64 { return .5 }
	r.quality.probe = func(context.Context, protocol.QualityTarget) qualityProbeResult {
		probes.Add(1)
		return qualityProbeResult{outcome: "canceled"}
	}
	c := userConfig(userTarget("telecom", "ipv4", "tcp", "example.com", 443))
	r.observeQualityReport(true, protocol.ReportResponse{Accepted: true, Capabilities: protocol.ReportCapabilities{NetworkQuality: true}, NetworkQuality: &c})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan struct{})
	go func() { r.runQualityWorker(ctx); close(done) }()
	waitForAgentCondition(t, "first discovery query", func() bool { return asked.Load() == 1 })
	for i := 0; i < 4; i++ {
		advance.Add(int64(31 * time.Second))
		r.quality.signal()
		time.Sleep(30 * time.Millisecond)
	}
	if probes.Load() != 0 {
		t.Fatal("a user target was probed before the first discovery finished")
	}
	close(release)
	// Once the answer is in, the lane is scheduled and runs on the normal period.
	waitForAgentCondition(t, "probe after discovery", func() bool {
		advance.Add(int64(31 * time.Second))
		r.quality.signal()
		time.Sleep(10 * time.Millisecond)
		return probes.Load() >= 1
	})
	cancel()
	<-done
}

// More candidates never allow what fewer refused: beyond eight the extra ones refuse everything inside
// them, and far beyond the limit no IPv6 destination passes.
func TestNAT64OverflowOnlyRefuses(t *testing.T) {
	useRealQualityPolicy(t)
	setNAT64(t)
	prefixOf := func(i int) netip.Prefix {
		return netip.MustParsePrefix(fmt.Sprintf("2606:4700:%x:1234:5678:9abc::/96", i))
	}
	answersFor := func(n int) []netip.Addr {
		var out []netip.Addr
		for i := 1; i <= n; i++ {
			out = append(out, nat64Compose(prefixOf(i), nat64WellKnown[0]))
		}
		return out
	}
	// A public IPv4 inside the ninth prefix is fine while it is one of eight, refused once it overflows.
	public := nat64Compose(prefixOf(9), netip.MustParseAddr("8.8.8.8"))
	nat64Snapshot.Store(nat64Discover(answersFor(8)))
	if !qualityAddressSafe(public) {
		t.Fatal("outside every prefix this address is an ordinary public one")
	}
	nat64Snapshot.Store(nat64Discover(answersFor(9)))
	if qualityAddressSafe(public) {
		t.Fatal("an overflow candidate must refuse what lies inside it")
	}
	nat64Snapshot.Store(nat64Discover(answersFor(nat64MaxRefused)))
	if qualityAddressSafe(public) || qualityAddressSafe(nat64Compose(prefixOf(nat64MaxRefused), netip.MustParseAddr("8.8.8.8"))) {
		t.Fatal("the last overflow prefix does not refuse")
	}
	elsewhere := netip.MustParseAddr("2606:4700:4700::1111")
	if !qualityAddressSafe(elsewhere) {
		t.Fatal("an address outside every candidate must stay allowed")
	}
	set := nat64Discover(answersFor(nat64MaxRefused + 1))
	if !set.refuseAll {
		t.Fatal("too many candidates must fail closed")
	}
	nat64Snapshot.Store(set)
	if qualityAddressSafe(elsewhere) || !qualityAddressSafe(netip.MustParseAddr("93.184.216.34")) {
		t.Fatal("fail closed refuses IPv6 and leaves IPv4 alone")
	}
	var many []netip.Addr
	for i := 0; i <= nat64MaxAnswers; i++ {
		many = append(many, netip.MustParseAddr(fmt.Sprintf("2606:4700:%x::1", i+1)))
	}
	if !nat64Discover(many).refuseAll {
		t.Fatal("an enormous answer must fail closed")
	}
}

// Property over the real discovery path: adding answers (new, repeated, reordered) never turns a
// refused address into an allowed one.
func TestNAT64MoreAnswersNeverAllowMore(t *testing.T) {
	useRealQualityPolicy(t)
	setNAT64(t)
	rng := rand.New(rand.NewSource(11))
	prefixOf := func(i int) netip.Prefix {
		return netip.MustParsePrefix(fmt.Sprintf("2606:4700:%x:1234:5678:9abc::/96", i))
	}
	for round := 0; round < 400; round++ {
		n := 1 + rng.Intn(12)
		var base []netip.Addr
		for i := 0; i < n; i++ {
			base = append(base, nat64Compose(prefixOf(1+rng.Intn(20)), nat64WellKnown[rng.Intn(2)]))
		}
		extra := append([]netip.Addr{}, base...)
		for i := rng.Intn(10); i > 0; i-- {
			extra = append(extra, nat64Compose(prefixOf(1+rng.Intn(40)), nat64WellKnown[0]))
		}
		rng.Shuffle(len(extra), func(i, j int) { extra[i], extra[j] = extra[j], extra[i] })
		for probe := 0; probe < 40; probe++ {
			v4 := netip.AddrFrom4([4]byte{byte(rng.Intn(256)), byte(rng.Intn(256)), byte(rng.Intn(256)), byte(rng.Intn(256))})
			addr := nat64Compose(prefixOf(1+rng.Intn(40)), v4)
			nat64Snapshot.Store(nat64Discover(base))
			before := qualityAddressSafe(addr)
			nat64Snapshot.Store(nat64Discover(extra))
			after := qualityAddressSafe(addr)
			if after && !before {
				t.Fatalf("round %d: %v refused with %d answers, allowed with %d", round, addr, len(base), len(extra))
			}
		}
	}
}

// The wait for the first discovery ends at the timeout when the resolver never answers, the targets
// are then probed, and later workers (after a stop and resume) are not held again.
func TestUserTargetsStartAfterTheDiscoveryTimeoutAndAreNotHeldAgain(t *testing.T) {
	setNAT64(t)
	server := newQuietServer()
	defer server.Close()
	r := n2bRunner(t, server.URL, "id", "token")
	r.setQualityUserTargets(true)
	var asked, probes atomic.Int32
	r.quality.nat64Resolver = blockingNAT64{release: make(chan struct{}), calls: &asked} // never answers
	r.quality.nat64Timeout = 150 * time.Millisecond
	var advance atomic.Int64
	r.quality.now = func() time.Time { return time.Now().Add(time.Duration(advance.Load())) }
	r.quality.random = func() float64 { return .5 }
	r.quality.probe = func(context.Context, protocol.QualityTarget) qualityProbeResult {
		probes.Add(1)
		return qualityProbeResult{outcome: "canceled"}
	}
	c := userConfig(userTarget("telecom", "ipv4", "tcp", "example.com", 443))
	r.observeQualityReport(true, protocol.ReportResponse{Accepted: true, Capabilities: protocol.ReportCapabilities{NetworkQuality: true}, NetworkQuality: &c})
	run := func() (context.CancelFunc, chan struct{}) {
		ctx, cancel := context.WithCancel(context.Background())
		done := make(chan struct{})
		go func() { r.runQualityWorker(ctx); close(done) }()
		return cancel, done
	}
	cancel, done := run()
	time.Sleep(60 * time.Millisecond)
	advance.Add(int64(31 * time.Second))
	r.quality.signal()
	time.Sleep(40 * time.Millisecond)
	if probes.Load() != 0 {
		t.Fatal("probed before the timeout")
	}
	time.Sleep(150 * time.Millisecond) // the timeout has passed
	waitForAgentCondition(t, "probe after the timeout", func() bool {
		advance.Add(int64(31 * time.Second))
		r.quality.signal()
		time.Sleep(10 * time.Millisecond)
		return probes.Load() >= 1
	})
	cancel()
	<-done
	// Stop and resume: the new worker starts a new discovery but its targets are not held.
	before := probes.Load()
	cancel, done = run()
	waitForAgentCondition(t, "second discovery loop", func() bool { return asked.Load() >= 2 })
	waitForAgentCondition(t, "probe without a new wait", func() bool {
		advance.Add(int64(31 * time.Second))
		r.quality.signal()
		time.Sleep(10 * time.Millisecond)
		return probes.Load() > before
	})
	cancel()
	<-done
}

// A configuration without user targets is never held by the discovery.
func TestCatalogAndEmptyConfigurationsAreNotHeld(t *testing.T) {
	r := &Runner{quality: newQualityControl()}
	if !r.startNAT64Discovery(context.Background(), nil) {
		t.Fatal("nil configuration held")
	}
	cat := n2bConfig()
	if !r.startNAT64Discovery(context.Background(), &cat) {
		t.Fatal("a catalog configuration was held")
	}
}

// numberedNAT64 answers the n-th question with the n-th prefix.
type numberedNAT64 struct {
	calls  atomic.Int32
	prefix func(n int) netip.Prefix
}

func (r *numberedNAT64) LookupNetIP(context.Context, string, string) ([]netip.Addr, error) {
	n := int(r.calls.Add(1))
	return []netip.Addr{nat64Compose(r.prefix(n), nat64WellKnown[0])}, nil
}

// A loop that has been superseded can pass its cancellation check and still be late to publish. Its
// result must not replace what a newer loop published (R07-C re-review). Loops 1 and 2 are stopped right
// before they publish, loop 3 publishes, and the old ones are then released in both orders; the test waits
// for each late publish to finish instead of sleeping.
func TestSupersededNAT64LoopsNeverPublish(t *testing.T) {
	for _, order := range [][2]int{{1, 2}, {2, 1}} {
		t.Run(fmt.Sprintf("released %d then %d", order[0], order[1]), func(t *testing.T) {
			setNAT64(t)
			prefixOf := func(n int) netip.Prefix {
				return netip.MustParsePrefix(fmt.Sprintf("2606:4700:%x:1234:5678:9abc::/96", n))
			}
			resolver := &numberedNAT64{prefix: prefixOf}
			q := newQualityControl()
			q.nat64Resolver = resolver
			q.nat64Wait = func(time.Duration) <-chan time.Time { return make(chan time.Time) }
			r := &Runner{quality: q}
			config := userConfig(userTarget("telecom", "ipv4", "tcp", "example.com", 443))

			reached := make(chan int, 8)
			finished := make(chan bool, 8)
			release := map[int]chan struct{}{1: make(chan struct{}), 2: make(chan struct{})}
			var hooks atomic.Int32
			nat64PublishHook = func() {
				n := int(hooks.Add(1))
				reached <- n
				if gate := release[n]; gate != nil {
					<-gate
				}
			}
			nat64PublishDoneHook = func(stored bool) { finished <- stored }
			t.Cleanup(func() { nat64PublishHook, nat64PublishDoneHook = nil, nil })

			start := func() context.CancelFunc {
				ctx, cancel := context.WithCancel(context.Background())
				r.startNAT64Discovery(ctx, &config)
				return cancel
			}
			expect := func(want int) {
				t.Helper()
				select {
				case n := <-reached:
					if n != want {
						t.Fatalf("loop %d reached the publish, want %d", n, want)
					}
				case <-time.After(2 * time.Second):
					t.Fatalf("loop %d never reached the publish", want)
				}
			}
			cancel1 := start()
			expect(1)
			cancel1() // stopped (device disabled) while it is about to publish
			cancel2 := start()
			expect(2)
			cancel2() // stopped again before the third resume
			cancel3 := start()
			defer cancel3()
			expect(3)
			select {
			case stored := <-finished: // loop 3
				if !stored {
					t.Fatal("the newest loop did not publish")
				}
			case <-time.After(2 * time.Second):
				t.Fatal("the newest loop never finished")
			}
			for _, n := range order {
				close(release[n])
				select {
				case stored := <-finished:
					if stored {
						t.Fatalf("superseded loop %d published", n)
					}
				case <-time.After(2 * time.Second):
					t.Fatalf("superseded loop %d never finished", n)
				}
			}
			set := nat64Snapshot.Load()
			if set == nil || len(set.checked) != 1 || set.checked[0] != prefixOf(3) {
				t.Fatalf("a superseded loop overwrote the newest result: %+v", set)
			}
		})
	}
}
