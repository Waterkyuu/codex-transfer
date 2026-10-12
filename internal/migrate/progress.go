package migrate

import (
	"fmt"
	"io"
	"sync"
	"sync/atomic"
	"time"
)

// progress counts successfully transferred archive bytes and owns its reporting goroutine.
type progress struct {
	// bytes is updated by the transfer stream and read by the reporting goroutine.
	bytes atomic.Int64
	// total is the negotiated archive length in bytes.
	total int64
	// output receives progress lines only while this reporter owns the stream.
	output io.Writer
	// done requests reporting shutdown.
	done chan struct{}
	// finished acknowledges shutdown before callers write subsequent status messages.
	finished chan struct{}
	// once allows cleanup from both failure and success paths.
	once sync.Once
}

// newProgress starts periodic throughput reporting for one transfer stream.
func newProgress(output io.Writer, total int64) *progress {
	p := &progress{total: total, output: output, done: make(chan struct{}), finished: make(chan struct{})}
	go p.report()
	return p
}

// Write records stream bytes without retaining sensitive payloads.
func (p *progress) Write(data []byte) (int, error) {
	p.bytes.Add(int64(len(data)))
	return len(data), nil
}

// stop waits for the reporting goroutine to relinquish its output writer.
func (p *progress) stop() {
	p.once.Do(func() { close(p.done) })
	<-p.finished
}

// report displays instantaneous throughput and a final average rate, including fast transfers.
func (p *progress) report() {
	defer close(p.finished)
	start := time.Now()
	previousTime := start
	var previousBytes int64
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		select {
		case now := <-ticker.C:
			current := p.bytes.Load()
			p.print(current, float64(current-previousBytes)/now.Sub(previousTime).Seconds(), now.Sub(start))
			previousBytes, previousTime = current, now
		case <-p.done:
			elapsed := time.Since(start)
			current := p.bytes.Load()
			p.print(current, float64(current)/elapsed.Seconds(), elapsed)
			return
		}
	}
}

// print uses MiB and seconds so network rates are comparable across platforms.
func (p *progress) print(current int64, rate float64, elapsed time.Duration) {
	percent := 100 * float64(current) / float64(p.total)
	_, _ = fmt.Fprintf(p.output, "%.1f%% | %.2f / %.2f MiB | %.2f MiB/s | %.1fs\n",
		percent,
		float64(current)/(1<<20),
		float64(p.total)/(1<<20),
		rate/(1<<20),
		elapsed.Seconds(),
	)
}
