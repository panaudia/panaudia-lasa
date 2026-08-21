package timing

import (
	"fmt"
	"time"
)

type Ticker struct {
	previous   time.Time
	after      time.Time
	before     time.Time
	targetTime time.Time
	diff       time.Duration
	took       time.Duration
	totalSleep time.Duration
	totalTook  time.Duration
	sleep      time.Duration
	jitter     time.Duration
	dur        time.Duration
	//compensation   time.Duration
	ticks          int
	ms             int
	ticksPerSecond int
	print          bool
	tight          bool
}

func NewTicker(ms int, print bool) *Ticker {

	ticker := Ticker{}
	now := time.Now()
	noTime := now.Sub(now)
	ticker.previous = now
	ticker.after = now
	ticker.before = now
	ticker.totalSleep = noTime
	ticker.totalTook = noTime
	ticker.diff = noTime
	ticker.took = noTime
	ticker.sleep = noTime
	ticker.jitter = noTime
	ticker.ticks = 0
	ticker.ms = ms
	ticker.print = print
	ticker.tight = true
	ticker.ticksPerSecond = 1000 / ms
	//     fmt.Printf("ticker.ticks_per_second: %v\n", ticker.ticks_per_second)

	ticker.dur, _ = time.ParseDuration(fmt.Sprintf("%dms", ms))
	//ticker.compensation, _ = time.ParseDuration(fmt.Sprintf("%dns", 800000))
	ticker.targetTime = now.Add(ticker.dur)

	return &ticker
}

func NewTickerUs(us int, print bool) *Ticker {

	ticker := Ticker{}
	now := time.Now()
	noTime := now.Sub(now)
	ticker.previous = now
	ticker.after = now
	ticker.before = now
	ticker.totalSleep = noTime
	ticker.totalTook = noTime
	ticker.diff = noTime
	ticker.took = noTime
	ticker.sleep = noTime
	ticker.jitter = noTime
	ticker.ticks = 0
	ticker.ms = 0
	ticker.print = print
	ticker.tight = true
	ticker.ticksPerSecond = 1000000 / us

	ticker.dur = time.Duration(us) * time.Microsecond
	//ticker.compensation, _ = time.ParseDuration(fmt.Sprintf("%dns", 800000))
	ticker.targetTime = now.Add(ticker.dur)

	return &ticker
}

func NewLooseTicker(ms int, print bool) *Ticker {

	ticker := Ticker{}
	now := time.Now()
	noTime := now.Sub(now)
	ticker.previous = now
	ticker.after = now
	ticker.before = now
	ticker.totalSleep = noTime
	ticker.totalTook = noTime
	ticker.diff = noTime
	ticker.took = noTime
	ticker.sleep = noTime
	ticker.jitter = noTime
	ticker.ticks = 0
	ticker.ms = ms
	ticker.print = print
	ticker.tight = false
	ticker.ticksPerSecond = 1000 / ms
	//     fmt.Printf("ticker.ticks_per_second: %v\n", ticker.ticks_per_second)

	ticker.dur, _ = time.ParseDuration(fmt.Sprintf("%dms", ms))
	//ticker.compensation, _ = time.ParseDuration(fmt.Sprintf("%dns", 800000))
	ticker.targetTime = now.Add(ticker.dur)

	return &ticker
}

func NewOffTicker(ms int, offset int, print bool) *Ticker {

	ticker := Ticker{}
	now := time.Now()
	noTime := now.Sub(now)
	ticker.previous = now
	ticker.after = now
	ticker.before = now
	ticker.totalSleep = noTime
	ticker.totalTook = noTime
	ticker.diff = noTime
	ticker.took = noTime
	ticker.sleep = noTime
	ticker.jitter = noTime
	ticker.ticks = 0
	ticker.ms = ms
	ticker.print = print
	ticker.ticksPerSecond = 1000 / ms
	//     fmt.Printf("ticker.ticks_per_second: %v\n", ticker.ticks_per_second)

	ticker.dur, _ = time.ParseDuration(fmt.Sprintf("%dms", ms))
	off, _ := time.ParseDuration(fmt.Sprintf("%dns", offset))
	ticker.dur = ticker.dur + off
	//ticker.compensation, _ = time.ParseDuration(fmt.Sprintf("%dns", 800000))
	ticker.targetTime = now.Add(ticker.dur)

	return &ticker
}

func (ticker *Ticker) Tick() time.Duration {
	ticker.jitter = ticker.before.Sub(ticker.previous)

	//fmt.Printf("jitter: %v\n", ticker.jitter)

	ticker.after = time.Now()
	ticker.diff = ticker.targetTime.Sub(ticker.after)

	ticker.took = ticker.after.Sub(ticker.before)
	ticker.totalTook += ticker.took
	if ticker.diff > 0 {
		ticker.sleep = ticker.diff
		ticker.totalSleep += ticker.sleep
		time.Sleep(ticker.sleep)
		ticker.previous = ticker.targetTime
	} else {
		if ticker.tight {
			ticker.previous = ticker.targetTime
		} else {
			ticker.previous = ticker.after
		}
	}

	ticker.ticks++
	if ticker.ticks == ticker.ticksPerSecond {
		if ticker.print {
			//             println("sleep ms: ", total_sleep.Milliseconds())
			println("ms: ", ticker.totalTook.Milliseconds())
			//             fmt.Printf("target: %v\n", target_time)
		}
		ticker.ticks = 0
		ticker.totalSleep = 0
		ticker.totalTook = 0
	}
	ticker.targetTime = ticker.previous.Add(ticker.dur)
	ticker.before = time.Now()
	return ticker.took
}
