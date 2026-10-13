package migrate

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"runtime"
	"sync"
)

const (
	// compressionBlock bounds each worker's input while providing enough work to amortize scheduling.
	compressionBlock = 4 << 20
	// frameHeader includes gzip's fixed header, extra length, and our optional member metadata.
	frameHeader = 24
	// maxCompressedBlock bounds allocation for incompressible data and gzip framing overhead.
	maxCompressedBlock = compressionBlock + (compressionBlock >> 4) + frameHeader + 1024
)

// blockJob associates one input block with its ordered, buffered worker result.
type blockJob struct {
	// input remains owned by the job until processing completes.
	input []byte
	// result prevents canceled consumers from blocking a worker's completion.
	result chan blockResult
}

// blockResult contains processed bytes or the first error for one independent member.
type blockResult struct {
	// data is written in input order regardless of worker completion order.
	data []byte
	// err prevents a malformed or incomplete block from being published.
	err error
}

// blockPipeline bounds in-flight work and preserves order while using independent CPU workers.
type blockPipeline struct {
	// ctx interrupts blocked submissions and output waits.
	ctx context.Context
	// cancel releases workers after a sink or transform failure.
	cancel context.CancelFunc
	// jobs distributes CPU work among workers.
	jobs chan blockJob
	// ordered retains input order independently of scheduling.
	ordered chan blockJob
	// workers joins every worker before the pipeline owner returns.
	workers sync.WaitGroup
	// done reports the output goroutine's final result exactly once.
	done chan error
}

// workerCount uses the runtime CPU budget by default and rejects unreasonable explicit fan-out.
func workerCount(workers int) (int, error) {
	if workers == 0 {
		workers = runtime.GOMAXPROCS(0)
	}
	if workers < 1 || workers > 256 {
		return 0, errors.New("workers must be between 1 and 256, or 0 for the runtime CPU budget")
	}
	return workers, nil
}

// newBlockPipeline starts bounded processors and a single ordered output goroutine.
func newBlockPipeline(
	ctx context.Context,
	output io.Writer,
	workers int,
	process func([]byte) ([]byte, error),
) *blockPipeline {
	ctx, cancel := context.WithCancel(ctx)
	p := &blockPipeline{
		ctx: ctx, cancel: cancel,
		jobs: make(chan blockJob, workers), ordered: make(chan blockJob, workers), done: make(chan error, 1),
	}
	for range workers {
		p.workers.Add(1)
		go func() {
			defer p.workers.Done()
			for {
				select {
				case <-ctx.Done():
					return
				case job, ok := <-p.jobs:
					if !ok {
						return
					}
					data, err := process(job.input)
					job.result <- blockResult{data: data, err: err}
				}
			}
		}()
	}
	go func() { p.done <- p.flush(output) }()
	return p
}

// flush waits for each submitted block in order and cancels promptly on output failure.
func (p *blockPipeline) flush(output io.Writer) error {
	defer p.cancel()
	for {
		select {
		case <-p.ctx.Done():
			return p.ctx.Err()
		case job, ok := <-p.ordered:
			if !ok {
				return nil
			}
			select {
			case <-p.ctx.Done():
				return p.ctx.Err()
			case result := <-job.result:
				if result.err != nil {
					return result.err
				}
				if _, err := io.Copy(output, bytes.NewReader(result.data)); err != nil {
					return err
				}
			}
		}
	}
}

// submit transfers an input buffer to one worker and records its position in the output stream.
func (p *blockPipeline) submit(input []byte) error {
	job := blockJob{input: input, result: make(chan blockResult, 1)}
	select {
	case <-p.ctx.Done():
		return p.ctx.Err()
	case p.jobs <- job:
	}
	select {
	case <-p.ctx.Done():
		return p.ctx.Err()
	case p.ordered <- job:
		return nil
	}
}

// finish closes ownership channels and waits for all processors even after cancellation.
func (p *blockPipeline) finish(cause error) error {
	if cause == nil {
		cause = p.ctx.Err()
	}
	if cause != nil {
		p.cancel()
	}
	close(p.jobs)
	close(p.ordered)
	err := <-p.done
	p.workers.Wait()
	p.cancel()
	return errors.Join(cause, err)
}

// compressionWriter chunks a tar stream without retaining the complete archive in memory.
type compressionWriter struct {
	// pipeline owns all asynchronous processing and output.
	pipeline *blockPipeline
	// buffer holds only the currently incomplete member.
	buffer []byte
	// closed prevents duplicate channel closure.
	closed bool
	// result preserves the first close result for idempotent cleanup.
	result error
}

// compressionLevel selects standard-library presets without coupling workers to compression effort.
func compressionLevel(level string) (int, error) {
	switch level {
	case "", "fast":
		return gzip.BestSpeed, nil
	case "balanced":
		return gzip.DefaultCompression, nil
	case "best":
		return gzip.BestCompression, nil
	default:
		return 0, fmt.Errorf("unsupported compression %q; use fast, balanced, best, or none", level)
	}
}

