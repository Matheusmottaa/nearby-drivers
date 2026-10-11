package ride

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/twmb/franz-go/pkg/kgo"

	"github.com/Matheusmottaa/nearby-drivers/internal/event"
)

// WorkerGroup is the single consumer group of this process.
const WorkerGroup = "ride-workers"

// Kafka publishes outbox rows and consumes matched, match-failed and presence.
type Kafka struct {
	cl    *kgo.Client
	seeds []string
	log   *slog.Logger
}

// NewKafka returns a bus. seeds is the broker list passed to depend.OpenKafka.
func NewKafka(cl *kgo.Client, brokers string, log *slog.Logger) *Kafka {
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	return &Kafka{cl: cl, seeds: splitSeeds(brokers), log: log}
}

func (k *Kafka) Publish(ctx context.Context, topic, key string, value []byte) error {
	res := k.cl.ProduceSync(ctx, &kgo.Record{
		Topic: topic,
		Key:   []byte(key),
		Value: value,
	})
	if err := res.FirstErr(); err != nil {
		return fmt.Errorf("publish %s: %w", topic, err)
	}
	return nil
}

// Consume reads the worker topics. A record is committed after handle returns nil.
// It returns when ctx is canceled.
func (k *Kafka) Consume(ctx context.Context, handle func(context.Context, string, []byte) error) error {
	cl, err := kgo.NewClient(
		kgo.SeedBrokers(k.seeds...),
		kgo.ConsumerGroup(WorkerGroup),
		kgo.ConsumeTopics(event.TopicRidesMatched, event.TopicRidesMatchFailed, event.TopicDriversPresence),
		kgo.DisableAutoCommit(),
		kgo.DialTimeout(5*time.Second),
	)
	if err != nil {
		return fmt.Errorf("ride consumer: %w", err)
	}
	defer cl.Close()

	for {
		fetches := cl.PollFetches(ctx)
		if ctx.Err() != nil {
			return nil
		}
		if fetches.IsClientClosed() {
			return fmt.Errorf("ride consumer closed")
		}
		k.logLag(fetches)
		var handleErr error
		fetches.EachError(func(topic string, partition int32, err error) {
			if handleErr == nil {
				handleErr = fmt.Errorf("consume %s/%d: %w", topic, partition, err)
			}
		})
		fetches.EachRecord(func(r *kgo.Record) {
			if handleErr != nil {
				return
			}
			if err := handle(ctx, r.Topic, r.Value); err != nil {
				handleErr = err
				return
			}
			if err := cl.CommitRecords(ctx, r); err != nil && ctx.Err() == nil {
				handleErr = fmt.Errorf("commit %s/%d: %w", r.Topic, r.Partition, err)
			}
		})
		if handleErr != nil {
			return handleErr
		}
	}
}

func (k *Kafka) logLag(fetches kgo.Fetches) {
	fetches.EachTopic(func(t kgo.FetchTopic) {
		if t.Topic != event.TopicRidesMatched {
			return
		}
		t.EachPartition(func(p kgo.FetchPartition) {
			if len(p.Records) == 0 {
				return
			}
			last := p.Records[len(p.Records)-1].Offset
			k.log.Info("kafka.consumer.lag", "topic", t.Topic, "partition", p.Partition, "lag", p.HighWatermark-last-1)
		})
	})
}

func splitSeeds(s string) []string {
	parts := strings.Split(s, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p != "" {
			out = append(out, p)
		}
	}
	return out
}
