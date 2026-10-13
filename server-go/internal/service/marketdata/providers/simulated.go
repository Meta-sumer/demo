package providers

import (
	"context"
	"math"
	"math/rand/v2"
	"strconv"
	"strings"
	"sync"
	"time"

	"binarytrading/internal/platform/logx"
)

// Simulated is the development / test feed: a random walk per symbol. It
// exists so the platform can be run end to end without a vendor contract - it
// is NOT a price source, and Authoritative is false so the boot check refuses
// it in production unless ALLOW_SIMULATED_FEED_IN_PRODUCTION.
//
// The walk is on the log of the price, with a little momentum so the chart has
// the short runs of a real one, and a pull back towards the symbol's seed price
// (an Ornstein-Uhlenbeck process) so it cannot wander off over hours. Moves are
// sized per symbol to roughly what that market does in a minute. An earlier
// walk reverted only its momentum, not its level: BTC went from 104,250 to
// under 10,000 in a day, and the 24h change read -85%.
type Simulated struct {
	Base
	interval time.Duration
	// Per-tick pull towards the seed, from reversionHalfLife.
	kappa float64

	mu        sync.Mutex
	rng       *rand.Rand
	states    map[string]*symbolState
	connected bool
	stop      chan struct{}
}

type symbolState struct {
	price    float64
	sequence int64
	// Log of the price, and of the seed price it is pulled towards.
	logPrice float64
	anchor   float64
	// This tick's move in log price; carries into the next.
	momentum float64
	// Typical size of one tick's move, as a fraction of the price.
	stepVol float64
}

// How far each market typically moves in a minute, as a fraction of its
// price: crypto most, gold less, currencies least. Lively enough to see on a
// 30-second chart, close enough to real markets that a demo reads as one.
var minuteVol = map[string]float64{
	"btcusdt": 0.0015,
	"ethusdt": 0.0018,
	"xauusd":  0.0006,
	"eurusd":  0.0002,
	"gbpusd":  0.00025,
	"usdjpy":  0.00025,
}

const (
	defaultMinuteVol = 0.0005
	// Share of a tick's move carried into the next.
	momentum = 0.6
	// Time for the pull towards the seed price to halve a deviation. The
	// price then stays within a few percent of its seed (about 2% for BTC,
	// one standard deviation) however long the feed runs.
	reversionHalfLife = 4 * time.Hour
)

var seedPrices = map[string]float64{
	"btcusdt": 104_250,
	"ethusdt": 3_280,
	"eurusd":  1.0842,
	"gbpusd":  1.2684,
	"usdjpy":  155.42,
	"xauusd":  2_654.3,
}

// NewSimulated ticks every interval (default 50ms when 0). The feed-health
// classifier calls a feed degraded once its newest tick is older than
// FEED_HEALTHY_MAX_MS (100ms by default); a slower generator would report
// itself degraded most of the time.
func NewSimulated(interval time.Duration) *Simulated {
	if interval <= 0 {
		interval = 50 * time.Millisecond
	}
	return &Simulated{
		Base:     Base{name: "simulated"},
		interval: interval,
		kappa:    math.Ln2 * float64(interval) / float64(reversionHalfLife),
		rng:      rand.New(rand.NewPCG(uint64(time.Now().UnixNano()), 0x9e3779b97f4a7c15)),
		states:   map[string]*symbolState{},
	}
}

func (s *Simulated) Authoritative() bool { return false }

func (s *Simulated) Connect(context.Context) error {
	s.mu.Lock()
	if s.connected {
		s.mu.Unlock()
		return nil
	}
	s.mu.Unlock()
	s.emitStatus(StateConnecting, "")
	s.mu.Lock()
	s.connected = true
	stop := make(chan struct{})
	s.stop = stop
	s.mu.Unlock()
	go s.run(stop)
	s.emitStatus(StateConnected, "")
	logx.Warn("simulated market feed started - not an authoritative price source")
	return nil
}

func (s *Simulated) run(stop chan struct{}) {
	t := time.NewTicker(s.interval)
	defer t.Stop()
	for {
		select {
		case <-stop:
			return
		case <-t.C:
			s.tick()
		}
	}
}

func (s *Simulated) Disconnect(context.Context) error {
	s.mu.Lock()
	if s.stop != nil {
		close(s.stop)
		s.stop = nil
	}
	s.connected = false
	s.mu.Unlock()
	s.emitStatus(StateDisconnected, "")
	return nil
}

func (s *Simulated) Subscribe(_ context.Context, symbols []string) error {
	s.smu.Lock()
	defer s.smu.Unlock()
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, symbol := range symbols {
		key := strings.ToLower(symbol)
		s.addSymbol(key)
		if _, ok := s.states[key]; !ok {
			seed, ok := seedPrices[key]
			if !ok {
				seed = 100
			}
			vol, ok := minuteVol[key]
			if !ok {
				vol = defaultMinuteVol
			}
			ticksPerMinute := float64(time.Minute) / float64(s.interval)
			s.states[key] = &symbolState{
				price:    seed,
				logPrice: math.Log(seed),
				anchor:   math.Log(seed),
				stepVol:  vol / math.Sqrt(ticksPerMinute),
			}
		}
	}
	return nil
}

func (s *Simulated) Unsubscribe(_ context.Context, symbols []string) error {
	s.smu.Lock()
	defer s.smu.Unlock()
	for _, symbol := range symbols {
		s.removeSymbol(strings.ToLower(symbol))
	}
	return nil
}

func (s *Simulated) IsConnected() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.connected
}

func (s *Simulated) tick() {
	at := time.Now().UnixMilli()
	symbols := s.symbolList()
	ticks := make([]Tick, 0, len(symbols))
	s.mu.Lock()
	for _, symbol := range symbols {
		st, ok := s.states[symbol]
		if !ok {
			continue
		}
		s.step(st)
		st.sequence++

		spread := st.price * 0.0001
		ticks = append(ticks, Tick{
			Symbol:            symbol,
			Price:             roundPrice(st.price),
			Bid:               ptr(roundPrice(st.price - spread/2)),
			Ask:               ptr(roundPrice(st.price + spread/2)),
			ProviderTimestamp: ptr(at),
			Sequence:          ptr(st.sequence),
		})
	}
	s.mu.Unlock()
	for _, t := range ticks {
		s.emitTick(t)
	}
}

// step moves one symbol by one tick. The shock is scaled by (1 - momentum) so
// that, momentum included, a tick moves the price by stepVol on average over
// many ticks. Callers hold s.mu (the generator is not safe for concurrent use).
func (s *Simulated) step(st *symbolState) {
	shock := s.rng.NormFloat64() * st.stepVol * (1 - momentum)
	st.momentum = momentum*st.momentum + shock
	st.logPrice += st.momentum + s.kappa*(st.anchor-st.logPrice)
	st.price = math.Exp(st.logPrice)
}

// roundPrice is Number(value.toFixed(value > 100 ? 2 : 5)).
func roundPrice(v float64) float64 {
	digits := 5
	if v > 100 {
		digits = 2
	}
	out, _ := strconv.ParseFloat(strconv.FormatFloat(v, 'f', digits, 64), 64)
	return out
}