// newCompressionWriter emits ordinary concatenated gzip members with optional parallel-decoding hints.
func newCompressionWriter(
	ctx context.Context,
	output io.Writer,
	workers int,
	level string,
) (*compressionWriter, error) {
	workers, err := workerCount(workers)
	if err != nil {
		return nil, err
	}
	selected, err := compressionLevel(level)
	if err != nil {
		return nil, err
	}
	pool := sync.Pool{}
	process := func(input []byte) ([]byte, error) {
		var buffer bytes.Buffer
		writer, ok := pool.Get().(*gzip.Writer)
		if !ok {
			var err error
			writer, err = gzip.NewWriterLevel(&buffer, selected)
			if err != nil {
				return nil, err
			}
		} else {
			writer.Reset(&buffer)
		}
		defer pool.Put(writer)
		writer.Extra = make([]byte, 12)
		copy(writer.Extra, "CTF1")
		// #nosec G115 -- Input is limited to the 4 MiB compressionBlock.
		binary.LittleEndian.PutUint32(writer.Extra[4:8], uint32(len(input)))
		if _, err := writer.Write(input); err != nil {
			return nil, errors.Join(err, writer.Close())
		}
		if err := writer.Close(); err != nil {
			return nil, err
		}
		data := buffer.Bytes()
		if len(data) > maxCompressedBlock {
			return nil, errors.New("compressed block exceeds its framing bound")
		}
		// The extra field is outside the gzip data CRC and can be filled after compression.
		// #nosec G115 -- The preceding maximum is less than 5 MiB.
		binary.LittleEndian.PutUint32(data[20:24], uint32(len(data)))
		return data, nil
	}
	return &compressionWriter{pipeline: newBlockPipeline(ctx, output, workers, process)}, nil
}

// Write fills bounded members and applies backpressure when CPU workers or the output are saturated.
func (w *compressionWriter) Write(data []byte) (int, error) {
	if w.closed {
		return 0, io.ErrClosedPipe
	}
	if err := w.pipeline.ctx.Err(); err != nil {
		return 0, err
	}
	written := 0
	for len(data) > 0 {
		if w.buffer == nil {
			w.buffer = make([]byte, 0, compressionBlock)
		}
		count := min(len(data), compressionBlock-len(w.buffer))
		w.buffer = append(w.buffer, data[:count]...)
		data = data[count:]
		written += count
		if len(w.buffer) == compressionBlock {
			if err := w.pipeline.submit(w.buffer); err != nil {
				return written, err
			}
			w.buffer = nil
		}
	}
	return written, nil
}

// Close flushes the final partial member and releases every goroutine before returning.
func (w *compressionWriter) Close() error {
	if w.closed {
		return w.result
	}
	w.closed = true
	var err error
	if len(w.buffer) > 0 {
		err = w.pipeline.submit(w.buffer)
	}
	w.buffer = nil
	w.result = w.pipeline.finish(err)
	return w.result
}

// decodeBlock validates an independent gzip member's declared size, checksum, and trailing bytes.
func decodeBlock(data []byte) ([]byte, error) {
	input := bytes.NewReader(data)
	reader, err := gzip.NewReader(input)
	if err != nil {
		return nil, err
	}
	reader.Multistream(false)
	declared := int64(binary.LittleEndian.Uint32(data[16:20]))
	result := make([]byte, int(declared))
	_, err = io.ReadFull(reader, result)
	if err == nil {
		var extra [1]byte
		_, tailErr := reader.Read(extra[:])
		if !errors.Is(tailErr, io.EOF) || input.Len() != 0 {
			if tailErr != nil {
				err = tailErr
			} else {
				err = errors.New("gzip member does not match its declared size")
			}
		}
	}
	if err := errors.Join(err, reader.Close()); err != nil {
		return nil, err
	}
	return result, nil
}

// readCompressedBlock bounds allocation before accepting an untrusted member's optional hints.
func readCompressedBlock(input io.Reader) ([]byte, error) {
	var header [frameHeader]byte
	if _, err := io.ReadFull(input, header[:]); err != nil {
		return nil, err
	}
	valid := bytes.Equal(header[:4], []byte{0x1f, 0x8b, 8, 4}) &&
		binary.LittleEndian.Uint16(header[10:12]) == 12 && string(header[12:16]) == "CTF1"
	rawSize := binary.LittleEndian.Uint32(header[16:20])
	compressedSize := binary.LittleEndian.Uint32(header[20:24])
	if !valid || rawSize == 0 || rawSize > compressionBlock ||
		compressedSize < frameHeader+8 || compressedSize > maxCompressedBlock {
		return nil, errors.New("invalid compressed member framing")
	}
	data := make([]byte, int(compressedSize))
	copy(data, header[:])
	_, err := io.ReadFull(input, data[frameHeader:])
	return data, err
}

// decodeArchive restores bounded gzip members in parallel while remaining compatible with standard gzip tools.
func decodeArchive(input io.Reader, encoding string, workers int) (io.Reader, func(), error) {
	if encoding == "" || encoding == "none" {
		return input, func() {}, nil
	}
	if encoding != "gzip" {
		return nil, nil, fmt.Errorf("unsupported archive encoding %q", encoding)
	}
	workers, err := workerCount(workers)
	if err != nil {
		return nil, nil, err
	}
	ctx, cancel := context.WithCancel(context.Background())
	reader, writer := io.Pipe()
	complete := make(chan struct{})
	go func() {
		defer close(complete)
		pipeline := newBlockPipeline(ctx, writer, workers, decodeBlock)
		var cause error
		for {
			data, err := readCompressedBlock(input)
			if errors.Is(err, io.EOF) {
				break
			}
			if err != nil {
				cause = err
				break
			}
			if err := pipeline.submit(data); err != nil {
				cause = err
				break
			}
		}
		_ = writer.CloseWithError(pipeline.finish(cause))
	}()
	closeReader := func() {
		cancel()
		_ = reader.Close()
		<-complete
	}
	return reader, closeReader, nil
}
