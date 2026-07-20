package history

import (
	"context"
	"errors"
	"sync/atomic"
	"time"
)

type WriterOptions struct {
	QueueCapacity  int
	BatchSize      int
	FlushInterval  time.Duration
	SoftQuotaBytes int64
}

type WriterStats struct {
	Submitted           uint64
	Written             uint64
	DroppedBackpressure uint64
	DroppedQuota        uint64
	DroppedWriteError   uint64
	WriteErrors         uint64
	Quota               QuotaState
}

type queuedRecord struct {
	host       *HostSample
	container  *ContainerSample
	transition *ServiceTransition
}

type Writer struct {
	repository    WriterRepository
	queue         chan queuedRecord
	batchSize     int
	flushInterval time.Duration
	softQuota     int64

	submitted           atomic.Uint64
	written             atomic.Uint64
	droppedBackpressure atomic.Uint64
	droppedQuota        atomic.Uint64
	droppedWriteError   atomic.Uint64
	writeErrors         atomic.Uint64
	quota               atomic.Value
}

func NewWriter(repository WriterRepository, options WriterOptions) (*Writer, error) {
	if repository == nil {
		return nil, errors.New("history writer requires a repository")
	}
	if options.QueueCapacity <= 0 {
		options.QueueCapacity = DefaultQueueCapacity
	}
	if options.BatchSize <= 0 {
		options.BatchSize = DefaultBatchSize
	}
	if options.FlushInterval <= 0 {
		options.FlushInterval = DefaultFlushInterval
	}
	if options.FlushInterval > DefaultFlushInterval {
		return nil, errors.New("history flush interval must not exceed 10 seconds")
	}
	if options.SoftQuotaBytes <= 0 {
		options.SoftQuotaBytes = DefaultSoftQuota
	}
	writer := &Writer{
		repository:    repository,
		queue:         make(chan queuedRecord, options.QueueCapacity),
		batchSize:     options.BatchSize,
		flushInterval: options.FlushInterval,
		softQuota:     options.SoftQuotaBytes,
	}
	writer.quota.Store(QuotaState{LimitBytes: options.SoftQuotaBytes})
	return writer, nil
}

func (w *Writer) RecordHost(sample HostSample) bool {
	if sample.NodeID == "" {
		sample.NodeID = LocalNodeID
	}
	return w.enqueue(queuedRecord{host: &sample})
}

func (w *Writer) RecordContainer(sample ContainerSample) bool {
	if sample.NodeID == "" {
		sample.NodeID = LocalNodeID
	}
	return w.enqueue(queuedRecord{container: &sample})
}

func (w *Writer) RecordServiceTransition(transition ServiceTransition) bool {
	if transition.NodeID == "" {
		transition.NodeID = LocalNodeID
	}
	return w.enqueue(queuedRecord{transition: &transition})
}

func (w *Writer) enqueue(record queuedRecord) bool {
	w.submitted.Add(1)
	select {
	case w.queue <- record:
		return true
	default:
		w.droppedBackpressure.Add(1)
		return false
	}
}

// Run owns all calls that mutate history storage. Callers may submit records
// concurrently; a single Run goroutine guarantees one SQLite history writer.
func (w *Writer) Run(ctx context.Context) error {
	if err := w.refreshQuota(ctx); err != nil && ctx.Err() == nil {
		w.writeErrors.Add(1)
	}
	ticker := time.NewTicker(w.flushInterval)
	defer ticker.Stop()

	pending := make([]queuedRecord, 0, w.batchSize)
	for {
		select {
		case <-ctx.Done():
			shutdownContext := context.WithoutCancel(ctx)
			for {
				select {
				case record := <-w.queue:
					pending = append(pending, record)
					if len(pending) >= w.batchSize {
						w.flush(shutdownContext, pending)
						pending = pending[:0]
					}
				default:
					w.flush(shutdownContext, pending)
					return ctx.Err()
				}
			}
		case record := <-w.queue:
			pending = append(pending, record)
			if len(pending) >= w.batchSize {
				w.flush(ctx, pending)
				pending = pending[:0]
			}
		case <-ticker.C:
			w.flush(ctx, pending)
			pending = pending[:0]
		}
	}
}

func (w *Writer) flush(ctx context.Context, records []queuedRecord) {
	if len(records) == 0 {
		_ = w.refreshQuota(ctx)
		return
	}
	if err := w.refreshQuota(ctx); err != nil {
		w.writeErrors.Add(1)
	}
	quota := w.QuotaState()
	batch := Batch{}
	for _, record := range records {
		switch {
		case record.host != nil:
			if quota.Full {
				w.droppedQuota.Add(1)
			} else {
				batch.Hosts = append(batch.Hosts, *record.host)
			}
		case record.container != nil:
			if quota.Full {
				w.droppedQuota.Add(1)
			} else {
				batch.Containers = append(batch.Containers, *record.container)
			}
		case record.transition != nil:
			batch.ServiceTransitions = append(batch.ServiceTransitions, *record.transition)
		}
	}
	if batch.Len() == 0 {
		return
	}
	if err := w.repository.WriteHistoryBatch(ctx, batch); err != nil {
		w.writeErrors.Add(1)
		w.droppedWriteError.Add(uint64(batch.Len()))
		return
	}
	w.written.Add(uint64(batch.Len()))
	_ = w.refreshQuota(ctx)
}

func (w *Writer) refreshQuota(ctx context.Context) error {
	used, err := w.repository.HistorySizeBytes(ctx)
	if err != nil {
		return err
	}
	ratio := float64(used) / float64(w.softQuota)
	w.quota.Store(QuotaState{
		UsedBytes:  used,
		LimitBytes: w.softQuota,
		Ratio:      ratio,
		Warning:    ratio >= 0.8,
		Full:       ratio >= 1,
	})
	return nil
}

func (w *Writer) QuotaState() QuotaState {
	return w.quota.Load().(QuotaState)
}

func (w *Writer) Stats() WriterStats {
	return WriterStats{
		Submitted:           w.submitted.Load(),
		Written:             w.written.Load(),
		DroppedBackpressure: w.droppedBackpressure.Load(),
		DroppedQuota:        w.droppedQuota.Load(),
		DroppedWriteError:   w.droppedWriteError.Load(),
		WriteErrors:         w.writeErrors.Load(),
		Quota:               w.QuotaState(),
	}
}
